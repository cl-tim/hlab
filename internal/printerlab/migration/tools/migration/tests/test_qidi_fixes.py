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
START = '''[gcode_macro PRINT_START]
gcode:
    TOOL_CHANGE_END
    {% if printer.save_variables.variables.box_count >= 1 %}
        {% for i in range(16) %}
            SAVE_VARIABLE VARIABLE=runout_{i} VALUE=0
            G4 P100
        {% endfor %}
    {% endif %}
    G4 P5000

[gcode_macro KEEP]
gcode: G4 P100
'''


class FixesTests(unittest.TestCase):
    def test_runout_edit_is_narrow_idempotent_and_preserves_line_endings(self):
        for newline in ('\n', '\r\n'):
            source = START.replace('\n', newline)
            trimmed, state = fixes.trim_runout_waits(source)
            self.assertEqual(state, 'trimmed')
            self.assertEqual(trimmed, source.replace('            G4 P100' + newline, '', 1))
            self.assertEqual(fixes.trim_runout_waits(trimmed), (trimmed, 'unchanged'))
        for source in (START.replace('range(16)', 'range(8)'), START.replace('VALUE=0', 'VALUE=1'),
                       START.replace('G4 P100\n        {% endfor', 'G4 P200\n        {% endfor'),
                       START + START, START.replace('PRINT_START', 'OTHER_START')):
            # Duplicated sections are rejected by the include-graph gate, not guessed here.
            if source == START + START:
                with self.assertRaises(fixes.FixError):
                    fixes.section_names({Path('fixture'): source})
                continue
            self.assertEqual(fixes.trim_runout_waits(source)[0], source)

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
                      for name in ('qidi-fixes/box_output_clock_guard.py', 'qidi-fixes/box_status_compat.py', 'qidi-fixes/box_drying.py', 'qidi-fixes/save_variables_fast.py', 'qidi-fixes/log_limits.py'))
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

    def legacy_configuration(self):
        self.updater.version.return_value = '1.6.0'
        self.updater.idle.return_value = ['mcu']
        fixtures = HELPER.parent / 'tests/fixtures'
        self.printer.write_bytes((fixtures / 'qidi_1_6_0_printer.cfg').read_bytes())
        (self.config / 'gcode_macro.cfg').write_bytes((fixtures / 'qidi_1_6_0_gcode_macro.cfg').read_bytes())
        # Base-image includes are absent from the update archive. Their bytes
        # must be retained; 1.6.0 maintenance does not claim to harden PLR.
        for name in ('plr.cfg', 'time_update.cfg', 'timelapse.cfg', 'KAMP_Settings.cfg'):
            (self.config / name).write_text('# synthetic base-image include\n')
        (self.home / 'klipper/klippy/extras/save_variables.py').write_bytes(
            (fixtures / 'qidi_1_6_0_save_variables.py').read_bytes())
        self.updater.api.return_value = dict(objects=['save_variables_fast'])

    def test_legacy_transaction_is_idempotent_and_rolls_back_without_box_or_plr_writes(self):
        self.legacy_configuration()
        before = {path: path.read_bytes() for path in self.config.glob('*.cfg')}
        saved = self.config / 'saved_variables.cfg'
        saved.write_bytes(b'[Variables]\nlegacy_value = 17\n')
        self.fix.options.update(fast_probing=True, fast_variable_saves=True,
                                usb_event_log=True, trim_runout_waits=True)
        self.fix.options['assets']['qidi-fixes/usb_events.py'] = base64.b64encode(b'# fixture writer\n').decode()
        usb_files = {str(self.root / Path(path).name): content for path, content in fixes.USB_FILES.items()}
        script = str(self.root / 'usb_events.py')
        with patch.object(fixes, 'USB_FILES', usb_files), patch.object(fixes, 'USB_SCRIPT', script):
            plan = self.prepare()
            self.assertFalse(plan['plr_changed'])
            self.assertEqual(plan['probe_sections'], ['bed_mesh', 'z_tilt'])
            self.assertIsNone(plan['runout'])
            self.fix.plr.assert_not_called()
            self.fix.apply()
            self.assertIn('[save_variables_fast]', self.printer.read_text())
            self.assertNotIn('box_output_clock_guard', self.printer.read_text())
            self.assertFalse(self.fix.plan()['changed'])
            self.assertEqual(saved.read_bytes(), b'[Variables]\nlegacy_value = 17\n')
            self.fix.options['rollback'] = str(self.fix.tx)
            self.fix.rollback()
            self.fix.plr.assert_not_called()
            for path, content in before.items():
                self.assertEqual(path.read_bytes(), content)
            self.assertFalse((self.home / 'klipper/klippy/extras/save_variables_fast.py').exists())
            self.assertFalse(Path(script).exists())

    def test_legacy_rejects_unreviewed_duplicates_and_box_additions_before_prepare(self):
        self.legacy_configuration()
        original = self.printer.read_text()
        for text in (original.replace('accel_per_hz: 150', 'accel_per_hz: 151'),
                     original + '\n[resonance_tester]\naccel_chip:adxl345\n',
                     original + '\n[extruder]\nmax_temp:300\n'):
            self.printer.write_text(text)
            with self.assertRaisesRegex(fixes.FixError, 'Duplicate'):
                self.fix.plan()
        self.printer.write_text(original)
        # The stock exception is version-bound and cannot be split across files.
        with self.assertRaisesRegex(fixes.FixError, 'Duplicate'):
            fixes.section_names({self.printer: original}, '2.1.1')
        split = self.config / 'split_resonance.cfg'
        split.write_text(fixes.LEGACY_RESONANCE_SECTIONS[1] + '\n')
        self.printer.write_text(original.replace(fixes.LEGACY_RESONANCE_SECTIONS[1], '', 1)
                                + '\n[include split_resonance.cfg]\n')
        with self.assertRaisesRegex(fixes.FixError, 'Duplicate'):
            self.fix.plan()
        split.unlink()
        self.printer.write_text(original)
        for key in ('polling', 'box_drying'):
            self.fix.options[key] = True
            with self.assertRaisesRegex(fixes.FixError, 'not supported'):
                self.fix.plan()
            self.fix.options[key] = False
        self.printer.write_text(original + '\n[box_extras]\n')
        with self.assertRaisesRegex(fixes.FixError, 'without QIDI Box'):
            self.fix.plan()
        self.printer.write_text(original)
        binary = self.home / 'klipper/klippy/extras/box_extras.so'
        binary.write_bytes(b'unreviewed')
        with self.assertRaisesRegex(fixes.FixError, 'without QIDI Box'):
            self.fix.plan()
        binary.unlink()
        self.updater.idle.return_value = ['mcu', 'mcu mcu_box1']
        with self.assertRaisesRegex(fixes.FixError, 'without QIDI Box'):
            self.fix.plan()
        self.fix.plr.assert_not_called()

    def test_legacy_crlf_config_and_calibration_tail_are_preserved(self):
        self.legacy_configuration()
        tail = '#*# <---------------------- SAVE_CONFIG ---------------------->\n#*# [probe]\n#*# z_offset = 0.17\n'
        original = (self.printer.read_text() + '\n' + tail).replace('\n', '\r\n')
        self.printer.write_bytes(original.encode())
        self.fix.options['fast_variable_saves'] = True
        plan = self.fix.plan()
        edited = base64.b64decode(plan['writes'][str(self.printer)]).decode()
        self.assertTrue(edited.endswith(tail.replace('\n', '\r\n')))
        self.assertNotIn('\n', edited.replace('\r\n', ''))
        self.assertLess(edited.index('[save_variables_fast]'), edited.index('SAVE_CONFIG'))

    def test_legacy_firmware_change_blocks_rollback(self):
        self.legacy_configuration()
        self.fix.options['fast_probing'] = True
        self.prepare()
        self.fix.apply()
        self.fix.options['rollback'] = str(self.fix.tx)
        self.updater.version.return_value = '1.8.2'
        self.fix.stop.reset_mock()
        with self.assertRaisesRegex(fixes.FixError, 'Firmware changed'):
            self.fix.rollback()
        self.fix.stop.assert_not_called()

    def test_legacy_source_changes_after_preparation_block_apply(self):
        self.legacy_configuration()
        self.fix.options['fast_variable_saves'] = True
        self.prepare()
        (self.home / 'klipper/klippy/extras/save_variables.py').write_text('# changed')
        with self.assertRaisesRegex(fixes.FixError, 'Unreviewed saved-variable'):
            self.fix.apply()
        self.fix.stop.assert_not_called()

    def test_legacy_journal_plan_still_requires_the_small_ramlog_layout(self):
        self.legacy_configuration()
        real_is_dir = Path.is_dir
        log_files = {str(self.root / Path(path).name): content for path, content in fixes.LOG_FILES.items()}
        with patch.object(Path, 'is_dir', lambda path: str(path) == '/var/log.hdd/journal' or real_is_dir(path)), \
                patch.object(fixes, 'LOG_FILES', log_files):
            for size, expected in ((128 * 1024 * 1024, True), (129 * 1024 * 1024, False)):
                with patch.object(fixes.os, 'statvfs', return_value=Mock(f_blocks=size, f_frsize=1)):
                    plan = self.fix.plan()
                self.assertEqual(plan['logs'], expected)
                self.assertEqual(any(path in plan['writes'] for path in log_files), expected)

    def test_legacy_verification_uses_the_legacy_hook_without_querying_box_objects(self):
        self.legacy_configuration()
        self.fix.result['version'] = '1.6.0'
        plan = dict(version='1.6.0', controllers=['mcu'], variable_sources={'source': {}}, variable_box=False)
        for profile in ('legacy-1.6.0', 'box-era'):
            self.fix.result['startup_verified'] = False
            self.updater.api.side_effect = [
                {'status': {'mcu': {'mcu_version': 'fixture'}}},
                {'status': {'save_variables_fast': dict(active=True, box_hook='absent', profile=profile, errors=0)}},
            ]
            with patch.object(fixes.time, 'monotonic', side_effect=[0, 0, 6]), patch.object(fixes.time, 'sleep'):
                if profile == 'legacy-1.6.0':
                    self.fix.verify(plan)
                else:
                    with self.assertRaisesRegex(fixes.FixError, 'verification timed out'):
                        self.fix.verify(plan)
            self.assertEqual(self.fix.result['startup_verified'], profile == 'legacy-1.6.0')
            self.assertNotIn('box_output_clock_guard', self.updater.api.call_args_list[-2].args[0])
        self.updater.api.side_effect = None

    def test_identity_accepts_only_reviewed_versions_and_stock_machine_identity(self):
        from types import SimpleNamespace
        self.fix.options['release'] = dict(device_marker='QIDI@x-plus_4', architecture='aarch64')
        with patch.object(fixes, 'Path') as path, patch.object(fixes.os, 'uname', return_value=SimpleNamespace(machine='aarch64')):
            path.return_value.read_text.return_value = 'QIDI@x-plus_4'
            for version in ('1.6.0', '1.8.2', '2.1.1'):
                self.updater.version.return_value = version
                self.assertEqual(fixes.Fixes.identity(self.fix), version)
            self.updater.version.return_value = '1.6.1'
            with self.assertRaisesRegex(fixes.FixError, 'reviewed only'):
                fixes.Fixes.identity(self.fix)
            path.return_value.read_text.return_value = 'different-printer'
            with self.assertRaisesRegex(fixes.FixError, 'reviewed aarch64'):
                fixes.Fixes.identity(self.fix)

    def test_fast_variable_saves_install_idempotence_and_rollback_preserve_values(self):
        extras = self.home / 'klipper/klippy/extras'
        (extras / 'save_variables.py').write_bytes((HELPER.parent / 'tests/fixtures/qidi_save_variables.py').read_bytes())
        self.printer.write_text(self.printer.read_text() + '\n[save_variables]\nfilename: saved_variables.cfg\n')
        values = self.config / 'saved_variables.cfg'
        values.write_bytes(b'[Variables]\nbox_count = 1\nrunout_0 = 0\n')
        before = values.read_bytes()
        self.fix.options['fast_variable_saves'] = True
        self.updater.api.return_value = dict(objects=['box_output_clock_guard', 'save_variables_fast'])
        plan = self.prepare()
        self.assertTrue(plan['variable_sources'])
        self.fix.apply()
        self.assertIn('[save_variables_fast]', self.printer.read_text())
        self.assertEqual(values.read_bytes(), before)
        self.assertFalse(self.fix.plan()['changed'])
        self.fix.options['rollback'] = str(self.fix.tx)
        self.fix.rollback()
        self.assertNotIn('[save_variables_fast]', self.printer.read_text())
        self.assertFalse((extras / 'save_variables_fast.py').exists())
        self.assertEqual(values.read_bytes(), before)

    def test_fast_variable_saves_prepared_mode_and_source_are_bound(self):
        extras = self.home / 'klipper/klippy/extras'
        (extras / 'save_variables.py').write_bytes((HELPER.parent / 'tests/fixtures/qidi_save_variables.py').read_bytes())
        self.printer.write_text(self.printer.read_text() + '\n[save_variables]\n')
        self.fix.options['fast_variable_saves'] = True
        self.prepare()
        self.fix.options['fast_variable_saves'] = False
        with self.assertRaisesRegex(fixes.FixError, 'options differ'):
            self.fix.apply()
        self.fix.stop.assert_not_called()
        self.fix.options['fast_variable_saves'] = True
        (extras / 'save_variables.py').write_text('changed source')
        with self.assertRaisesRegex(fixes.FixError, 'Unreviewed saved-variable'):
            self.fix.apply()
        self.fix.stop.assert_not_called()

    def test_fast_variable_saves_reject_unknown_source_without_writes(self):
        self.fix.options['fast_variable_saves'] = True
        with self.assertRaisesRegex(fixes.FixError, 'require \\[save_variables'):
            self.fix.plan()
        self.printer.write_text(self.printer.read_text() + '\n[save_variables]\n')
        (self.home / 'klipper/klippy/extras/save_variables.py').write_text('custom')
        with self.assertRaisesRegex(fixes.FixError, 'Unreviewed saved-variable'):
            self.fix.plan()
        self.fix.plr.assert_not_called()

    def test_fast_variable_saves_verifies_both_loaded_hooks(self):
        self.fix.result['version'] = '2.1.1'
        for active, box_hook, errors in ((True, 'active', 0), (False, 'active', 0), (True, 'absent', 0), (True, 'active', 1)):
            self.updater.api.side_effect = [
                {'status': {'box_output_clock_guard': {'state': 'inactive-no-box'}}},
                {'status': {'save_variables_fast': dict(active=active, box_hook=box_hook, errors=errors)}},
            ]
            with patch.object(fixes.time, 'monotonic', side_effect=[0, 0, 6]), patch.object(fixes.time, 'sleep'):
                plan = dict(controllers=[], variable_sources={'/extras/box_extras.so': {}}, variable_box=True)
                if active and box_hook == 'active' and not errors:
                    self.fix.verify(plan)
                else:
                    with self.assertRaises(fixes.FixError):
                        self.fix.verify(plan)

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

    def test_fast_probing_preserves_other_settings_and_skips_unknown_speeds(self):
        source = '[z_tilt]\nspeed: 200 # travel\nretry_tolerance: 0.006\n[bed_mesh]\nspeed:150\nprobe_count:14,14\n[gcode_macro KEEP]\ngcode: M204 S1000\n'
        for newline in ('\n', '\r\n'):
            text = source.replace('\n', newline)
            edited, sections, notes = fixes.fast_probing(text)
            self.assertEqual(edited, text.replace('200 # travel', '300 # travel').replace('speed:150', 'speed:300'))
            self.assertEqual(set(sections), {'z_tilt', 'bed_mesh'})
            self.assertFalse(notes)
            self.assertEqual(fixes.fast_probing(edited)[0], edited)
        unknown = source.replace('speed: 200', 'speed: 350')
        edited, sections, notes = fixes.fast_probing(unknown)
        self.assertIn('speed: 350', edited)
        self.assertEqual(sections, ['bed_mesh'])
        self.assertTrue(notes)

    def test_speed_and_drying_options_plan_apply_and_rollback(self):
        macros = self.config / 'macros.cfg'
        macros.write_text(macros.read_text() + '\n[z_tilt]\nspeed:200\n[bed_mesh]\nspeed:150\n')
        (self.config / 'box.cfg').write_text(SENSOR + '\n[heater_generic heater_box1]\nheater_pin: mcu_box1:PA3\n')
        policy = self.config / 'officiall_filas_list.cfg'
        policy.write_text('[fila1]\nbox_max_temp=0\n[fila18]\nbox_max_temp=45\n')
        self.updater.api.return_value = dict(objects=['box_output_clock_guard', 'box_status_compat', 'box_drying'])
        self.fix.options.update(fast_probing=True, box_drying=True)
        original = macros.read_bytes()
        plan = self.prepare()
        self.assertEqual(plan['probe_sections'], ['bed_mesh', 'z_tilt'])
        self.assertEqual(plan['drying_metadata'], fixes.digest(policy.read_bytes()))
        self.fix.apply()
        self.assertIn('[box_drying]', self.printer.read_text())
        self.assertIn('speed:300', macros.read_text())
        self.assertTrue((self.home / 'klipper/klippy/extras/box_drying.py').is_file())
        self.assertFalse(self.fix.plan()['changed'])
        self.fix.options['rollback'] = str(self.fix.tx)
        self.fix.rollback()
        self.assertEqual(macros.read_bytes(), original)
        self.assertNotIn('[box_drying]', self.printer.read_text())
        self.assertFalse((self.home / 'klipper/klippy/extras/box_drying.py').exists())
        self.assertTrue(policy.is_file())

    def test_box_drying_refuses_missing_box_and_invalid_policy_before_writes(self):
        self.fix.options['box_drying'] = True
        with self.assertRaisesRegex(fixes.FixError, 'active QIDI Box heater'):
            self.fix.plan()
        (self.config / 'box.cfg').write_text(SENSOR + '\n[heater_generic heater_box1]\n')
        with self.assertRaisesRegex(fixes.FixError, 'valid QIDI filament'):
            self.fix.plan()
        (self.config / 'officiall_filas_list.cfg').write_text('[fila1]\nbox_max_temp=nan\n')
        with self.assertRaisesRegex(fixes.FixError, 'valid QIDI filament'):
            self.fix.plan()
        self.fix.plr.assert_not_called()

    def test_loaded_probe_speed_is_verified(self):
        self.fix.result['version'] = '2.1.1'
        for speed in (150, 300):
            self.fix.result['startup_verified'] = False
            self.updater.api.side_effect = [
                {'status': {'box_output_clock_guard': {'state': 'inactive-no-box'}}},
                {'status': {'configfile': {'settings': {'bed_mesh': {'speed': speed}}}}},
            ]
            with patch.object(fixes.time, 'monotonic', side_effect=[0, 0, 6]), patch.object(fixes.time, 'sleep'):
                if speed == 300:
                    self.fix.verify(dict(controllers=[], probe_sections=['bed_mesh']))
                else:
                    with self.assertRaises(fixes.FixError):
                        self.fix.verify(dict(controllers=[], probe_sections=['bed_mesh']))
            self.assertEqual(self.fix.result['startup_verified'], speed == 300)

    def test_runout_option_resolves_include_and_preserves_other_macros(self):
        macros = self.config / 'macros.cfg'
        macros.write_text(macros.read_text() + START)
        for version in ('1.8.2', '2.1.1'):
            self.updater.version.return_value = version
            self.assertNotIn(str(macros), self.fix.plan()['writes'])
            self.fix.options['trim_runout_waits'] = True
            plan = self.fix.plan()
            self.assertEqual(plan['runout'], str(macros))
            edited = base64.b64decode(plan['writes'][str(macros)]).decode()
            self.assertIn('SAVE_VARIABLE VARIABLE=runout_{i} VALUE=0', edited)
            self.assertIn('gcode: G4 P100', edited)
            self.fix.options['trim_runout_waits'] = False

    def test_runout_apply_rollback_and_option_binding(self):
        macros = self.config / 'macros.cfg'
        macros.write_text(macros.read_text() + START)
        original = macros.read_bytes()
        self.fix.options['trim_runout_waits'] = True
        self.prepare()
        self.fix.options['trim_runout_waits'] = False
        with self.assertRaisesRegex(fixes.FixError, 'options differ'):
            self.fix.apply()
        self.fix.options['trim_runout_waits'] = True
        self.fix.apply()
        self.assertEqual(fixes.trim_runout_waits(macros.read_text())[1], 'unchanged')
        self.fix.options['rollback'] = str(self.fix.tx)
        self.fix.rollback()
        self.assertEqual(macros.read_bytes(), original)

    def test_startup_verifies_effective_runout_macro_not_only_files(self):
        for applied, normalized in ((False, False), (True, False), (False, True), (True, True)):
            source = fixes.trim_runout_waits(START)[0] if applied else START
            body = source.split('gcode:\n', 1)[1].split('[gcode_macro KEEP]', 1)[0]
            if normalized:
                # Real Klipper configfile responses strip each macro line.
                body = '\n'.join(line.strip() for line in body.splitlines())
            self.fix.result['startup_verified'] = False
            self.fix.result['version'] = '2.1.1'
            self.updater.api.side_effect = [
                {'status': {'box_output_clock_guard': {'state': 'inactive-no-box'}}},
                {'status': {'configfile': {'config': {'gcode_macro PRINT_START': {'gcode': body}}}}},
            ]
            with patch.object(fixes.time, 'monotonic', side_effect=[0, 0, 6]), patch.object(fixes.time, 'sleep'):
                if applied:
                    self.fix.verify(dict(controllers=[], runout='macros.cfg'))
                else:
                    with self.assertRaisesRegex(fixes.FixError, 'Startup verification timed out'):
                        self.fix.verify(dict(controllers=[], runout='macros.cfg'))
            self.assertEqual(self.fix.result['startup_verified'], applied)

    def test_startup_queries_named_controllers_with_escaped_object_names(self):
        controllers = ['mcu', 'mcu U_1', 'mcu mcu_box1']
        self.updater.api.return_value = {'status': {
            name: {'mcu_version': 'fixture'} for name in controllers
        }}
        self.fix.verify(dict(controllers=controllers), guards=False)
        self.updater.api.assert_called_once_with(
            '/printer/objects/query?mcu&mcu%20U_1&mcu%20mcu_box1')

    def test_unknown_runout_loop_is_skipped_and_opt_in_still_reports_why(self):
        macros = self.config / 'macros.cfg'
        macros.write_text(macros.read_text() + START.replace('range(16)', 'range(8)'))
        self.fix.options['trim_runout_waits'] = True
        plan = self.fix.plan()
        self.assertIsNone(plan['runout'])
        self.assertNotIn(str(macros), plan['writes'])
        self.assertTrue(any('unrecognized PRINT_START' in note for note in self.fix.result['notes']))

    def test_usb_files_are_optional_and_rollback_restores_service_policy(self):
        self.fix.options['assets']['qidi-fixes/usb_events.py'] = base64.b64encode(b'# fixture writer\n').decode()
        assets = {str(self.root / Path(path).name): content for path, content in fixes.USB_FILES.items()}
        script = str(self.root / 'usb_events.py')
        with patch.object(fixes, 'USB_FILES', assets), patch.object(fixes, 'USB_SCRIPT', script):
            self.assertNotIn(script, self.fix.plan()['writes'])
            self.fix.options['usb_event_log'] = True
            self.updater.enabled_state.return_value = 'disabled'
            plan = self.prepare()
            self.assertTrue(plan['usb'])
            self.assertIn(script, plan['writes'])
            self.fix.apply()
            self.assertEqual(Path(script).read_bytes(), b'# fixture writer\n')
            self.fix.options['rollback'] = str(self.fix.tx)
            self.fix.rollback()
            self.assertFalse(Path(script).exists())
            self.fix.updater_command.assert_any_call(['udevadm', 'control', '--reload-rules'])
            self.fix.updater_command.assert_any_call(['systemctl', 'start', fixes.USB_SOCKET])
            self.assertFalse(any(call.args[0] == ['systemctl', 'enable', fixes.USB_SOCKET]
                                 for call in self.fix.updater_command.call_args_list))

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

    def test_recovery_screen_probe_requires_a_fresh_sustained_known_loop(self):
        path = self.home / 'printer_data/logs/qidi.log'
        path.parent.mkdir(parents=True)
        loop = b'[2026-11-25 20:33:30][info]page_to : page_id = 56\n'
        self.assertFalse(self.fix.screen_recovery_loop())
        for data, expected in ((loop * 80, True), (loop * 20, False),
                               (loop * 80 + b'[info]other page\n', False),
                               (loop.replace(b'56', b'55') * 80, False),
                               (loop * 40 + loop.replace(b'20:33:30', b'20:34:00') * 40, False)):
            path.write_bytes(data)
            self.assertEqual(self.fix.screen_recovery_loop(), expected)
        path.write_bytes(loop * 80)
        os.utime(path, (0, 0))
        self.assertFalse(self.fix.screen_recovery_loop())

    def test_ramlog_and_screen_recovery_are_in_the_verified_transaction(self):
        real_is_dir = Path.is_dir
        files = {str(self.root / Path(path).name): content for path, content in fixes.LOG_FILES.items()}
        helper = str(self.root / 'log_limits.py')
        self.fix.screen_recovery_loop = Mock(return_value=True)
        with patch.object(Path, 'is_dir', lambda path: str(path) == '/var/log.hdd/journal' or real_is_dir(path)), \
                patch.object(fixes.os, 'statvfs', return_value=Mock(f_blocks=1024, f_frsize=4096)), \
                patch.object(fixes, 'LOG_FILES', files), patch.object(fixes, 'LOG_SCRIPT', helper):
            plan = self.prepare()
            self.assertTrue(plan['screen_restart'])
            self.assertIn(helper, plan['writes'])
            self.assertIn('restart ' + fixes.SCREEN_SERVICE + ' to clear recovery-screen loop', self.fix.result['changes'])
            # Simulate the screen's successful restart, leaving prepare/apply's
            # initial loop evidence identical until the service is restarted.
            def command(args):
                if args == ['systemctl', 'restart', fixes.SCREEN_SERVICE]:
                    self.fix.screen_recovery_loop.return_value = False
            self.fix.updater_command.side_effect = command
            with patch.object(self.fix, 'verify_screen_restart'):
                self.fix.apply()
            self.assertEqual(Path(helper).read_bytes(), base64.b64decode(self.options['assets']['qidi-fixes/log_limits.py']))
            self.fix.updater_command.assert_any_call(['systemctl', 'start', fixes.LOG_SERVICE])
            self.assertFalse(self.fix.plan()['changed'])
            self.fix.options['rollback'] = str(self.fix.tx)
            self.fix.rollback()
            self.assertFalse(Path(helper).exists())
            for path in files:
                self.assertFalse(Path(path).exists())

    def test_screen_restart_failure_is_not_reported_as_success(self):
        self.fix.screen_recovery_loop = Mock(return_value=True)
        self.prepare()
        with patch.object(self.fix, 'verify_screen_restart', side_effect=fixes.FixError('screen still looping')), \
                self.assertRaisesRegex(fixes.FixError, 'screen still looping'):
            self.fix.apply()
        self.assertFalse(self.fix.result['ok'])
        self.assertEqual(self.fix.manifest['status'], 'applying')

    def test_screen_restart_waits_for_new_client_output_and_stability(self):
        before = (1, 2, 100, 100 * 10**9)
        elapsed = [0]
        def tick(_seconds):
            elapsed[0] += 1
        def stamp():
            return before if elapsed[0] < 7 else (1, 2, 200, 107 * 10**9)
        self.fix.screen_log_stamp = Mock(side_effect=stamp)
        self.fix.screen_recovery_loop = Mock(return_value=False)
        with patch.object(fixes.time, 'monotonic', side_effect=lambda: elapsed[0]), \
                patch.object(fixes.time, 'time', side_effect=lambda: 100 + elapsed[0]), \
                patch.object(fixes.time, 'sleep', side_effect=tick):
            self.fix.verify_screen_restart(before)
        self.assertEqual(elapsed[0], 10)
        self.assertEqual(self.fix.screen_recovery_loop.call_count, 4)

    def test_screen_restart_rejects_missing_new_output_and_persistent_loop(self):
        before = (1, 2, 100, 100 * 10**9)
        for looping in (False, True):
            elapsed = [0]
            def tick(_seconds):
                elapsed[0] += 1
            self.fix.screen_log_stamp = Mock(side_effect=lambda: (1, 2, 200, (100 + elapsed[0]) * 10**9) if looping else before)
            self.fix.screen_recovery_loop = Mock(return_value=True)
            with patch.object(fixes.time, 'monotonic', side_effect=lambda: elapsed[0]), \
                    patch.object(fixes.time, 'time', side_effect=lambda: 100 + elapsed[0]), \
                    patch.object(fixes.time, 'sleep', side_effect=tick), \
                    self.assertRaisesRegex(fixes.FixError, 'stable fresh output'):
                self.fix.verify_screen_restart(before)
            self.assertEqual(elapsed[0], 30)

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
