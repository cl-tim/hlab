"""Real Git transactions with a simulated service and Moonraker boundary."""

import contextlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tarfile
from urllib.parse import unquote

import pytest


spec = importlib.util.spec_from_file_location("klipper_update", Path(__file__).parents[1] / "update-klipper.py")
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


def git(path, *args):
    return subprocess.check_output(["git", "-C", str(path), *args], text=True).strip()


class Printer(updater.KlipperUpdate):
    def __init__(self, options, home, origin):
        super().__init__(options, home)
        self.origin = origin
        self.service_actions = []
        self.state = "standby"
        self.heaters = {"extruder": 0, "heater_bed": 0, "heater_generic chamber": 0}
        self.paused = False
        self.idle_state = "Ready"
        self.klippy_state = "ready"
        self.failed_revision = None
        self.fail_stop = False
        self.fail_rollback = False
        self.beacon_samples = True
        self.mcu_online = True
        self.service_active = "active"
        self.restart_count = "0"
        self.syncs = 0

    def validate_origin(self):
        # Only the transport endpoint changes: fetch/merge/backup/rollback all
        # exercise real Git and filesystem operations.
        assert self.git("remote", "get-url", "origin") == str(self.origin)

    @contextlib.contextmanager
    def lock(self):
        yield

    def command(self, args, timeout=30):
        if args == ["sync"]:
            self.syncs += 1
            return ""
        if args[:3] == ["sudo", "-n", "/usr/local/sbin/qidi-plus4-host-tuning"]:
            return "result=tuned"
        return super().command(args, timeout)

    def systemctl(self, *args):
        self.service_actions.append(args)
        if args[0] == "stop" and self.fail_stop:
            self.fail_stop = False
            raise updater.UpdateError("stop failed")
        if args[0] == "start" and self.fail_rollback and self.git("rev-parse", "HEAD") == self.result["before"]:
            raise updater.UpdateError("rollback start failed")
        return ""

    def service_state(self):
        return {"User": "mks", "ActiveState": self.service_active,
                "SubState": "running" if self.service_active == "active" else "start-post",
                "NRestarts": self.restart_count, "MainPID": "123"}

    def api(self, path):
        if path == "/printer/info":
            return {"state": "error" if self.git("rev-parse", "HEAD") == self.failed_revision else self.klippy_state,
                    "klipper_path": str(self.repo), "config_file": str(self.data / "config/printer.cfg"),
                    "process_id": 123, "software_version": self.git("describe", "--tags", "--always") + "-dirty"}
        if path == "/printer/objects/list":
            return {"objects": ["mcu", "mcu U_1", "beacon"]}
        if path == "/printer/objects/query?heaters":
            return {"status": {"heaters": {"available_heaters": list(self.heaters)}}}
        objects = {"print_stats": {"state": self.state}, "pause_resume": {"is_paused": self.paused},
                   "idle_timeout": {"state": self.idle_state},
                   "mcu": {"mcu_version": "controller-v1"} if self.mcu_online else {},
                   "mcu U_1": {"mcu_version": "controller-v1"},
                   "beacon": {"last_received_sample": {"temp": 25} if self.beacon_samples else None}}
        objects.update({key: {"target": value} for key, value in self.heaters.items()})
        return {"status": {name: objects[name] for name in unquote(path.split("?", 1)[1]).split("&") if name in objects}}


@pytest.fixture
def printer(tmp_path, monkeypatch):
    monkeypatch.setenv("GIT_CONFIG_NOSYSTEM", "1")
    monkeypatch.setenv("GIT_CONFIG_GLOBAL", str(tmp_path / "no-gitconfig"))
    origin = tmp_path / "upstream"
    origin.mkdir()
    git(origin, "init", "-b", "master")
    git(origin, "config", "user.email", "test@example.invalid")
    git(origin, "config", "user.name", "Printerlab test")
    (origin / "host.py").write_text("version = 1\n")
    (origin / "scripts").mkdir()
    (origin / "scripts/klippy-requirements.txt").write_text("cffi==1.17.1\n")
    git(origin, "add", ".")
    git(origin, "commit", "-m", "baseline")
    git(origin, "tag", "v0.13.0")
    home = tmp_path / "printer"
    home.mkdir()
    git(home, "clone", str(origin), "klipper")
    (origin / "host.py").write_text("version = 2\n")
    git(origin, "commit", "-am", "update")
    config = home / "printer_data/config"
    config.mkdir(parents=True)
    (config / "printer.cfg").write_text("[include hardware.cfg]\n# saved calibration\n")
    (config / "hardware.cfg").write_text("[mcu]\nserial: /dev/ttyS2\n")
    (home / "klipper/extension.py").write_text("untouched\n")
    options = {"account": "mks", "check_only": False, "wait_seconds": 5, "moonraker_port": 7125}
    # Don't modify the pytest process's signal handlers when exercising recovery.
    monkeypatch.setattr(updater.signal, "signal", lambda *args: None)
    return Printer(options, home, origin)


