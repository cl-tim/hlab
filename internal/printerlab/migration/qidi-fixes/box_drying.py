"""Timed QIDI Box drying. Python 3.7; no motion or persistent runtime state."""
import configparser
import logging
import math
import re

FILAMENT_FILE = '/home/mks/printer_data/config/officiall_filas_list.cfg'
MAX_TEMPERATURE = 65.0
CHECK_INTERVAL = 2.0


def finite_number(value):
    if isinstance(value, bool):
        raise ValueError('Expected a finite number')
    number = float(value)
    if not math.isfinite(number):
        raise ValueError('Expected a finite number')
    return number


def filament_limits(path):
    """Use the printer's own QIDI limits, including its firmware-specific IDs."""
    parser = configparser.RawConfigParser()
    with open(path, 'r', encoding='utf-8') as stream:
        text = stream.read(65537)
    if len(text) > 65536:
        raise ValueError('Filament metadata exceeds its size limit')
    parser.read_string(text)
    limits = {}
    for section in parser.sections():
        match = re.fullmatch(r'fila([0-9]+)', section)
        if match:
            # QIDI includes unused rows with blank limits. Such IDs remain
            # unknown and cannot be heated when loaded; valid rows still work.
            try:
                limit = finite_number(parser.get(section, 'box_max_temp', fallback=''))
            except (ValueError, TypeError, OverflowError):
                continue
            if limit >= 0:
                limits[int(match.group(1))] = min(limit, MAX_TEMPERATURE)
    if not limits:
        raise ValueError('No supported filament metadata')
    return limits


