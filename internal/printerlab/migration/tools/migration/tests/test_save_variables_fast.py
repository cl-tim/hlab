"""Real file IO and QIDI command parity; synthetic values, no printer required."""
import ast
import configparser
import importlib.util
import os
from pathlib import Path
import shutil
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
SOURCE = HERE.parents[2] / 'qidi-fixes/save_variables_fast.py'
STOCK = HERE / 'fixtures/qidi_save_variables.py'


def load(path, name):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class Gcode:
    error = ValueError

    def __init__(self):
        self.commands = {}

    def register_command(self, name, handler, **kwargs):
        self.commands[name] = handler


class Command:
    error = ValueError

    def __init__(self, name, text):
        self.params = dict(VARIABLE=name, VALUE=text)

    def get(self, name):
        return self.params[name]


class SavesTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        shutil.copyfile(SOURCE, self.root / SOURCE.name)
        shutil.copyfile(STOCK, self.root / 'save_variables.py')
        self.fast = load(self.root / SOURCE.name, 'fast_saves_fixture')
        self.stock = load(STOCK, 'stock_saves_fixture')
        self.path = self.root / 'saved.cfg'
        self.path.write_text('[Variables]\nbox_count = 1\nrunout_0 = 0\n')
        self.gcode = Gcode()
        self.objects = dict(gcode=self.gcode)
        self.printer = SimpleNamespace(command_error=ValueError, config_error=ValueError,
            lookup_object=lambda name, default=None: self.objects.get(name, default),
            register_event_handler=lambda event, handler: None,
            load_object=lambda config, name: self.objects[name])
        self.config = SimpleNamespace(get_printer=lambda: self.printer,
            get=lambda key: str(self.path), error=ValueError)
        self.sv = self.stock.SaveVariables(self.config)
        self.objects['save_variables'] = self.sv
        self.box = SimpleNamespace(save_variables=self.sv,
            save_variable=lambda varname, value: self.stock.SaveVariables.cmd_SAVE_VARIABLE(self.sv, Command(varname, repr(value))))
        self.objects['box_extras'] = self.box
        self.extension = self.fast.load_config(self.config)
        self.extension.connect()

    def invoke(self, name, text):
        self.gcode.commands['SAVE_VARIABLE'](Command(name, text))

    def test_install_and_no_op_leave_disk_and_saved_objects_intact(self):
        before = self.path.read_bytes(), self.path.stat().st_mtime_ns
        current = self.sv.allVariables
        self.sv.runout_0 = 9
        self.invoke('runout_0', '0')
        self.assertEqual(self.sv.runout_0, 0)
        self.box.save_variable(varname='box_count', value=1)
        self.assertEqual((self.path.read_bytes(), self.path.stat().st_mtime_ns), before)
        self.assertIs(self.sv.allVariables, current)
        self.assertEqual(self.extension.get_status(0), dict(active=True, box_hook='active', writes=0, skipped=2, fallbacks=0, errors=0))

    def test_changed_values_match_stock_file_status_case_collisions_and_defaults(self):
        for box, name, text in ((False, 'runout_0', '1'), (True, 'nested', "{'x': [1, True, None, (2, 3)]}"),
                                (True, 'BOX_COUNT', '7'), (False, 'new_float', '-0.0'),
                                (False, 'runout_0', 'True'), (True, 'empty', 'set()')):
            with self.subTest(box=box, name=name, text=text):
                before = self.path.read_bytes()
                if box:
                    self.box.save_variable(name, ast.literal_eval(text))
                else:
                    self.invoke(name, text)
                actual_bytes, actual_status = self.path.read_bytes(), self.sv.get_status(0)
                self.path.write_bytes(before)
                self.sv.loadVariables()
                self.stock.SaveVariables.cmd_SAVE_VARIABLE(self.sv, Command(name, text))
                self.assertEqual(actual_bytes, self.path.read_bytes())
                self.assertEqual(repr(actual_status), repr(self.sv.get_status(0)))

    def test_atomic_write_preserves_permissions_link_and_flushes_file_then_directory(self):
        self.path.chmod(0o640)
        link = self.root / 'link.cfg'
        link.symlink_to(self.path)
        self.sv.filename = str(link)
        real_fsync = os.fsync
        modes = []

        def flush(fd):
            modes.append(os.fstat(fd).st_mode)
            real_fsync(fd)

        with patch.object(self.fast.os, 'fsync', side_effect=flush):
            self.invoke('runout_0', '1')
        import stat
        self.assertEqual([stat.S_ISREG(mode) for mode in modes], [True, False])
        self.assertTrue(stat.S_ISDIR(modes[1]))
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o640)
        self.assertTrue(link.is_symlink())
        self.assertEqual(list(self.root.glob('.hlab-variables-*')), [])

    def test_write_failure_never_falls_back_to_truncating_stock_writer(self):
        for failure in ('fsync', 'replace'):
            with self.subTest(failure=failure):
                before = self.path.read_bytes()
                with patch.object(self.fast.os, failure, side_effect=OSError('fixture disk failure')):
                    with self.assertRaisesRegex(ValueError, 'durably save'):
                        self.invoke('runout_0', '1')
                self.assertEqual(self.path.read_bytes(), before)
                self.assertEqual(self.sv.allVariables['runout_0'], 0)
                self.assertEqual(list(self.root.glob('.hlab-variables-*')), [])
                self.assertEqual(self.extension.fallbacks, 0)

    def test_directory_flush_failure_reports_failure_and_keeps_memory_consistent(self):
        real_fsync = os.fsync
        import stat

        def flush(fd):
            if stat.S_ISDIR(os.fstat(fd).st_mode):
                raise OSError('fixture directory failure')
            real_fsync(fd)

        with patch.object(self.fast.os, 'fsync', side_effect=flush):
            with self.assertRaisesRegex(ValueError, 'durably save'):
                self.invoke('runout_0', '1')
        self.assertEqual(self.sv.allVariables['runout_0'], 1)
        self.assertIn(b'runout_0 = 1', self.path.read_bytes())
        self.assertEqual(self.extension.writes, 0)
        self.assertEqual(self.extension.errors, 1)

    def test_unsupported_names_literals_and_interpolation_use_original_semantics(self):
        for name, text in (('odd-name', '3'), ('percent', "'100%'"), ('bad', 'not_a_literal')):
            with self.subTest(name=name):
                before = self.path.read_bytes()
                try:
                    self.invoke(name, text)
                    actual_error = None
                except Exception as exc:
                    actual_error = type(exc)
                actual = self.path.read_bytes()
                self.path.write_bytes(before)
                self.sv.loadVariables()
                try:
                    self.stock.SaveVariables.cmd_SAVE_VARIABLE(self.sv, Command(name, text))
                    stock_error = None
                except Exception as exc:
                    stock_error = type(exc)
                self.assertEqual(actual_error, stock_error)
                self.assertEqual(actual, self.path.read_bytes())
        self.assertEqual(self.extension.fallbacks, 3)

    def test_source_gate_and_python37_syntax(self):
        self.fast.validate_sources(self.root, '2.1.1')
        (self.root / 'box_extras.so').write_bytes(b'unknown binary')
        with self.assertRaisesRegex(ValueError, 'Unreviewed'):
            self.fast.validate_sources(self.root, '2.1.1')
        ast.parse(SOURCE.read_text(), feature_version=(3, 7))


if __name__ == '__main__':
    unittest.main()
