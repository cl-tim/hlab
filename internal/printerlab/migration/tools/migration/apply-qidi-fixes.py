#!/usr/bin/env python3
"""Stock Plus4 maintenance transaction. Python 3.7; no workstation Python.

Uses the updater's loopback API, idle/heater checks and shared firmware lock.
The workstation verifies a recovery archive before allowing any live edits.
"""
import base64
import fcntl
import glob
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tarfile
import tempfile
import time
import urllib.parse


TRANSACTIONS = Path('/var/lib/hlab/qidi-fixes')
TIMER = 'qidi-journal-archive-cleanup.timer'
USB_SOCKET = 'hlab-usb-events.socket'
USB_SERVICE = 'hlab-usb-events.service'
USB_SCRIPT = '/usr/local/lib/hlab/usb_events.py'
USB_FILES = {
    '/etc/udev/rules.d/99-hlab-usb-events.rules': '''# hlab: no device serial numbers; storage IO runs outside udev.
ACTION=="add|remove", SUBSYSTEM=="usb", ENV{DEVTYPE}=="usb_device", RUN+="/usr/bin/python3 -I /usr/local/lib/hlab/usb_events.py emit $env{ACTION} %k $env{PRODUCT}"
''',
    '/etc/systemd/system/hlab-usb-events.socket': '''[Unit]
Description=hlab USB evidence queue

[Socket]
ListenDatagram=/run/hlab-usb-events.sock
SocketMode=0600
RemoveOnStop=true

[Install]
WantedBy=sockets.target
''',
    '/etc/systemd/system/hlab-usb-events.service': '''[Unit]
Description=hlab persistent USB evidence
Requires=hlab-usb-events.socket
After=hlab-usb-events.socket

[Service]
Type=notify
TimeoutStartSec=10
ExecStart=/usr/bin/python3 -I /usr/local/lib/hlab/usb_events.py serve
Restart=on-failure
RestartSec=2
StateDirectory=hlab-usb-events
StateDirectoryMode=0755
UMask=0022
NoNewPrivileges=true
ProtectHome=true
ProtectSystem=strict
PrivateTmp=true
''',
}
LOG_FILES = {
    '/etc/systemd/journald.conf.d/zz-qidi-log-limits.conf': '''[Journal]
SystemMaxUse=16M
SystemKeepFree=16M
SystemMaxFileSize=2M
SystemMaxFiles=8
RuntimeMaxUse=8M
RuntimeKeepFree=16M
RuntimeMaxFileSize=2M
MaxRetentionSec=7day
RateLimitIntervalSec=30s
RateLimitBurst=1000
''',
    '/etc/systemd/system/qidi-journal-archive-cleanup.service': '''[Unit]
Description=Bound Armbian disk journal backups before the next boot
After=armbian-ramlog.service
ConditionPathIsDirectory=/var/log.hdd/journal

[Service]
Type=oneshot
Nice=19
IOSchedulingClass=idle
ExecStart=/usr/bin/journalctl --quiet --directory=/var/log.hdd/journal --vacuum-size=12M --vacuum-files=8 --vacuum-time=7day
''',
    '/etc/systemd/system/qidi-journal-archive-cleanup.timer': '''[Unit]
Description=Regular cleanup of Armbian journal backups

[Timer]
OnBootSec=2min
OnUnitActiveSec=15min
AccuracySec=1min

[Install]
WantedBy=timers.target
''',
}
HEADERS = re.compile(r'^\s*\[([^]\r\n]+)\]\s*(?:[#;].*)?$', re.M)


class FixError(Exception):
    pass


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read_config(path):
    # Preserve line endings in files that are only inspected, including add-ons.
    with path.open('r', encoding='utf-8', newline='') as stream:
        return stream.read()


def safe_path(path):
    path = Path(path)
    if not path.is_absolute() or '..' in path.parts:
        raise FixError('Noncanonical maintenance path')
    for item in [path] + list(path.parents):
        if item.is_symlink():
            raise FixError('Symlinked maintenance path: ' + str(item))
    if path.exists() and not path.is_file():
        raise FixError('Maintenance target is not a regular file: ' + str(path))
    return path


def snapshot(path):
    path = safe_path(path)
    if not path.exists():
        return None
    metadata = path.stat()
    return dict(sha256=digest(path.read_bytes()), mode=stat.S_IMODE(metadata.st_mode),
                uid=metadata.st_uid, gid=metadata.st_gid)


