"""Offline maintenance fixtures; no SSH, service restart, heat or movement."""
import ast
import base64
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

HELPER = Path(__file__).resolve().parents[1] / 'apply-qidi-fixes.py'
spec = importlib.util.spec_from_file_location('qidi_fixes', HELPER)
fixes = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixes)
SENSOR = '''[filament_motion_sensor box_motion_sensor]
switch_pin: ^mcu_box1:PB0
use_irq: True
debounce_us: 50
pause_on_runout: True
'''


class FixesTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.home = self.root / 'mks'
        self.config = self.home / 'printer_data/config'
        self.config.mkdir(parents=True)
        (self.home / 'klipper/klippy/extras').mkdir(parents=True)
        self.printer = self.config / 'printer.cfg'
        self.printer.write_text('[include macros.cfg]\n[include box.cfg]\n[extruder]\nmax_temp: 370\n')
        (self.config / 'box.cfg').write_text(SENSOR)
        (self.config / 'macros.cfg').write_text('''[gcode_shell_command POWER_LOSS_RESUME]
command: bash /home/mks/scripts/plr/plr.sh
timeout: 120
''')
        self.updater = Mock(home=self.home, config=self.config)
        self.updater.version.return_value = '2.1.1'
        self.updater.idle.return_value = ['mcu', 'mcu mcu_box1']
        self.updater.agent_active.return_value = False
        self.updater.service_state.return_value = 'active'
        self.updater.enabled_state.return_value = 'enabled'
        self.updater.api.return_value = dict(objects=['box_output_clock_guard', 'box_status_compat'])
        asset_root = HELPER.parents[2]
        assets = dict((name, base64.b64encode((asset_root / name).read_bytes()).decode('ascii'))
                      for name in ('qidi-fixes/box_output_clock_guard.py', 'qidi-fixes/box_status_compat.py'))
        self.options = dict(check_only=False, polling=False, assets=assets, transaction_id='a' * 32,
                            wait_seconds=5, phase='prepare')
        self.fix = fixes.Fixes(self.options, self.updater)
        self.fix.identity = Mock(side_effect=self.identity)
        self.fix.plr = Mock(return_value=dict(changed=False, actions=[], backup_manifest=None))
        self.fix.timer_active = Mock(return_value=True)
        # Keep log-layout detection independent of the test host.
        logfs = Mock(f_blocks=1024 * 1024, f_frsize=4096)
        self.log_patch = patch.object(fixes.os, 'statvfs', return_value=logfs)
        self.log_patch.start()
        self.addCleanup(self.log_patch.stop)

    def identity(self):
        self.fix.result['version'] = self.updater.version()
        return self.fix.result['version']

    def test_version_scopes_status_shim_without_removing_existing_shim(self):
        for version in ('1.8.2', '2.1.1'):
            self.updater.version.return_value = version
            plan = self.fix.plan()
            writes = plan['writes']
            guard = str(self.home / 'klipper/klippy/extras/box_output_clock_guard.py')
            shim = str(self.home / 'klipper/klippy/extras/box_status_compat.py')
            self.assertIn(guard, writes)
            self.assertEqual(shim in writes, version == '1.8.2')
            self.assertEqual('[box_status_compat]' in base64.b64decode(writes[str(self.printer)]).decode(), version == '1.8.2')
        original = self.printer.read_text() + '\n[box_status_compat]\n'
        self.printer.write_text(original)
        plan = self.fix.plan()
        self.assertIn('[box_status_compat]', base64.b64decode(plan['writes'][str(self.printer)]).decode())
        self.assertNotIn(shim, plan['writes'])

    def test_polling_is_optional_and_covers_active_config_and_all_templates(self):
        for count in range(1, 5):
            (self.home / ('cfg_with_%d' % count)).write_text(SENSOR)
        plain = self.fix.plan()
        self.assertNotIn(str(self.config / 'box.cfg'), plain['writes'])
        self.fix.options['polling'] = True
        plan = self.fix.plan()
        for path in [self.config / 'box.cfg'] + [self.home / ('cfg_with_%d' % count) for count in range(1, 5)]:
            data = base64.b64decode(plan['writes'][str(path)]).decode()
            self.assertIn('use_irq: False', data)
            self.assertNotIn('debounce_us', data)
            self.assertIn('pause_on_runout: True', data)
            self.assertEqual(fixes.polling(data)[0], data)

    def test_polling_refuses_unknown_pins_and_missing_active_sensors(self):
        with self.assertRaises(fixes.FixError):
            fixes.polling(SENSOR.replace('PB0', 'PA1'))
        self.fix.options['polling'] = True
        (self.config / 'box.cfg').write_text('[mcu mcu_box1]\nserial: /dev/fixture\n')
        with self.assertRaisesRegex(fixes.FixError, 'no supported active'):
            self.fix.plan()

    def test_duplicate_sections_and_include_cycles_fail_before_plr(self):
        (self.config / 'box.cfg').write_text('[extruder]\nmax_temp: 300\n')
        with self.assertRaisesRegex(fixes.FixError, 'Duplicate'):
            self.fix.plan()
        self.fix.plr.assert_not_called()
        (self.config / 'box.cfg').write_text('[include printer.cfg]\n')
        with self.assertRaisesRegex(fixes.FixError, 'Cyclic'):
            fixes.configs(self.printer)

    def test_addon_symlink_is_read_canonically_but_outside_home_is_refused(self):
        addon = self.home / 'KAMP'
        addon.mkdir()
        (addon / 'settings.cfg').write_text('[gcode_macro FIXTURE]\ngcode: M400\n')
        (self.config / 'KAMP').symlink_to(addon, target_is_directory=True)
        self.printer.write_text(self.printer.read_text() + '\n[include KAMP/settings.cfg]\n')
        self.assertIn(addon / 'settings.cfg', fixes.configs(self.printer))
        outside = self.root / 'outside.cfg'
        outside.write_text('[gcode_macro OUTSIDE]\ngcode: M400\n')
        (self.config / 'outside.cfg').symlink_to(outside)
        self.printer.write_text(self.printer.read_text() + '\n[include outside.cfg]\n')
        with self.assertRaisesRegex(fixes.FixError, 'outside printer home'):
            fixes.configs(self.printer)

    def test_unknown_plr_command_and_busy_agent_fail_without_writes(self):
        original = self.printer.read_bytes()
        self.updater.agent_active.return_value = True
        self.updater.api.return_value = dict(running=True)
        with self.assertRaisesRegex(fixes.FixError, 'no configuration operation'):
            self.fix.plan()
        self.updater.agent_active.return_value = False
        (self.config / 'macros.cfg').write_text('[gcode_shell_command POWER_LOSS_RESUME]\ncommand: custom\n')
        with self.assertRaisesRegex(fixes.FixError, 'reviewed stock PLR wrapper'):
            self.fix.plan()
        self.assertEqual(self.printer.read_bytes(), original)

    def write_as_test_user(self, path, data, metadata):
        metadata = dict(metadata, uid=os.getuid(), gid=os.getgid())
        self.real_write(path, data, metadata)

    def prepare(self):
        self.real_write = fixes.atomic_write
        writer = patch.object(fixes, 'atomic_write', side_effect=self.write_as_test_user)
        writer.start()
        self.addCleanup(writer.stop)
        transactions = patch.object(fixes, 'TRANSACTIONS', self.root / 'transactions')
        transactions.start()
        self.addCleanup(transactions.stop)
        plan = self.fix.plan()
        self.fix.prepare(plan)
        self.fix.result['ok'], self.fix.result['status'] = False, 'failed'
        self.fix.options['backup'] = str(self.fix.tx)
        self.fix.options['backup_sha256'] = self.fix.result['backup_sha256']
        # Production load accepts only root-owned /var/lib transactions. The
        # fixture uses an isolated unprivileged directory and the same plan.
        self.fix.load = Mock(return_value=plan)
        self.fix.stop = Mock()
        self.fix.updater_command = Mock()
        self.fix.verify = Mock(side_effect=lambda *args, **kwargs: self.fix.result.update(startup_verified=True))
        return plan

    def test_prepare_does_not_edit_live_files(self):
        original = self.printer.read_bytes()
        self.prepare()
        self.assertEqual(self.printer.read_bytes(), original)
        self.assertTrue((self.fix.tx / 'recovery.tar.gz').is_file())
        self.fix.stop.assert_not_called()

    def test_read_only_includes_preserve_crlf_bytes(self):
        path = self.config / 'macros.cfg'
        data = path.read_bytes().replace(b'\n', b'\r\n')
        path.write_bytes(data)
        self.assertNotIn(str(path), self.fix.plan()['writes'])
        self.assertEqual(path.read_bytes(), data)

    def test_guard_is_inserted_before_klipper_save_config_block(self):
        saved = '#*# <---------------------- SAVE_CONFIG ---------------------->\n#*# DO NOT EDIT THIS BLOCK OR BELOW.\n#*# [probe]\n#*# z_offset = 1.3\n'
        self.printer.write_text(self.printer.read_text() + '\n' + saved)
        plan = self.fix.plan()
        edited = base64.b64decode(plan['writes'][str(self.printer)]).decode()
        self.assertLess(edited.index('[box_output_clock_guard]'), edited.index('SAVE_CONFIG'))
        self.assertTrue(edited.endswith(saved))

    def test_existing_files_with_inactive_sections_plan_activation(self):
        plan = self.fix.plan()
        for path, content in plan['writes'].items():
            Path(path).write_bytes(base64.b64decode(content))
        self.assertFalse(self.fix.plan()['changed'])
        self.updater.api.return_value = dict(objects=[])
        self.fix.plan()
        self.assertIn('restart klipper.service to activate guard sections', self.fix.result['changes'])

    def test_apply_then_rollback_restores_bytes_and_removes_new_files(self):
        original = self.printer.read_bytes()
        plan = self.prepare()
        self.fix.apply()
        self.assertEqual(self.fix.result['status'], 'applied')
        self.assertIn('[box_output_clock_guard]', self.printer.read_text())
        self.assertTrue((self.home / 'klipper/klippy/extras/box_output_clock_guard.py').is_file())
        self.assertFalse(self.fix.plan()['changed'])
        self.fix.options['rollback'] = str(self.fix.tx)
        self.fix.rollback()
        self.assertEqual(self.fix.result['status'], 'rolled_back')
        self.assertEqual(self.printer.read_bytes(), original)
        self.assertFalse((self.home / 'klipper/klippy/extras/box_output_clock_guard.py').exists())
        self.assertEqual(dict((path, fixes.snapshot(path)) for path in plan['before']), plan['before'])

    def test_changed_config_after_prepare_blocks_apply_before_stop(self):
        self.prepare()
        self.printer.write_text(self.printer.read_text() + '\n# concurrent edit\n')
        with self.assertRaisesRegex(fixes.FixError, 'changed after preparation'):
            self.fix.apply()
        self.fix.stop.assert_not_called()

    def test_user_edits_after_install_block_rollback(self):
        self.prepare()
        self.fix.apply()
        self.fix.options['rollback'] = str(self.fix.tx)
        self.printer.write_text(self.printer.read_text() + '\n# keep my changes\n')
        self.fix.stop.reset_mock()
        with self.assertRaisesRegex(fixes.FixError, 'overwrite user changes'):
            self.fix.rollback()
        self.fix.stop.assert_not_called()

    def test_failed_startup_never_reports_applied(self):
        self.prepare()
        self.fix.verify.side_effect = fixes.FixError('startup failed')
        with self.assertRaisesRegex(fixes.FixError, 'startup failed'):
            self.fix.apply()
        self.assertEqual(self.fix.manifest['status'], 'applying')
        self.assertFalse(self.fix.result['ok'])

    def test_backup_hash_mismatch_blocks_apply(self):
        self.prepare()
        self.fix.options['backup_sha256'] = '0' * 64
        with self.assertRaisesRegex(fixes.FixError, 'desktop-verified'):
            self.fix.apply()
        self.fix.stop.assert_not_called()

    def test_corrupt_recovery_blocks_rollback_before_stop(self):
        self.prepare()
        self.fix.apply()
        self.fix.options['rollback'] = str(self.fix.tx)
        (self.fix.tx / 'recovery.tar.gz').write_bytes(b'corrupt')
        self.fix.stop.reset_mock()
        with self.assertRaises(Exception):
            self.fix.rollback()
        self.fix.stop.assert_not_called()

    def test_firmware_change_blocks_rollback(self):
        self.prepare()
        self.fix.apply()
        self.fix.options['rollback'] = str(self.fix.tx)
        self.updater.version.return_value = '1.8.2'
        self.fix.stop.reset_mock()
        with self.assertRaisesRegex(fixes.FixError, 'Firmware changed'):
            self.fix.rollback()
        self.fix.stop.assert_not_called()

    def test_log_timer_is_disabled_before_restoring_unit_files(self):
        plan = self.prepare()
        plan['logs'] = True
        self.fix.plan = Mock(return_value=plan)
        self.fix.manifest['timer_enabled'] = 'disabled'
        self.fix.manifest['timer_active'] = False
        self.fix.apply()
        self.fix.options['rollback'] = str(self.fix.tx)
        events = []
        self.fix.updater_command.side_effect = lambda command: events.append(command)
        with patch.object(fixes, 'atomic_write', side_effect=lambda *args: events.append(['restore', str(args[0])])):
            self.fix.rollback()
        disable = events.index(['systemctl', 'disable', '--now', fixes.TIMER])
        restore = next(index for index, event in enumerate(events) if event[0] == 'restore' and event[1] == str(self.printer))
        self.assertLess(disable, restore)
        self.assertNotIn(['systemctl', 'enable', fixes.TIMER], events)

    def test_optional_missing_timer_is_inactive_but_systemctl_failure_is_not_ignored(self):
        for code in (0, 3, 4):
            with patch.object(fixes.subprocess, 'run', return_value=Mock(returncode=code)):
                self.assertEqual(fixes.Fixes.timer_active(), code == 0)
        with patch.object(fixes.subprocess, 'run', return_value=Mock(returncode=1)):
            with self.assertRaises(fixes.FixError):
                fixes.Fixes.timer_active()

    def test_entrypoint_stops_klipper_and_returns_failure_after_partial_apply(self):
        options = dict(self.options,
                       release=json.loads((HELPER.parent / 'qidi-releases.json').read_text())[0],
                       moonraker_port=7125, api_key='fixture-secret')
        options['assets']['tools/migration/update-qidi.py'] = base64.b64encode((HELPER.parent / 'update-qidi.py').read_bytes()).decode('ascii')
        real_open = open

        def isolated_open(path, *args, **kwargs):
            if path == '/run/lock/hlab-qidi-firmware.lock':
                path = str(self.root / 'firmware.lock')
            return real_open(path, *args, **kwargs)

        def fail_during_apply(instance):
            instance.manifest = dict(status='applying')
            instance.mutating = True
            raise fixes.FixError('fixture startup failure')

        stdout = io.StringIO()
        with patch('builtins.open', side_effect=isolated_open), patch.object(fixes.os, 'geteuid', return_value=0), \
                patch.object(fixes.Fixes, 'run', fail_during_apply), patch.object(fixes.Fixes, 'save'), \
                patch.object(fixes.Fixes, 'updater_command') as command, contextlib.redirect_stdout(stdout):
            code = fixes.main(options)
        receipt = json.loads(stdout.getvalue())
        self.assertEqual(code, 1)
        self.assertFalse(receipt['ok'])
        self.assertFalse(receipt['startup_verified'])
        self.assertEqual(receipt['status'], 'failed')
        self.assertIn('fixture startup failure', receipt['error'])
        self.assertNotIn('fixture-secret', stdout.getvalue())
        command.assert_called_once_with(['systemctl', 'stop', 'klipper.service'])

    def test_source_supports_python37_and_no_embedded_machine_identity(self):
        for path in [HELPER] + list((HELPER.parents[2] / 'qidi-fixes').glob('*.py')):
            ast.parse(path.read_text(), feature_version=(3, 7))
            for secret in ('qidi1.lan', 'id_plab_qidi1', 'C:/Users/tim'):
                self.assertNotIn(secret, path.read_text())


if __name__ == '__main__':
    unittest.main()