def test_update_and_backup_preserve_config_and_extensions(printer):
    before = printer.git("rev-parse", "HEAD")
    digest = printer.configuration_digest()
    result = printer.run()
    assert result["ok"] and result["status"] == "updated"
    assert result["before"] == before and result["after"] == git(printer.origin, "rev-parse", "HEAD")
    assert printer.service_actions == [("stop", "klipper.service"), ("start", "--no-block", "klipper.service")]
    assert printer.configuration_digest() == digest
    assert (printer.repo / "extension.py").read_text() == "untouched\n"
    backup = Path(result["backup"])
    assert json.loads((backup / "manifest.json").read_text())["configuration_sha256"] == digest
    assert printer.git("rev-parse", "rollback/" + backup.name) == before
    with tarfile.open(backup / "config-before.tar.gz") as archive:
        assert archive.extractfile("config/printer.cfg").read() == (printer.data / "config/printer.cfg").read_bytes()
    assert printer.syncs == 2


def test_check_only_fetches_latest_but_does_not_stop_or_change_checkout(printer):
    printer.options["check_only"] = True
    before = printer.git("rev-parse", "HEAD")
    result = printer.run()
    assert result["ok"] and result["status"] == "available"
    assert result["after"] == before and result["target"] != before
    assert not printer.service_actions and not result["backup"]
    assert not (printer.data / "backups").exists()


def test_current_revision_is_verified_without_restart_or_backup(printer):
    printer.git("pull", "--ff-only")
    result = printer.run()
    assert result["ok"] and result["status"] == "current"
    assert not printer.service_actions and not result["backup"]


@pytest.mark.parametrize("field,value", [
    ("state", "printing"), ("state", "paused"), ("state", "unknown"),
    ("paused", True), ("paused", None), ("idle_state", "Printing"),
    ("klippy_state", "error"), ("klippy_state", "shutdown"),
])
def test_unsafe_printer_never_stops_or_changes_source(printer, field, value):
    before = printer.git("rev-parse", "HEAD")
    setattr(printer, field, value)
    with pytest.raises(updater.UpdateError):
        printer.run()
    assert not printer.service_actions and printer.git("rev-parse", "HEAD") == before
    assert not (printer.data / "backups").exists()


@pytest.mark.parametrize("target", [60, None, True, float("nan")])
def test_chamber_heater_or_missing_target_blocks_update(printer, target):
    printer.heaters["heater_generic chamber"] = target
    with pytest.raises(updater.UpdateError, match="zero targets"):
        printer.run()
    assert not printer.service_actions


def test_dirty_tracked_source_is_not_overwritten(printer):
    (printer.repo / "host.py").write_text("user changes\n")
    with pytest.raises(updater.UpdateError, match="local changes"):
        printer.run()
    assert (printer.repo / "host.py").read_text() == "user changes\n"
    assert not printer.service_actions


def test_unofficial_origin_is_rejected(printer):
    with pytest.raises(updater.UpdateError, match="official"):
        updater.KlipperUpdate.validate_origin(printer)
    assert not printer.service_actions


def test_dependency_changes_do_not_partially_update_environment(printer):
    (printer.origin / "scripts/klippy-requirements.txt").write_text("cffi==2.0.0\n")
    git(printer.origin, "commit", "-am", "dependencies")
    before = printer.git("rev-parse", "HEAD")
    with pytest.raises(updater.UpdateError, match="Python dependencies"):
        printer.run()
    assert not printer.service_actions and printer.git("rev-parse", "HEAD") == before


def test_failed_new_startup_rolls_back_real_checkout(printer):
    before = printer.git("rev-parse", "HEAD")
    printer.failed_revision = git(printer.origin, "rev-parse", "HEAD")
    result = printer.run()
    assert not result["ok"] and result["status"] == "rolled_back"
    assert result["after"] == before and printer.git("rev-parse", "HEAD") == before
    assert "error" in result["error"] and result["rollback_error"] == ""
    assert printer.service_actions[-2:] == [("stop", "klipper.service"), ("start", "--no-block", "klipper.service")]


