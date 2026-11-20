"""Behavioral fixtures for the stock-QIDI transaction; no printer required."""
import copy
import ast
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
import time
import subprocess
import unittest
from unittest.mock import patch

HELPER = Path(__file__).resolve().parents[1] / "update-qidi.py"
spec = importlib.util.spec_from_file_location("qidi_updater", HELPER)
qidi = importlib.util.module_from_spec(spec)
spec.loader.exec_module(qidi)
RELEASE = json.loads((HELPER.parent / "qidi-releases.json").read_text())[0]


def options(**changes):
    value = dict(release=copy.deepcopy(RELEASE), config_policy="preserve", host_only=True,
                 check_only=False, moonraker_port=7125, wait_seconds=5, phase="prepare", api_key="fixture-secret")
    value.update(changes)
    return value


class UpdaterTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.root_patch = patch.object(qidi, "ROOT", self.root)
        self.root_patch.start()
        self.addCleanup(self.root_patch.stop)
        self.addCleanup(self.temp.cleanup)
        self.update = qidi.QIDIUpdate(options())

    def api(self, path):
        if path == "/printer/info":
            return dict(state="ready", klipper_path=str(self.update.home / "klipper"), config_file=str(self.update.config / "printer.cfg"))
        if path == "/printer/objects/query?heaters":
            return {"status": {"heaters": {"available_heaters": ["extruder", "heater_bed", "heater_generic chamber"]}}}
        if path == "/printer/objects/list":
            return {"objects": ["mcu", "mcu U_1", "beacon"]}
        return {"status": self.status}

    def idle_status(self):
        self.status = {"print_stats": {"state": "standby"}, "pause_resume": {"is_paused": False}, "idle_timeout": {"state": "Ready"},
                       "extruder": {"target": 0}, "heater_bed": {"target": 0}, "heater_generic chamber": {"target": 0}}

    def test_helper_keeps_stock_python37_syntax(self):
        ast.parse(HELPER.read_text(), feature_version=(3, 7))

    def test_idle_gate_covers_paused_motion_and_every_heater(self):
        self.idle_status()
        with patch.object(self.update, "api", side_effect=self.api):
            self.assertEqual(self.update.idle(), ["mcu", "mcu U_1", "beacon"])
            for section, field, value in (("print_stats", "state", "printing"), ("pause_resume", "is_paused", True),
                                           ("idle_timeout", "state", "Printing"), ("heater_generic chamber", "target", 50),
                                           ("heater_bed", "target", float("nan")), ("extruder", "target", None)):
                self.idle_status()
                self.status[section][field] = value
                with self.assertRaises(qidi.UpdateError):
                    self.update.idle()

    def test_missing_heater_and_pause_evidence_fails_closed(self):
        self.idle_status()
        del self.status["heater_generic chamber"]
        with patch.object(self.update, "api", side_effect=self.api), self.assertRaises(qidi.UpdateError):
            self.update.idle()

    def test_configuration_archive_restores_contents_modes_and_links(self):
        self.update.config.mkdir(parents=True)
        config = self.update.config / "printer.cfg"
        config.write_text("custom Beacon configuration\n")
        config.chmod(0o600)
        link = self.update.config / "linked.cfg"
        link.symlink_to("printer.cfg")
        paths = self.update.protected_paths()
        before = self.update.fingerprint(paths)
        self.update.transaction = self.root / "transaction"
        self.update.transaction.mkdir()
        self.update.archive(self.update.transaction / "config.tar.gz", paths)
        config.write_text("vendor replacement")
        config.chmod(0o777)
        self.update.restore_config()
        self.assertEqual(self.update.fingerprint(paths), before)
        self.assertTrue(link.is_symlink())

    def test_download_refuses_replaced_payload(self):
        self.update.transaction = self.root
        class Response(io.BytesIO):
            def geturl(self):
                return "https://vendor.example/firmware"
        opener = unittest.mock.Mock()
        opener.open.return_value = Response(b"wrong bytes")
        artifact = dict(url="https://vendor.example/firmware", bytes=11, sha256="a" * 64)
        with patch.object(qidi.urllib.request, "build_opener", return_value=opener), self.assertRaises(qidi.UpdateError):
            self.update.download(artifact, "host.deb")

    def test_download_accepts_only_exact_size_and_digest(self):
        self.update.transaction = self.root
        class Response(io.BytesIO):
            def geturl(self):
                return "https://vendor.example/firmware"
        opener = unittest.mock.Mock()
        opener.open.return_value = Response(b"reviewed bytes")
        artifact = dict(url="https://vendor.example/firmware", bytes=14, sha256=hashlib.sha256(b"reviewed bytes").hexdigest())
        with patch.object(qidi.urllib.request, "build_opener", return_value=opener):
            self.update.download(artifact, "host.deb")
        self.assertEqual((self.root / "host.deb").read_bytes(), b"reviewed bytes")

    def test_workstation_staging_is_reverified_without_using_network(self):
        stage = self.root / "var/lib/hlab/qidi-payloads" / ("a" * 32)
        stage.mkdir(parents=True)
        source=stage/"host.deb"
        source.write_bytes(b"reviewed bytes")
        self.update.options["firmware_staging"]="/var/lib/hlab/qidi-payloads/" + "a" * 32
        self.update.transaction=self.root/"transaction"
        self.update.transaction.mkdir()
        artifact=dict(bytes=14,sha256=hashlib.sha256(b"reviewed bytes").hexdigest())
        with patch.object(qidi.urllib.request,"build_opener") as network:
            self.update.download(artifact,"host.deb")
        network.assert_not_called()
        self.assertEqual((self.update.transaction/"host.deb").read_bytes(),b"reviewed bytes")
        source.write_bytes(b"corrupt! bytes")
        with self.assertRaisesRegex(qidi.UpdateError,"does not match"):
            self.update.download(artifact,"host.deb")

    def test_screen_requires_ack_for_each_block_including_final_partial_block(self):
        image = self.root / "screen.tft"
        image.write_bytes(b"x" * (4096 + 10))
        screen = object.__new__(qidi.Screen)
        screen.fd = 1
        writes, acks = [], []
        with patch.object(screen, "identify", return_value=115200), patch.object(screen, "baud"), patch.object(qidi.termios, "tcflush"), \
                patch.object(screen, "write", side_effect=lambda data: writes.append(data)), patch.object(screen, "acknowledge", side_effect=lambda: acks.append(True)):
            screen.upload(image)
        self.assertTrue(writes[0].startswith(b"whmi-wri 4106,921600,0"))
        self.assertEqual([len(data) for data in writes[1:]], [4096, 10])
        self.assertEqual(len(acks), 3)

    def test_missing_screen_ack_stops_before_sending_more_data(self):
        screen = object.__new__(qidi.Screen)
        with patch.object(screen, "read", return_value=b""), self.assertRaises(qidi.UpdateError):
            screen.acknowledge()
        with patch.object(screen, "read", return_value=b"\x05garbage"), self.assertRaises(qidi.UpdateError):
            screen.acknowledge()

    def test_check_never_downloads_backs_up_or_installs(self):
        update = qidi.QIDIUpdate(options(phase="check", check_only=True))
        with patch.object(update, "preflight", return_value=["mcu"]), patch.object(update, "prepare") as prepare, patch.object(update, "apply") as apply:
            result = update.run()
        self.assertTrue(result["ok"])
        self.assertEqual(result["status"], "checked")
        prepare.assert_not_called()
        apply.assert_not_called()

    def test_preflight_rejects_wrong_live_model_before_any_installer(self):
        (self.root / "dev_info.txt").write_text("QIDI@wrong-model")
        with self.assertRaisesRegex(qidi.UpdateError, "model"):
            self.update.preflight()

    def test_persistent_service_inhibitor_works_with_etc_units_and_survives_reentry(self):
        self.update.transaction = self.root / "transaction"
        with patch.object(qidi, "command") as run:
            self.update.inhibit(["klipper.service", "moonraker.service"])
            dropin = self.root / "etc/systemd/system/klipper.service.d/90-hlab-qidi-update.conf"
            self.assertIn("ConditionPathExists=", dropin.read_text())
            self.assertFalse((self.update.transaction / "allow-service-start").exists())
            self.update.inhibit(["klipper.service", "moonraker.service"])
            self.update.uninhibit(["klipper.service", "moonraker.service"])
            self.assertFalse(dropin.exists())
        self.assertIn(unittest.mock.call(["systemctl", "stop", "klipper.service", "moonraker.service"]), run.call_args_list)

    def prepared_transaction(self):
        update = self.update
        update.options.update(phase="apply", backup="/var/lib/hlab/qidi-updates/" + "a" * 32, local_backup="desktop/recovery.tar.gz")
        update.transaction = self.root / update.options["backup"].lstrip("/")
        update.transaction.mkdir(parents=True, mode=0o700)
        update.config.mkdir(parents=True)
        (update.config / "printer.cfg").write_text("custom configuration")
        for name in ("host.deb", "recovery.tar.gz", "files-before.tar.gz"):
            (update.transaction / name).write_bytes(name.encode())
        update.archive(update.transaction / "config.tar.gz", update.protected_paths())
        update.release["host"]["sha256"] = qidi.sha256(update.transaction / "host.deb")
        update.options["backup_sha256"] = qidi.sha256(update.transaction / "recovery.tar.gz")
        manifest = dict(status="prepared", created=time.time(), release=update.release, before="1.8.2", host_only=True, config_policy="preserve",
                        protected=update.fingerprint(update.protected_paths()), config_sha256=qidi.sha256(update.transaction / "config.tar.gz"),
                        files_sha256=qidi.sha256(update.transaction / "files-before.tar.gz"), controllers=["mcu"], enabled_services={})
        update.write_json("manifest.json", manifest)
        return update

    def test_install_restores_configuration_before_starting_services(self):
        update = self.prepared_transaction()
        events = []
        real_command = qidi.command
        def run_command(args, **kwargs):
            if args[0] == "tar":
                return real_command(args, **kwargs)
            if args[:2] == ["systemctl", "start"]:
                events.append("start")
                self.assertEqual((update.config / "printer.cfg").read_text(), "custom configuration")
            return "loaded"
        def install(args, **kwargs):
            self.assertEqual(args[0], "dpkg")
            events.append("install")
            (update.config / "printer.cfg").write_text("vendor defaults")
            return subprocess.CompletedProcess(args, 0)
        # tar uses the real runner, while the actual dpkg action is a fixture.
        def restore():
            import tarfile
            with tarfile.open(update.transaction / "config.tar.gz") as archive:
                archive.extractall(self.root, filter="fully_trusted")
            events.append("restore")
        with patch.object(update, "preflight"), patch.object(update, "idle"), patch.object(update, "service_state", return_value="active"), \
                patch.object(update, "agent_active", return_value=False), patch.object(update, "inhibit", side_effect=lambda _: events.append("stop")), \
                patch.object(update, "uninhibit"), patch.object(update, "restore_config", side_effect=restore), patch.object(update, "verify"), \
                patch.object(qidi, "command", side_effect=run_command), patch.object(qidi.subprocess, "run", side_effect=install):
            update.apply()
        self.assertEqual(events, ["stop", "install", "restore", "start"])
        self.assertEqual(json.loads((update.transaction / "manifest.json").read_text())["status"], "updated")

    def test_install_failure_keeps_inhibitor_restores_configuration_and_refuses_retry(self):
        update = self.prepared_transaction()
        def install(args, **kwargs):
            (update.config / "printer.cfg").write_text("partial vendor install")
            return subprocess.CompletedProcess(args, 1)
        def restore():
            (update.config / "printer.cfg").write_text("custom configuration")
        with patch.object(update, "preflight"), patch.object(update, "idle"), patch.object(update, "service_state", return_value="active"), \
                patch.object(update, "agent_active", return_value=False), patch.object(update, "inhibit") as inhibit, \
                patch.object(update, "restore_config", side_effect=restore), patch.object(qidi, "command", return_value="loaded"), \
                patch.object(qidi.subprocess, "run", side_effect=install), self.assertRaises(qidi.UpdateError):
            update.apply()
        self.assertEqual(inhibit.call_count, 2)
        self.assertEqual(update.result["status"], "recovery_required")
        self.assertEqual((update.config / "printer.cfg").read_text(), "custom configuration")
        with self.assertRaisesRegex(qidi.UpdateError, "already started"):
            update.apply()

    def test_changed_backup_blocks_before_any_service_stop(self):
        update = self.prepared_transaction()
        (update.transaction / "config.tar.gz").write_bytes(b"corrupt")
        with patch.object(update, "inhibit") as inhibit, self.assertRaisesRegex(qidi.UpdateError, "changed"):
            update.apply()
        inhibit.assert_not_called()


if __name__ == "__main__":
    unittest.main()
