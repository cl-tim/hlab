"""Synthetic USB records and local sockets only; no udev or printer changes."""
import ast
import importlib.util
import json
import os
from pathlib import Path
import socket
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import MagicMock, patch

SOURCE = Path(__file__).resolve().parents[3] / 'qidi-fixes/usb_events.py'
spec = importlib.util.spec_from_file_location('usb_events', SOURCE)
usb = importlib.util.module_from_spec(spec)
spec.loader.exec_module(usb)
BOOT = '11111111-2222-3333-4444-555555555555'


class UsbTests(unittest.TestCase):
    def record(self, **changes):
        record = dict(schema=1, action='remove', port='2-1.3', ids='1d50/614e/100',
                      bootId=BOOT, epoch=1800000100.0, monotonic=100.0)
        record.update(changes)
        return record

    def test_python37_and_invalid_records(self):
        ast.parse(SOURCE.read_text(), feature_version=(3, 7))
        usb.validate(self.record())
        for changes in (dict(schema=True), dict(action='reset'), dict(port='../bad'), dict(ids='serial-number'),
                        dict(bootId='other'), dict(epoch=float('nan')), dict(monotonic=-1), dict(epoch=True),
                        dict(serial='private')):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                usb.validate(self.record(**changes))

    def test_emit_queues_one_bounded_record_without_serials(self):
        with tempfile.TemporaryDirectory() as name:
            endpoint = str(Path(name) / 'queue.sock')
            with socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM) as receiver:
                receiver.bind(endpoint)
                receiver.settimeout(1)
                with patch.object(usb, 'event', return_value=self.record()):
                    usb.emit('remove', '2-1.3', '1d50/614e/100', endpoint)
                data = receiver.recv(usb.MAX_EVENT_BYTES + 1)
                self.assertLessEqual(len(data), usb.MAX_EVENT_BYTES)
                self.assertEqual(json.loads(data), self.record())
                self.assertNotIn(b'serial', data)

    def test_rotation_is_bounded_durable_and_preserves_complete_records(self):
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            with patch.object(usb, 'trusted_directory'), patch.object(usb, 'MAX_BYTES', 700), \
                    patch.object(usb.os, 'fsync', wraps=os.fsync) as sync:
                for index in range(20):
                    usb.append_record(self.record(monotonic=index), directory)
                self.assertGreaterEqual(sync.call_count, 40)
                for path in directory.iterdir():
                    self.assertLessEqual(path.stat().st_size, 700)
                    for line in path.read_text().splitlines():
                        usb.validate(json.loads(line))
                # A new writer after restart appends to the same retained files.
                usb.append_record(self.record(bootId='aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee'), directory)
                self.assertEqual(json.loads((directory / 'events.jsonl').read_text().splitlines()[-1])['bootId'],
                                 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee')
                self.assertEqual(set(path.name for path in directory.iterdir()), {'events.jsonl', 'events.jsonl.1'})

    def test_writer_refuses_symlink_without_touching_target(self):
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            target = directory / 'keep'
            target.write_text('untouched')
            (directory / 'events.jsonl').symlink_to(target)
            with patch.object(usb, 'trusted_directory'), self.assertRaisesRegex(ValueError, 'Symlinked'):
                usb.append_record(self.record(), directory)
            self.assertEqual(target.read_text(), 'untouched')

    def test_root_writer_refuses_untrusted_directory(self):
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            directory.chmod(0o777)
            with self.assertRaisesRegex(ValueError, 'Untrusted'):
                usb.trusted_directory(directory)

    def test_service_announces_ready_after_durable_start_and_ignores_bad_packets(self):
        receiver, notifier = MagicMock(), MagicMock()
        receiver.__enter__.return_value = receiver
        notifier.__enter__.return_value = notifier
        receiver.family = socket.AF_UNIX
        receiver.getsockopt.return_value = socket.SOCK_DGRAM
        receiver.recv.side_effect = [b'bad json', b'x' * 1025, json.dumps(self.record()).encode(), KeyboardInterrupt]
        events = []
        notifier.sendto.side_effect = lambda *args: events.append('ready')
        with patch.dict(os.environ, LISTEN_PID=str(os.getpid()), LISTEN_FDS='1', NOTIFY_SOCKET='@fixture'), \
                patch.object(usb.os, 'geteuid', return_value=0), \
                patch.object(usb.socket, 'socket', side_effect=[receiver, notifier]), \
                patch.object(usb, 'event', return_value=self.record(action='start', port='-', ids='-')), \
                patch.object(usb, 'append_record', side_effect=lambda record: events.append(record['action'])), \
                patch.object(usb.sys, 'stderr'), self.assertRaises(KeyboardInterrupt):
            usb.serve()
        self.assertEqual(events, ['start', 'ready', 'remove'])
        notifier.sendto.assert_called_once_with(b'READY=1', '\0fixture')

    def test_service_does_not_announce_ready_if_initial_persistence_fails(self):
        receiver = MagicMock()
        receiver.__enter__.return_value = receiver
        receiver.family = socket.AF_UNIX
        receiver.getsockopt.return_value = socket.SOCK_DGRAM
        with patch.dict(os.environ, LISTEN_PID=str(os.getpid()), LISTEN_FDS='1'), \
                patch.object(usb.os, 'geteuid', return_value=0), \
                patch.object(usb.socket, 'socket', return_value=receiver) as factory, \
                patch.object(usb, 'event', return_value=self.record()), \
                patch.object(usb, 'append_record', side_effect=OSError('disk full')), self.assertRaises(OSError):
            usb.serve()
        factory.assert_called_once_with(fileno=3)

    @unittest.skipUnless(shutil.which('systemd-analyze'), 'systemd unit validator unavailable')
    def test_installed_units_pass_systemd_validation_without_starting_services(self):
        helper_path = SOURCE.parents[1] / 'tools/migration/apply-qidi-fixes.py'
        helper_spec = importlib.util.spec_from_file_location('usb_fixes_units', helper_path)
        helper = importlib.util.module_from_spec(helper_spec)
        helper_spec.loader.exec_module(helper)
        with tempfile.TemporaryDirectory() as name:
            units = []
            for path, text in helper.USB_FILES.items():
                if path.endswith(('.socket', '.service')):
                    target = Path(name) / Path(path).name
                    target.write_text(text)
                    units.append(str(target))
            result = subprocess.run(['systemd-analyze', 'verify', '--man=no'] + units,
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr.decode())


if __name__ == '__main__':
    unittest.main()
