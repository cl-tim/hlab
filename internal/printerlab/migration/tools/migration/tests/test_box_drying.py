"""Box drying with synthetic heaters, sensors and a controllable monotonic clock."""
import ast
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

SOURCE = Path(__file__).resolve().parents[3] / 'qidi-fixes/box_drying.py'
spec = importlib.util.spec_from_file_location('box_drying', SOURCE)
drying = importlib.util.module_from_spec(spec)
spec.loader.exec_module(drying)
POLICY = '[fila1]\nbox_max_temp=0\n[fila18]\nbox_max_temp=45\n[fila25]\nbox_max_temp=65\n'


class Command:
    def __init__(self, **params):
        self.params = params
        self.messages = []

    def get_int(self, name, default, minval, maxval):
        value = int(self.params.get(name, default))
        if not minval <= value <= maxval:
            raise ValueError('Invalid box')
        return value

    def get_float(self, name, default):
        return float(self.params.get(name, default))

    def error(self, message):
        return ValueError(message)

    def respond_info(self, message):
        self.messages.append(message)


class Status:
    def __init__(self, **status):
        self.status = status

    def get_status(self, now):
        return self.status


class Heater(Status):
    def __init__(self):
        super().__init__(target=0.0)
        self.writes = []

    def set_temp(self, target):
        self.status['target'] = target
        self.writes.append(target)


class Reactor:
    NEVER = float('inf')

    def __init__(self):
        self.now = 100.0

    def monotonic(self):
        return self.now

    def register_timer(self, callback, when):
        self.callback, self.when = callback, when
        return 'timer'

    def update_timer(self, timer, when):
        self.when = when


class Gcode:
    def __init__(self, heaters):
        self.all_off = Mock(side_effect=lambda command: [heater.set_temp(0) for heater in heaters.values()])
        self.commands = {'TURN_OFF_HEATERS': self.all_off}
        self.messages = []

    def register_command(self, name, callback, **kwargs):
        previous = self.commands.pop(name, None)
        if callback is not None:
            self.commands[name] = callback
        return previous

    def respond_info(self, message):
        self.messages.append(message)


class DryingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.policy = Path(self.temp.name) / 'filaments.cfg'
        self.policy.write_text(POLICY)
        self.reactor = Reactor()
        self.heaters = {'heater_box1': Heater(), 'heater_box2': Heater()}
        self.variables = {'box_count': 2, 'filament_slot0': 18, 'filament_slot4': 25}
        self.objects = {
            'save_variables': Status(variables=self.variables), 'print_stats': Status(state='standby'),
            'aht20_f heater_box1': Status(humidity=40), 'aht20_f heater_box2': Status(humidity=40),
        }
        for slot in range(8):
            self.objects['box_stepper slot%d' % slot] = Status(runout_button=0 if slot in (0, 4) else 1)
        self.gcode = Gcode(self.heaters)
        self.objects['gcode'] = self.gcode
        self.printer = Mock()
        self.printer.get_reactor.return_value = self.reactor
        self.printer.lookup_object.side_effect = self.lookup
        manager = Mock()
        manager.lookup_heater.side_effect = lambda name: self.heaters[name]
        self.printer.load_object.return_value = manager
        config = Mock()
        config.get_printer.return_value = self.printer
        config.get.side_effect = lambda name, default: str(self.policy) if name == 'filament_file' else default
        self.controller = drying.load_config(config)
        logger = patch.object(drying.logging, 'exception')
        logger.start()
        self.addCleanup(logger.stop)

    def lookup(self, name, *default):
        if name in self.objects:
            return self.objects[name]
        if default:
            return default[0]
        raise ValueError('Missing object ' + name)

    def start(self, **params):
        self.controller.cmd_start(Command(**params))

    def tick(self, now):
        self.reactor.now = now
        return self.controller._tick(now)

    def test_python37_syntax_and_default_bounded_cycle(self):
        ast.parse(SOURCE.read_text(), feature_version=(3, 7))
        self.start()
        self.assertEqual(self.heaters['heater_box1'].status['target'], 45)
        self.assertEqual(self.controller.sessions[1]['deadline'], 100 + 4 * 3600)
        self.assertEqual(self.reactor.when, 102)
        self.assertEqual(self.controller.get_status(110)['boxes'][1]['remaining_seconds'], 14390)

    def test_small_positive_duration_expires_without_rounding_to_forever(self):
        self.start(HOURS=0.001)
        self.assertAlmostEqual(self.controller.sessions[1]['deadline'], 103.6)
        self.assertAlmostEqual(self.tick(102), 103.6)
        self.assertEqual(self.tick(103.6), self.reactor.NEVER)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)

    def test_zero_duration_is_explicitly_untimed(self):
        self.start(HOURS=0)
        self.assertIsNone(self.controller.sessions[1]['deadline'])
        self.tick(100000)
        self.assertIn(1, self.controller.sessions)

    def test_starting_another_box_does_not_extend_first_deadline(self):
        self.start(HOURS=1)
        self.reactor.now = 130
        self.start(BOX=2, HOURS=1)
        self.assertEqual(self.controller.sessions[1]['deadline'], 3700)
        self.assertEqual(self.tick(3700), 3702)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)
        self.assertEqual(self.heaters['heater_box2'].status['target'], 65)
        self.tick(4000)
        self.assertFalse(self.controller.sessions)

    def test_bad_parameters_and_unsafe_loaded_filaments_never_heat(self):
        for params in ({'HOURS': -1}, {'HOURS': 49}, {'HOURS': float('nan')}, {'HOURS': float('inf')},
                       {'TEMP': 46}, {'TEMP': float('nan')}, {'TEMP': 0},
                       {'HUMIDITY': float('inf')}, {'HUMIDITY': 91}, {'BOX': 3}):
            with self.subTest(params=params), self.assertRaises(ValueError):
                self.start(**params)
        for identifier in (1, 999, None, True, '18.5'):
            self.variables['filament_slot0'] = identifier
            with self.subTest(identifier=identifier), self.assertRaises(ValueError):
                self.start()
        self.assertEqual(self.heaters['heater_box1'].writes, [])

    def test_limits_are_rechecked_for_inserted_and_reidentified_filaments(self):
        for identifier in (1, 18, 999):
            self.variables['filament_slot0'] = 25
            self.start(TEMP=65)
            self.objects['box_stepper slot1'].status['runout_button'] = 0
            self.variables['filament_slot1'] = identifier
            self.tick(self.reactor.now + 2)
            self.assertNotIn(1, self.controller.sessions)
            self.assertEqual(self.heaters['heater_box1'].status['target'], 0)
            self.objects['box_stepper slot1'].status['runout_button'] = 1

    def test_modified_or_missing_filament_policy_stops_heating(self):
        self.start()
        self.policy.write_text(POLICY.replace('box_max_temp=45', 'box_max_temp=40'))
        self.tick(102)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)
        self.policy.write_text(POLICY)
        self.start()
        self.policy.unlink()
        self.tick(104)
        self.assertFalse(self.controller.sessions)

    def test_humidity_hysteresis_and_filament_checks_while_paused(self):
        self.start(HUMIDITY=20)
        sensor = self.objects['aht20_f heater_box1'].status
        sensor['humidity'] = 19
        self.tick(102)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)
        sensor['humidity'] = 24
        self.tick(104)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)
        sensor['humidity'] = 26
        self.tick(106)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 45)
        sensor['humidity'] = 19
        self.tick(108)
        self.variables['filament_slot0'] = 1
        sensor['humidity'] = 40
        self.tick(110)
        self.assertFalse(self.controller.sessions)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)

    def test_invalid_humidity_stops_and_missing_humidity_refuses_to_start(self):
        self.start(HUMIDITY=20)
        self.objects['aht20_f heater_box1'].status['humidity'] = float('nan')
        self.tick(102)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)
        del self.objects['aht20_f heater_box1']
        with self.assertRaises(ValueError):
            self.start(HUMIDITY=20)

    def test_external_heater_target_is_left_alone_and_other_box_continues(self):
        self.start()
        self.start(BOX=2)
        self.heaters['heater_box1'].set_temp(30)
        self.tick(102)
        self.assertNotIn(1, self.controller.sessions)
        self.assertIn(2, self.controller.sessions)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 30)

    def test_all_heaters_off_cancels_a_humidity_paused_cycle(self):
        self.objects['aht20_f heater_box1'].status['humidity'] = 10
        self.start(HUMIDITY=20)
        self.gcode.commands['TURN_OFF_HEATERS'](Command())
        self.gcode.all_off.assert_called_once()
        self.objects['aht20_f heater_box1'].status['humidity'] = 80
        self.tick(102)
        self.assertFalse(self.controller.sessions)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)

    def test_core_all_heaters_off_still_runs_if_extension_cleanup_fails(self):
        self.start()
        with patch.object(self.controller, '_cancel_all', side_effect=ValueError('sensor failed')):
            with self.assertRaises(ValueError):
                self.gcode.commands['TURN_OFF_HEATERS'](Command())
        self.gcode.all_off.assert_called_once()
        self.assertFalse(self.controller.sessions)
        self.assertEqual(self.reactor.when, self.reactor.NEVER)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)

    def test_print_start_shutdown_and_restart_stop_owned_heat(self):
        self.start()
        self.objects['print_stats'].status['state'] = 'printing'
        self.tick(102)
        self.assertFalse(self.controller.sessions)
        with self.assertRaises(ValueError):
            self.start()
        self.objects['print_stats'].status['state'] = 'standby'
        for event in ('klippy:shutdown', 'klippy:disconnect', 'gcode:request_restart'):
            self.start()
            handler = next(call.args[1] for call in self.printer.register_event_handler.call_args_list if call.args[0] == event)
            handler()
            self.assertFalse(self.controller.sessions)
            self.assertEqual(self.heaters['heater_box1'].status['target'], 0)

    def test_missing_sensors_or_unavailable_box_fail_closed(self):
        self.start()
        del self.objects['box_stepper slot1']
        self.tick(102)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)
        with self.assertRaises(ValueError):
            self.start()
        del self.objects['print_stats']
        with self.assertRaises(ValueError):
            self.start()

    def test_policy_caps_at_65_and_refuses_invalid_values(self):
        self.policy.write_text('[fila25]\nbox_max_temp=90\n')
        self.assertEqual(drying.filament_limits(str(self.policy)), {25: 65})
        for value in ('nan', '-1', 'unknown'):
            self.policy.write_text('[fila25]\nbox_max_temp=' + value + '\n')
            with self.assertRaises(ValueError):
                drying.filament_limits(str(self.policy))

    def test_unused_vendor_rows_remain_unknown_without_disabling_valid_filaments(self):
        self.policy.write_text(POLICY + '[fila9]\nbox_max_temp=\n[fila10]\nbox_max_temp=nan\n')
        limits = drying.filament_limits(str(self.policy))
        self.assertNotIn(9, limits)
        self.assertNotIn(10, limits)
        self.start()
        self.variables['filament_slot0'] = 9
        self.tick(102)
        self.assertFalse(self.controller.sessions)
        self.assertEqual(self.heaters['heater_box1'].status['target'], 0)


if __name__ == '__main__':
    unittest.main()