class BoxDrying:
    def __init__(self, config):
        self.printer = config.get_printer()
        self.reactor = self.printer.get_reactor()
        self.gcode = self.printer.lookup_object('gcode')
        self.heaters = self.printer.load_object(config, 'heaters')
        self.filament_file = config.get('filament_file', FILAMENT_FILE)
        self.sessions = {}
        self.timer = self.reactor.register_timer(self._tick, self.reactor.NEVER)
        self.gcode.register_command('KLIPPEE_BOX_DRY', self.cmd_start,
                                    desc='Dry a Box: BOX=1 TEMP=45 HOURS=4 HUMIDITY=20')
        self.gcode.register_command('KLIPPEE_BOX_DRY_STOP', self.cmd_stop,
                                    desc='Stop a managed Box drying cycle: BOX=1')
        # An all-heaters-off command must also cancel humidity-paused cycles.
        self.all_off = self.gcode.register_command('TURN_OFF_HEATERS', None)
        self.gcode.register_command('TURN_OFF_HEATERS', self.cmd_all_off)
        for event in ('klippy:shutdown', 'klippy:disconnect', 'gcode:request_restart'):
            self.printer.register_event_handler(event, self._cancel_all)

    def _variables(self, now):
        return self.printer.lookup_object('save_variables').get_status(now)['variables']

    def _limit(self, box, now, limits):
        variables = self._variables(now)
        count = variables.get('box_count')
        if type(count) is not int or not 1 <= box <= min(count, 4):
            raise ValueError('Requested Box is unavailable')
        limit = MAX_TEMPERATURE
        for slot in range((box - 1) * 4, box * 4):
            status = self.printer.lookup_object('box_stepper slot%d' % slot).get_status(now)
            present = status.get('runout_button')
            if present not in (0, 1):
                raise ValueError('Box filament sensor state is unavailable')
            if present == 0:
                identifier = variables.get('filament_slot%d' % slot)
                if isinstance(identifier, bool) or not re.fullmatch(r'[0-9]+', str(identifier)):
                    raise ValueError('Loaded filament identification is unavailable')
                if int(identifier) not in limits:
                    raise ValueError('Loaded filament has no QIDI drying limit')
                limit = min(limit, limits[int(identifier)])
        return limit

    def _humidity(self, box, now):
        sensor = self.printer.lookup_object('aht20_f heater_box%d' % box)
        humidity = finite_number(sensor.get_status(now)['humidity'])
        if not 0 <= humidity <= 100:
            raise ValueError('Box humidity reading is invalid')
        return humidity

    def _target(self, heater, now):
        return finite_number(heater.get_status(now)['target'])

    def _printing(self, now):
        stats = self.printer.lookup_object('print_stats', None)
        return stats is None or stats.get_status(now).get('state') not in ('standby', 'complete', 'cancelled', 'error')

    def _schedule(self, now):
        deadlines = [s['deadline'] for s in self.sessions.values() if s['deadline'] is not None]
        when = min([now + CHECK_INTERVAL] + deadlines) if self.sessions else self.reactor.NEVER
        self.reactor.update_timer(self.timer, when)

    def cmd_start(self, gcmd):
        now = self.reactor.monotonic()
        box = gcmd.get_int('BOX', 1, minval=1, maxval=4)
        try:
            hours = finite_number(gcmd.get_float('HOURS', 4.0))
            humidity = finite_number(gcmd.get_float('HUMIDITY', 0.0))
            if not 0 <= hours <= 48 or not 0 <= humidity <= 90:
                raise ValueError('HOURS must be 0..48 and HUMIDITY 0..90')
            if self._printing(now):
                raise ValueError('Start drying only while the printer is idle')
            limit = self._limit(box, now, filament_limits(self.filament_file))
            temp = finite_number(gcmd.get_float('TEMP', limit))
            if not 0 < temp <= limit:
                raise ValueError('Temperature exceeds the loaded filaments drying limit')
            heater = self.heaters.lookup_heater('heater_box%d' % box)
            heating = not humidity or self._humidity(box, now) > humidity
        except Exception as exc:
            raise gcmd.error('Box drying refused: %s' % exc)
        expected = temp if heating else 0.0
        heater.set_temp(expected)
        self.sessions[box] = dict(heater=heater, temperature=temp, humidity=humidity,
                                  expected=expected, deadline=now + hours * 3600 if hours else None)
        self._schedule(now)
        gcmd.respond_info('Box %d drying started%s' % (box, ' until stopped' if not hours else ''))

    def _stop(self, box, reason, now):
        session = self.sessions.pop(box, None)
        if session is not None:
            # Do not override a temperature subsequently set by another controller.
            try:
                owned = abs(self._target(session['heater'], now) - session['expected']) < 0.000001
            except Exception:
                owned = True
            if owned:
                session['heater'].set_temp(0.0)
            self.gcode.respond_info('Box %d drying stopped: %s' % (box, reason))

    def cmd_stop(self, gcmd):
        now = self.reactor.monotonic()
        self._stop(gcmd.get_int('BOX', 1, minval=1, maxval=4), 'requested', now)
        self._schedule(now)

    def _cancel_all(self, *args):
        now = self.reactor.monotonic()
        for box in list(self.sessions):
            self._stop(box, 'all heaters off or printer stopping', now)
        self._schedule(now)

    def cmd_all_off(self, gcmd):
        try:
            self._cancel_all()
        finally:
            self.sessions.clear()
            self._schedule(self.reactor.monotonic())
            self.all_off(gcmd)

    def _tick(self, now):
        if not self.sessions:
            return self.reactor.NEVER
        try:
            limits = filament_limits(self.filament_file)
        except Exception:
            limits = None
        for box, session in list(self.sessions.items()):
            try:
                if abs(self._target(session['heater'], now) - session['expected']) >= 0.000001:
                    self._stop(box, 'heater changed elsewhere', now)
                    continue
                if session['deadline'] is not None and now >= session['deadline']:
                    self._stop(box, 'time elapsed', now)
                    continue
                if self._printing(now):
                    raise ValueError('printing started')
                if limits is None or self._limit(box, now, limits) < session['temperature']:
                    raise ValueError('filament limits changed or are unavailable')
                if session['humidity']:
                    rh = self._humidity(box, now)
                    expected = session['expected']
                    if expected and rh <= session['humidity']:
                        expected = 0.0
                    elif not expected and rh >= session['humidity'] + 5:
                        expected = session['temperature']
                    if expected != session['expected']:
                        session['heater'].set_temp(expected)
                        session['expected'] = expected
            except Exception:
                logging.exception('Box drying safety check failed for Box %d', box)
                self._stop(box, 'safety check failed', now)
        deadlines = [s['deadline'] for s in self.sessions.values() if s['deadline'] is not None]
        return min([now + CHECK_INTERVAL] + deadlines) if self.sessions else self.reactor.NEVER

    def get_status(self, eventtime):
        return {'active_boxes': sorted(self.sessions), 'boxes': {
            box: {'temperature': s['temperature'], 'humidity': s['humidity'],
                  'remaining_seconds': max(0.0, s['deadline'] - eventtime) if s['deadline'] is not None else None}
            for box, s in self.sessions.items()}}


def load_config(config):
    return BoxDrying(config)