def test_failed_rollback_is_reported_and_backup_retained(printer):
    printer.failed_revision = git(printer.origin, "rev-parse", "HEAD")
    printer.fail_rollback = True
    result = printer.run()
    assert not result["ok"] and result["status"] == "rollback_failed"
    assert result["rollback_error"] == "rollback start failed"
    assert Path(result["backup"]).exists()


def test_failed_stop_restarts_previous_host(printer):
    printer.fail_stop = True
    result = printer.run()
    assert result["status"] == "rolled_back" and not result["ok"]
    assert printer.service_actions[-1] == ("start", "--no-block", "klipper.service")


def test_ready_api_does_not_hide_incomplete_systemd_startup(printer, monkeypatch):
    printer.options["check_only"] = True
    printer.service_active = "activating"
    # The baseline rejects an unfinished service before any update.
    with pytest.raises(updater.UpdateError, match="existing Klipper service"):
        printer.run()
    printer.service_active = "active"
    printer.idle()
    digest = printer.configuration_digest()
    printer.service_active = "activating"
    monkeypatch.setattr(updater.time, "sleep", lambda _: None)
    times = iter([0, 0, 6])
    monkeypatch.setattr(updater.time, "monotonic", lambda: next(times))
    with pytest.raises(updater.UpdateError, match="finish startup"):
        printer.verify(printer.git("rev-parse", "HEAD"), digest)


@pytest.mark.parametrize("attribute,message", [("beacon_samples", "Beacon"), ("mcu_online", "controller")])
def test_missing_hardware_evidence_is_not_reported_as_success(printer, attribute, message):
    setattr(printer, attribute, False)
    printer.options["check_only"] = True
    with pytest.raises(updater.UpdateError, match=message):
        printer.run()


def test_busy_printer_during_failed_update_is_not_stopped_for_rollback(printer, monkeypatch):
    original = printer.verify
    def fail_new(revision, digest):
        if revision == printer.result["target"]:
            printer.state = "printing"
            raise updater.UpdateError("verification failure")
        return original(revision, digest)
    monkeypatch.setattr(printer, "verify", fail_new)
    result = printer.run()
    assert result["status"] == "rollback_failed" and "not idle" in result["rollback_error"]
    assert printer.service_actions == [("stop", "klipper.service"), ("start", "--no-block", "klipper.service")]


def test_empty_config_is_rejected_without_restart(printer):
    (printer.data / "config/printer.cfg").write_bytes(b"")
    with pytest.raises(updater.UpdateError, match="empty"):
        printer.run()
    assert not printer.service_actions


def test_check_is_safe_during_an_active_print(printer):
    printer.options["check_only"] = True
    printer.state = "printing"
    printer.heaters["extruder"] = 220
    result = printer.run()
    assert result["ok"] and result["status"] == "available"
    assert not printer.service_actions


def test_configuration_change_during_backup_aborts_before_stop(printer, monkeypatch):
    original = printer.backup
    def change(digest):
        original(digest)
        (printer.data / "config/printer.cfg").write_text("user's new configuration\n")
    monkeypatch.setattr(printer, "backup", change)
    with pytest.raises(updater.UpdateError, match="Configuration changed"):
        printer.run()
    assert not printer.service_actions
    assert (printer.data / "config/printer.cfg").read_text() == "user's new configuration\n"


def test_checkout_change_during_backup_aborts_before_stop(printer, monkeypatch):
    original = printer.backup
    def change(digest):
        original(digest)
        (printer.repo / "host.py").write_text("concurrent edit\n")
    monkeypatch.setattr(printer, "backup", change)
    with pytest.raises(updater.UpdateError, match="checkout changed"):
        printer.run()
    assert not printer.service_actions
    assert (printer.repo / "host.py").read_text() == "concurrent edit\n"


def test_a_restart_during_new_startup_is_detected(printer):
    printer.idle()
    printer.restart_count = "1"
    with pytest.raises(updater.UpdateError, match="restarted"):
        printer.verify(printer.git("rev-parse", "HEAD"), printer.configuration_digest())


@pytest.mark.skipif(os.name == "nt", reason="printer flock is POSIX-only")
def test_parallel_transactions_are_locked_and_lock_is_released(printer):
    with updater.KlipperUpdate.lock(printer):
        with pytest.raises(updater.UpdateError, match="Another"):
            with updater.KlipperUpdate.lock(printer):
                pytest.fail("second update acquired the lock")
    with updater.KlipperUpdate.lock(printer):
        pass