def atomic_write(path, data, metadata):
    path = safe_path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix='.hlab-fixes-', dir=str(path.parent))
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(data)
            stream.flush()
            os.fchown(stream.fileno(), metadata['uid'], metadata['gid'])
            os.fchmod(stream.fileno(), metadata['mode'])
            os.fsync(stream.fileno())
        os.replace(temporary, str(path))
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def configs(root):
    """Resolve Klipper includes without rewriting, flattening or duplicating them."""
    safe_path(root)
    home = root.parent.parent.parent
    found, visiting = {}, set()

    def read(path):
        # Add-ons such as KAMP use symlinked include directories. Read their
        # canonical files only within the printer's home; never replace links.
        path = safe_path(path.resolve())
        if path in visiting:
            raise FixError('Cyclic configuration include')
        if path in found:
            raise FixError('Configuration included more than once: ' + str(path))
        if os.path.commonpath([str(path), str(home)]) != str(home):
            raise FixError('Configuration include outside printer home')
        visiting.add(path)
        text = read_config(path)
        found[path] = text
        for match in HEADERS.finditer(text):
            section = match.group(1).strip()
            if section.startswith('include '):
                pattern = section[8:].strip()
                matches = sorted(glob.glob(str(path.parent / pattern)))
                if not matches and not glob.has_magic(pattern):
                    raise FixError('Missing configuration include: ' + pattern)
                for included in matches:
                    read(Path(included))
        visiting.remove(path)

    read(root)
    return found


def section_names(files):
    names = []
    for text in files.values():
        names.extend(match.group(1).strip() for match in HEADERS.finditer(text)
                     if not match.group(1).strip().startswith('include '))
    if len(names) != len(set(names)):
        raise FixError('Duplicate configuration sections; resolve before applying fixes')
    return names


def add_section(text, section):
    # Klipper requires the SAVE_CONFIG block to remain the final block. Placing
    # ordinary config after it breaks startup on calibrated stock machines.
    marker = re.search(r'^#\*#.*SAVE_CONFIG.*$', text, re.M)
    position = marker.start() if marker else len(text)
    newline = '\r\n' if '\r\n' in text else '\n'
    addition = newline * 2 + '# hlab stock QIDI maintenance' + newline + '[' + section + ']' + newline * 2
    return text[:position] + addition + text[position:]


def polling(text):
    """Change only the reviewed Box motion-sensor sections, preserving other text."""
    headers = list(HEADERS.finditer(text))
    count = 0
    for index in reversed(range(len(headers))):
        header = headers[index]
        name = header.group(1).strip()
        if not re.match(r'^filament_motion_sensor box_motion_sensor(?:[1-4])?$', name):
            continue
        end = headers[index + 1].start() if index + 1 < len(headers) else len(text)
        body = text[header.end():end]
        if not re.search(r'^\s*switch_pin\s*[:=]\s*\^?mcu_box[1-4]:PB0\s*(?:[#;].*)?$', body, re.M):
            raise FixError('Unexpected Box motion pin: ' + name)
        body = re.sub(r'^\s*(?:use_irq|debounce_us)\s*[:=].*\n?', '', body, flags=re.M)
        text = text[:header.end()] + '\nuse_irq: False\n' + body.lstrip('\r\n') + text[end:]
        count += 1
    return text, count


def trim_runout_waits(text):
    """Recognize only the vendor loop inside PRINT_START, preserving other bytes."""
    headers = list(HEADERS.finditer(text))
    for index, header in enumerate(headers):
        if header.group(1).strip() != 'gcode_macro PRINT_START':
            continue
        end = headers[index + 1].start() if index + 1 < len(headers) else len(text)
        body = text[header.end():end]
        loop = re.compile(r'(?m)^(?P<indent>[ \t]*)\{% for i in range\(16\) %\}(?P<nl>\r?\n)'
                          r'(?P<save>[ \t]+SAVE_VARIABLE VARIABLE=runout_\{i\} VALUE=0\r?\n)'
                          r'(?P<dwell>[ \t]+G4 P100\r?\n)?'
                          r'(?P=indent)\{% endfor %\}')
        matches = list(loop.finditer(body))
        # A second/modified runout loop is not an invitation to guess.
        if len(matches) != 1 or len(re.findall(r'VARIABLE=runout_', body)) != 1:
            return text, 'skipped: unrecognized PRINT_START runout loop'
        match = matches[0]
        if match.group('dwell') is None:
            return text, 'unchanged'
        start, stop = match.span('dwell')
        return text[:header.end() + start] + text[header.end() + stop:], 'trimmed'
    return text, 'skipped: PRINT_START not found'


