"""Bounded synthetic ramlog fixtures; no printer or system logs are changed."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest


HELPER = Path(__file__).resolve().parents[3] / 'qidi-fixes/log_limits.py'
spec = importlib.util.spec_from_file_location('log_limits', HELPER)
limits = importlib.util.module_from_spec(spec)
spec.loader.exec_module(limits)


class LogLimitsTests(unittest.TestCase):
    def test_flood_is_trimmed_in_place_to_recent_complete_lines(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'syslog'
            prefix = b'old\n' * (limits.MAX_BYTES // 4)
            recent = b'recent recovery-screen message\n' * 100
            path.write_bytes(prefix + recent)
            before = path.stat()
            self.assertTrue(limits.trim_log(str(path)))
            self.assertEqual(path.stat().st_ino, before.st_ino)
            self.assertEqual(path.stat().st_mode, before.st_mode)
            self.assertLessEqual(path.stat().st_size, limits.KEEP_BYTES)
            self.assertTrue(path.read_bytes().endswith(recent))
            self.assertFalse(limits.trim_log(str(path)))

    def test_missing_small_and_linked_logs_are_not_changed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'syslog'
            self.assertFalse(limits.trim_log(str(path)))
            path.write_bytes(b'keep\n')
            self.assertFalse(limits.trim_log(str(path)))
            self.assertEqual(path.read_bytes(), b'keep\n')
            path.write_bytes(b'x' * (limits.MAX_BYTES + 1))
            link = Path(directory) / 'linked'
            link.symlink_to(path)
            with self.assertRaises(OSError):
                limits.trim_log(str(link))
            link.unlink()
            os.link(path, link)
            with self.assertRaises(ValueError):
                limits.trim_log(str(path))
            self.assertGreater(path.stat().st_size, limits.MAX_BYTES)

    def test_only_known_plain_logs_are_maintained_in_both_ramlog_trees(self):
        with tempfile.TemporaryDirectory() as directory:
            roots = [Path(directory) / name for name in ('ram', 'disk')]
            for root in roots:
                root.mkdir()
                for name in ('syslog', 'daemon.log.1', 'unrelated', 'journal'):
                    (root / name).write_bytes(b'x' * (limits.MAX_BYTES + 1))
            limits.maintain([str(root) for root in roots])
            for root in roots:
                for name in ('syslog', 'daemon.log.1'):
                    self.assertLessEqual((root / name).stat().st_size, limits.KEEP_BYTES)
                for name in ('unrelated', 'journal'):
                    self.assertGreater((root / name).stat().st_size, limits.MAX_BYTES)


if __name__ == '__main__':
    unittest.main()