def fast_probing(text):
    """Change reviewed tilt/mesh speeds only; keep geometry and macro tuning."""
    headers = list(HEADERS.finditer(text))
    recognized, notes = [], []
    for index in reversed(range(len(headers))):
        header = headers[index]
        name = header.group(1).strip()
        if name not in ('bed_mesh', 'z_tilt'):
            continue
        end = headers[index + 1].start() if index + 1 < len(headers) else len(text)
        body = text[header.end():end]
        speeds = list(re.finditer(r'(?m)^(?P<prefix>[ \t]*speed[ \t]*[:=][ \t]*)(?P<value>[0-9]+(?:\.[0-9]+)?)(?P<tail>[ \t]*(?:[#;][^\r\n]*)?)[\r]?$' , body))
        if len(speeds) != 1 or float(speeds[0].group('value')) not in (150, 200, 300):
            notes.append('Probing speed skipped: unrecognized ' + name + ' speed')
            continue
        recognized.append(name)
        start, stop = speeds[0].span('value')
        text = text[:header.end() + start] + '300' + text[header.end() + stop:]
    return text, recognized, notes


class Fixes:
    def __init__(self, options, updater):
        self.options, self.updater = options, updater
        self.tx = None
        self.manifest = None
        self.stage = None
        self.mutating = False
        self.result = dict(schema=1, ok=False, status='failed', model='plus4', version='',
                           check_only=options['check_only'], polling=options['polling'],
                           trim_runout_waits=options.get('trim_runout_waits', False),
                           usb_event_log=options.get('usb_event_log', False),
                           fast_probing=options.get('fast_probing', False),
                           box_drying=options.get('box_drying', False),
                           fast_variable_saves=options.get('fast_variable_saves', False),
                           backup='', backup_sha256='', local_backup=options.get('local_backup', ''),
                           startup_verified=False, changes=[], notes=[], error='')

    def identity(self):
        release = self.options['release']
        if Path('/dev_info.txt').read_text().strip() != release['device_marker'] or os.uname().machine != release['architecture']:
            raise FixError('Expected the reviewed aarch64 QIDI Plus4 stock layout')
        version = self.updater.version()
        if version not in ('1.8.2', '2.1.1'):
            raise FixError('Fixes are reviewed only for stock Plus4 1.8.2 and 2.1.1')
        self.result['version'] = version
        return version

    def quiescent(self, allow_stopped=False):
        state = self.updater.service_state('klipper.service')
        if not (allow_stopped and state == 'inactive'):
            self.updater.idle()
        if self.updater.agent_active():
            status = self.updater.api('/server/extensions/request', {'agent': 'klippee', 'method': 'klippee_operation_status'})
            if status.get('running') is not False:
                raise FixError('Klippee must prove no configuration operation is running')

    def plr(self, apply=False, rollback=''):
        args = ['/home/mks/klippy-env/bin/python', '-B', str(self.stage / 'install.py'),
                '--printer-user', 'mks', '--transaction-id', 'hlab-' + self.options['transaction_id'], '--json']
        if rollback:
            args += ['--rollback-manifest', rollback]
        if apply:
            args += ['--apply', '--klipper-stopped']
        process = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=90)
        try:
            result = json.loads(process.stdout.decode('utf-8'))
        except (ValueError, UnicodeError):
            raise FixError('PLR installer returned invalid JSON')
        if process.returncode or result.get('ok') is not True or result.get('account') != 'mks':
            raise FixError('PLR installer refused the runtime: ' + str(result.get('error', 'unknown error')))
        return result

    @staticmethod
    def timer_active():
        process = subprocess.run(['systemctl', 'is-active', '--quiet', TIMER],
                                 stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
        if process.returncode not in (0, 3, 4):
            raise FixError('Could not inspect journal cleanup timer')
        return process.returncode == 0

    def plan(self):
        version = self.identity()
        self.quiescent()
        root = self.updater.config / 'printer.cfg'
        files = configs(root)
        names = section_names(files)
        # Ensure the existing stock recovery command will use the hardened wrapper.
        text = '\n'.join(files.values())
        shell = re.search(r'^\s*\[gcode_shell_command POWER_LOSS_RESUME\]([^\[]*)', text, re.M)
        if not shell or not re.search(r'^\s*command\s*[:=]\s*bash /home/mks/scripts/plr/plr.sh\s*$', shell.group(1), re.M):
            raise FixError('POWER_LOSS_RESUME must use the reviewed stock PLR wrapper')
        desired = {}
        sections = ['box_output_clock_guard']
        variable_sources = {}
        if self.options.get('fast_variable_saves'):
            if 'save_variables' not in names:
                raise FixError('Fast variable saves require [save_variables]')
            scope = {'__name__': 'hlab_save_policy'}
            exec(compile(base64.b64decode(self.options['assets']['qidi-fixes/save_variables_fast.py']), 'save_variables_fast.py', 'exec'), scope)
            extras = self.updater.home / 'klipper/klippy/extras'
            try:
                scope['validate_sources'](extras, version)
            except (ValueError, OSError) as exc:
                raise FixError(str(exc))
            for name in ('save_variables.py', 'box_extras.so'):
                path = safe_path(extras / name)
                if path.exists():
                    variable_sources[str(path)] = snapshot(path)
            sections.append('save_variables_fast')
        drying_metadata = None
        if self.options.get('box_drying'):
            if not any(re.fullmatch(r'heater_generic heater_box[1-4]', name) for name in names):
                raise FixError('Box drying requires an active QIDI Box heater')
            scope = {'__name__': 'hlab_box_drying_policy'}
            exec(compile(base64.b64decode(self.options['assets']['qidi-fixes/box_drying.py']), 'box_drying.py', 'exec'), scope)
            metadata = self.updater.config / 'officiall_filas_list.cfg'
            safe_path(metadata)
            try:
                scope['filament_limits'](str(metadata))
            except Exception:
                raise FixError('Box drying requires valid QIDI filament drying limits')
            drying_metadata = digest(metadata.read_bytes())
            sections.append('box_drying')
        if version == '1.8.2':
            sections.append('box_status_compat')
        else:
            self.result['notes'].append('2.1.1 status-shim need is unproven; existing shim retained, no new shim installed')
        for section in sections:
            target = self.updater.home / 'klipper/klippy/extras' / (section + '.py')
            desired[str(target)] = base64.b64decode(self.options['assets']['qidi-fixes/' + section + '.py'])
            if section not in names:
                files[root] = add_section(files[root], section)
        runout = None
        probe_sections = []
        if self.options.get('fast_probing'):
            for path in list(files):
                files[path], recognized, notes = fast_probing(files[path])
                probe_sections.extend(recognized)
                self.result['notes'].extend(notes)
            if not probe_sections:
                self.result['notes'].append('Probing speed skipped: no reviewed tilt/mesh sections')
        if self.options.get('trim_runout_waits'):
            for path in list(files):
                if 'gcode_macro PRINT_START' not in section_names({path: files[path]}):
                    continue
                files[path], state = trim_runout_waits(files[path])
                self.result['notes'].append('Runout waits: ' + state)
                if state in ('trimmed', 'unchanged'):
                    runout = str(path)
                break
            else:
                self.result['notes'].append('Runout waits skipped: PRINT_START not found')
        usb = bool(self.options.get('usb_event_log'))
        if usb:
            desired.update((path, content.encode('utf-8')) for path, content in USB_FILES.items())
            desired[USB_SCRIPT] = base64.b64decode(self.options['assets']['qidi-fixes/usb_events.py'])
            for unit in (USB_SOCKET, USB_SERVICE):
                if self.updater.enabled_state(unit) not in ('enabled', 'disabled', 'static', 'indirect', 'not-found', ''):
                    raise FixError('Unsupported USB logger unit policy: ' + unit)
        if self.options['polling']:
            total = 0
            for path in list(files):
                files[path], count = polling(files[path])
                total += count
            if not total:
                raise FixError('Polling requested but no supported active Box motion sensor was found')
            for number in range(1, 5):
                path = safe_path(self.updater.home / ('cfg_with_%d' % number))
                if path.exists():
                    changed, count = polling(read_config(path))
                    if not count:
                        raise FixError('Unsupported Box template: ' + str(path))
                    desired[str(path)] = changed.encode('utf-8')
        for path, content in files.items():
            desired[str(path)] = content.encode('utf-8')
        logfs = os.statvfs('/var/log')
        logs = Path('/var/log.hdd/journal').is_dir() and logfs.f_blocks * logfs.f_frsize <= 128 * 1024 * 1024
        if logs:
            desired.update((path, text.encode('utf-8')) for path, text in LOG_FILES.items())
        else:
            self.result['notes'].append('Log limits skipped: small Armbian ramlog/archive layout not detected')
        plr = self.plr()
        tracked = set(desired)
        tracked.update(item['path'] for item in plr['actions'] if item['role'] != 'scripts-directory-ownership')
        before = dict((path, snapshot(path)) for path in sorted(tracked))
        writes = dict((path, base64.b64encode(data).decode('ascii')) for path, data in desired.items()
                      if before[path] is None or before[path]['sha256'] != digest(data))
        changes = sorted(writes)
        changes.extend(item['path'] for item in plr['actions'] if item['action'] != 'unchanged')
        if logs and self.updater.enabled_state(TIMER) not in ('enabled', 'disabled', 'not-found', ''):
            raise FixError('Unsupported journal timer enablement; inspect its existing policy')
        activate_logs = logs and (self.updater.enabled_state(TIMER) != 'enabled' or not self.timer_active())
        if activate_logs:
            changes.append('enable ' + TIMER)
        if usb and (self.updater.enabled_state(USB_SOCKET) != 'enabled'
                    or any(self.updater.service_state(unit) != 'active' for unit in (USB_SOCKET, USB_SERVICE))):
            changes.append('activate ' + USB_SOCKET)
        objects = self.updater.api('/printer/objects/list').get('objects', [])
        if any(section not in objects for section in sections):
            changes.append('restart klipper.service to activate guard sections')
        self.result['changes'] = sorted(set(changes))
        return dict(version=version, before=before, writes=writes, plr_changed=plr['changed'],
                    plr_backup=plr.get('backup_manifest'), logs=logs, activate_logs=activate_logs,
                    usb=usb, runout=runout, probe_sections=sorted(probe_sections),
                    drying_metadata=drying_metadata, variable_sources=variable_sources, variable_box='box_extras' in names, changed=bool(changes), controllers=self.updater.idle())

    def save(self):
        atomic_write(self.tx / 'manifest.json', (json.dumps(self.manifest, sort_keys=True) + '\n').encode('utf-8'),
                     dict(uid=0, gid=0, mode=0o600))

    def prepare(self, plan):
        # Parents must be real directories; transaction handles are root-owned.
        for path in [TRANSACTIONS] + list(TRANSACTIONS.parents):
            if path.is_symlink():
                raise FixError('Unsafe transaction directory')
        TRANSACTIONS.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.tx = TRANSACTIONS / self.options['transaction_id']
        self.tx.mkdir(mode=0o700)
        self.manifest = dict(schema=1, status='prepared', plan=plan, after=None,
                             timer_enabled=self.updater.enabled_state(TIMER),
                             timer_active=self.timer_active(),
                             agent_active=self.updater.agent_active(), polling=self.options['polling'])
        self.manifest['trim_runout_waits'] = self.options.get('trim_runout_waits', False)
        self.manifest['usb_event_log'] = self.options.get('usb_event_log', False)
        self.manifest['fast_probing'] = self.options.get('fast_probing', False)
        self.manifest['box_drying'] = self.options.get('box_drying', False)
        self.manifest['fast_variable_saves'] = self.options.get('fast_variable_saves', False)
        if plan.get('usb'):
            self.manifest['usb_before'] = dict((unit, dict(enabled=self.updater.enabled_state(unit),
                                                          active=self.updater.service_state(unit) == 'active'))
                                               for unit in (USB_SOCKET, USB_SERVICE))
        self.save()
        with tarfile.open(str(self.tx / 'recovery.tar.gz'), 'w:gz') as archive:
            archive.add(str(self.tx / 'manifest.json'), arcname='manifest.json')
            for path, metadata in plan['before'].items():
                if metadata is not None:
                    archive.add(path, arcname='files/' + path.lstrip('/'), recursive=False)
        self.manifest['archive_sha256'] = digest((self.tx / 'recovery.tar.gz').read_bytes())
        self.recovery_files(plan)
        self.save()
        self.result.update(status='prepared', ok=True, backup=str(self.tx),
                           backup_sha256=self.manifest['archive_sha256'])

    def recovery_files(self, plan):
        data = {}
        with tarfile.open(str(self.tx / 'recovery.tar.gz'), 'r:gz') as archive:
            for path, metadata in plan['before'].items():
                if metadata is None:
                    continue
                member = archive.getmember('files/' + path.lstrip('/'))
                if not member.isfile():
                    raise FixError('Recovery archive contains a nonregular member')
                data[path] = archive.extractfile(member).read()
                if digest(data[path]) != metadata['sha256']:
                    raise FixError('Recovery member digest mismatch')
        return data

    def load(self, path):
        if not re.match(r'^/var/lib/hlab/qidi-fixes/[0-9a-f]{32}$', path):
            raise FixError('Invalid fixes transaction handle')
        self.tx = Path(path)
        for parent in [self.tx] + list(self.tx.parents):
            if parent.is_symlink() or parent.stat().st_uid != 0 or parent.stat().st_mode & 0o022:
                raise FixError('Untrusted fixes transaction directory')
        self.manifest = json.loads(safe_path(self.tx / 'manifest.json').read_text())
        self.result.update(backup=str(self.tx), backup_sha256=digest(safe_path(self.tx / 'recovery.tar.gz').read_bytes()))
        if self.result['backup_sha256'] != self.manifest.get('archive_sha256'):
            raise FixError('Recovery archive no longer matches the retained transaction')
        return self.manifest['plan']

    def stop(self):
        if self.updater.agent_active():
            self.updater.user_systemctl('stop', 'klippee-moonraker.service')
        self.quiescent(allow_stopped=True)
        self.updater_command(['systemctl', 'stop', 'klipper.service'])
        if self.updater.service_state('klipper.service') != 'inactive':
            raise FixError('Could not prove Klipper stopped')

    @staticmethod
    def updater_command(args):
        completed = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
        if completed.returncode:
            raise FixError(args[0] + ' failed; inspect the retained transaction')

    def verify(self, plan, guards=True):
        deadline = time.monotonic() + self.options['wait_seconds']
        while time.monotonic() < deadline:
            try:
                self.updater.idle()
                names = list(plan['controllers']) + (['box_output_clock_guard'] if guards else [])
                status = self.updater.api('/printer/objects/query?' + '&'.join(
                    urllib.parse.quote(name, safe='') for name in names))['status']
                for name in plan['controllers']:
                    if not status.get(name, {}).get('last_received_sample' if name == 'beacon' else 'mcu_version'):
                        raise FixError('Controller did not return: ' + name)
                if guards:
                    if plan.get('variable_sources'):
                        saved = self.updater.api('/printer/objects/query?save_variables_fast')['status'].get('save_variables_fast', {})
                        expected_box = 'active' if plan.get('variable_box') else 'absent'
                        if saved.get('active') is not True or saved.get('box_hook') != expected_box or saved.get('errors') != 0:
                            raise FixError('Fast variable-save hooks did not activate')
                    if plan.get('probe_sections'):
                        settings = self.updater.api('/printer/objects/query?configfile')['status']['configfile']['settings']
                        if any(float(settings.get(name, {}).get('speed', 0)) != 300 for name in plan['probe_sections']):
                            raise FixError('Loaded tilt/mesh speed is not 300 mm/s')
                    if plan.get('drying_metadata'):
                        drying = self.updater.api('/printer/objects/query?box_drying')['status'].get('box_drying', {})
                        if drying.get('active_boxes') != [] or drying.get('boxes') != {}:
                            raise FixError('Box drying did not load in the inactive state')
                    if plan.get('runout'):
                        loaded = self.updater.api('/printer/objects/query?configfile')['status']['configfile']['config']
                        macro = loaded.get('gcode_macro PRINT_START', {}).get('gcode', '')
                        # Klipper's configfile response strips macro-line indentation.
                        # Restore config-value indentation only for this read-only check.
                        body = '\n'.join('    ' + line for line in macro.splitlines())
                        _, state = trim_runout_waits('[gcode_macro PRINT_START]\ngcode:\n' + body + '\n')
                        if state != 'unchanged':
                            raise FixError('Loaded PRINT_START did not prove the trimmed runout loop')
                    if plan.get('usb') and any(self.updater.service_state(unit) != 'active' for unit in (USB_SOCKET, USB_SERVICE)):
                        raise FixError('USB evidence services did not activate')
                    guard = status.get('box_output_clock_guard', {})
                    if any(name.startswith('mcu mcu_box') for name in plan['controllers']):
                        outputs = guard.get('outputs', {})
                        if guard.get('state') not in ('corrected', 'already-correct') or guard.get('reload_all_guarded') is not True or set(outputs) != {'b_output', 'e_output'}:
                            raise FixError('Box output guard did not activate')
                        if any(item.get('state') not in ('corrected', 'already-correct') or not item.get('pin_mcu', '').startswith('mcu_box') for item in outputs.values()):
                            raise FixError('Box output ownership could not be verified')
                    elif guard.get('state') != 'inactive-no-box':
                        raise FixError('Unexpected guard state on a printer without a Box')
                    if self.result['version'] == '1.8.2':
                        shim = self.updater.api('/printer/objects/query?box_status_compat')['status'].get('box_status_compat', {})
                        allowed = ('corrected', 'already-provided') if any(name.startswith('mcu mcu_box') for name in plan['controllers']) else ('inactive-no-box',)
                        if shim.get('state') not in allowed:
                            raise FixError('Box status shim did not activate')
                    if self.options['polling']:
                        settings = self.updater.api('/printer/objects/query?configfile')['status']['configfile']['settings']
                        sensors = [value for name, value in settings.items()
                                   if re.match(r'^filament_motion_sensor box_motion_sensor(?:[1-4])?$', name)]
                        if not sensors or any(sensor.get('use_irq') is not False for sensor in sensors):
                            raise FixError('Active Box motion polling could not be verified')
                self.result['startup_verified'] = True
                return
            except Exception:
                time.sleep(1)
        raise FixError('Startup verification timed out; Klipper will be stopped; use the retained rollback handle')

    def activate_logs(self, plan):
        if plan['logs']:
            self.updater_command(['systemctl', 'daemon-reload'])
            self.updater_command(['systemctl', 'restart', 'systemd-journald'])
            self.updater_command(['systemctl', 'enable', '--now', TIMER])
            if self.updater.service_state(TIMER) != 'active' or self.updater.service_state('systemd-journald') != 'active':
                raise FixError('Journal services did not activate')
        if plan.get('usb'):
            self.updater_command(['systemctl', 'daemon-reload'])
            self.updater_command(['udevadm', 'control', '--reload-rules'])
            self.updater_command(['systemctl', 'enable', '--now', USB_SOCKET])
            self.updater_command(['systemctl', 'start', USB_SERVICE])

    def apply(self):
        plan = self.load(self.options['backup'])
        if self.manifest['status'] != 'prepared' or self.manifest['polling'] != self.options['polling'] or self.result['backup_sha256'] != self.options['backup_sha256']:
            raise FixError('Apply does not match the desktop-verified recovery transaction')
        if any(self.manifest.get(key, False) != self.options.get(key, False) for key in ('trim_runout_waits', 'usb_event_log', 'fast_probing', 'box_drying', 'fast_variable_saves')):
            raise FixError('Apply options differ from prepared maintenance transaction')
        if self.plan() != plan:
            raise FixError('Printer files or state changed after preparation; start a new check')
        self.manifest['status'] = 'applying'
        self.save()
        self.mutating = True
        self.stop()
        if dict((path, snapshot(path)) for path in plan['before']) != plan['before']:
            raise FixError('Configuration changed while stopping; rollback required')
        if any(snapshot(path) != metadata for path, metadata in plan.get('variable_sources', {}).items()):
            raise FixError('Saved-variable source changed while stopping; rollback required')
        if plan.get('usb'):
            for unit in (USB_SOCKET, USB_SERVICE):
                if self.updater.service_state(unit) in ('active', 'activating', 'reloading', 'deactivating'):
                    self.updater_command(['systemctl', 'stop', unit])
        if plan['plr_changed']:
            applied = self.plr(apply=True)
            if applied.get('backup_manifest') != plan['plr_backup'] or self.plr()['changed']:
                raise FixError('PLR post-install verification failed')
        owner = os.stat(str(self.updater.home / 'klipper'))
        for path, content in plan['writes'].items():
            metadata = plan['before'][path]
            if metadata is None:
                metadata = dict(uid=owner.st_uid if path.startswith('/home/mks/') else 0,
                                gid=owner.st_gid if path.startswith('/home/mks/') else 0, mode=0o644)
            atomic_write(path, base64.b64decode(content), metadata)
            if snapshot(path)['sha256'] != digest(base64.b64decode(content)):
                raise FixError('Installed fix digest mismatch: ' + path)
        self.manifest['after'] = dict((path, snapshot(path)) for path in plan['before'])
        self.save()
        self.activate_logs(plan)
        self.updater_command(['systemctl', 'start', 'klipper.service'])
        self.verify(plan)
        if self.manifest['agent_active']:
            self.updater.user_systemctl('start', 'klippee-moonraker.service')
            if not self.updater.agent_active():
                raise FixError('Klippee agent did not restart')
        self.manifest['status'] = 'applied'
        self.save()
        self.result.update(ok=True, status='applied')

    def rollback(self):
        plan = self.load(self.options['rollback'])
        if self.manifest['status'] not in ('applied', 'applying', 'recovery_required'):
            raise FixError('Transaction is not eligible for rollback')
        if self.identity() != plan['version']:
            raise FixError('Firmware changed since this fixes transaction; rollback is not compatible')
        self.quiescent(allow_stopped=True)
        # Do not overwrite subsequent user changes following a successful install.
        if self.manifest['status'] == 'applied' and self.manifest['after'] != dict((path, snapshot(path)) for path in plan['before']):
            raise FixError('Files changed after installation; automatic rollback would overwrite user changes')
        recovered = self.recovery_files(plan)
        self.mutating = True
        self.stop()
        self.manifest['status'] = 'rolling_back'
        self.save()
        if plan['plr_changed'] and plan['plr_backup'] and Path(plan['plr_backup']).is_file():
            self.plr(apply=True, rollback=plan['plr_backup'])
        if plan['logs']:
            # Disable while the installed unit still exists on disk.
            self.updater_command(['systemctl', 'disable', '--now', TIMER])
        if plan.get('usb'):
            if self.updater.enabled_state(USB_SOCKET) == 'enabled':
                self.updater_command(['systemctl', 'disable', '--now', USB_SOCKET])
            for unit in (USB_SOCKET, USB_SERVICE):
                if self.updater.service_state(unit) in ('active', 'activating', 'reloading', 'deactivating'):
                    self.updater_command(['systemctl', 'stop', unit])
        for path, metadata in plan['before'].items():
            target = safe_path(path)
            if metadata is None:
                if target.exists():
                    target.unlink()
            else:
                atomic_write(path, recovered[path], metadata)
        if plan['logs']:
            self.updater_command(['systemctl', 'daemon-reload'])
            self.updater_command(['systemctl', 'restart', 'systemd-journald'])
            if self.manifest['timer_enabled'] == 'enabled':
                self.updater_command(['systemctl', 'enable', TIMER])
            if self.manifest['timer_active']:
                self.updater_command(['systemctl', 'start', TIMER])
        if plan.get('usb'):
            self.updater_command(['systemctl', 'daemon-reload'])
            self.updater_command(['udevadm', 'control', '--reload-rules'])
            for unit, state in self.manifest['usb_before'].items():
                if state['enabled'] == 'enabled':
                    self.updater_command(['systemctl', 'enable', unit])
                if state['active']:
                    self.updater_command(['systemctl', 'start', unit])
        self.updater_command(['systemctl', 'start', 'klipper.service'])
        self.verify(plan, guards=False)
        if self.manifest['agent_active']:
            self.updater.user_systemctl('start', 'klippee-moonraker.service')
        self.manifest['status'] = 'rolled_back'
        self.save()
        self.result.update(ok=True, status='rolled_back')

    def run(self):
        with tempfile.TemporaryDirectory(prefix='hlab-qidi-fixes-') as stage:
            self.stage = Path(stage)
            for name, content in self.options['assets'].items():
                if name.startswith('plr-hardening/'):
                    (self.stage / Path(name).name).write_bytes(base64.b64decode(content))
            if self.options.get('rollback'):
                self.rollback()
            elif self.options['phase'] == 'apply':
                self.apply()
            else:
                plan = self.plan()
                if self.options['check_only']:
                    self.result.update(ok=True, status='checked')
                elif not plan['changed']:
                    self.verify(plan)
                    self.result.update(ok=True, status='unchanged')
                else:
                    self.prepare(plan)


def main(options):
    os.umask(0o077)
    shared = {'__name__': 'hlab_qidi_update'}
    exec(compile(base64.b64decode(options['assets']['tools/migration/update-qidi.py']), 'update-qidi.py', 'exec'), shared)
    updater = shared['QIDIUpdate'](dict(options, host_only=True, config_policy='preserve', local_backup=''))
    fixes = Fixes(options, updater)
    lock = None
    try:
        if os.geteuid() != 0:
            raise FixError('Fixes require root via sudo')
        # Same lock as firmware upgrades: these operations must never overlap.
        lock = open('/run/lock/hlab-qidi-firmware.lock', 'a')
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        fixes.run()
    except Exception as exc:
        fixes.result['error'] = str(exc) if isinstance(exc, (FixError, shared['UpdateError'])) else 'Maintenance failed (%s); inspect the retained transaction' % type(exc).__name__
        if fixes.manifest is not None and fixes.mutating:
            fixes.manifest['status'] = 'recovery_required'
            try:
                fixes.save()
            except Exception:
                fixes.result['error'] += '; could not persist the recovery journal'
            try:
                fixes.updater_command(['systemctl', 'stop', 'klipper.service'])
            except Exception:
                fixes.result['error'] += '; could not verify Klipper stopped'
    finally:
        if lock is not None:
            lock.close()
    print(json.dumps(fixes.result))
    return 0 if fixes.result['ok'] else 1


if __name__ == '__main__':
    raise SystemExit(main(globals()['PLAB_OPTIONS']))
