#!/usr/bin/env python3
"""Guarded upstream Klipper host updates, streamed over SSH by printerlab."""

import contextlib
import hashlib
import json
import math
import os
from pathlib import Path
import re
import signal
import subprocess
import tarfile
import tempfile
import time
import urllib.request


UPSTREAM = "https://github.com/Klipper3d/klipper.git"
COMMIT = re.compile(r"^[0-9a-f]{40}$")


class UpdateError(Exception):
    pass


class KlipperUpdate:
    def __init__(self, options, home=None):
        self.options = options
        self.home = Path(home) if home else Path.home()
        self.repo = self.home / "klipper"
        self.data = self.home / "printer_data"
        self.deadline = options["wait_seconds"]
        self.result = {
            "schema": 1, "ok": False, "status": "failed",
            "account": options["account"], "check_only": options["check_only"],
            "before": "", "target": "", "after": "", "version": "",
            "backup": "", "error": "", "rollback_error": "",
        }
        self.expected_mcus = []
        self.expected_beacon = False
        self.expected_restarts = "0"

    def command(self, args, timeout=30):
        try:
            process = subprocess.run(
                args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                text=True, timeout=timeout, check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as exc:
            raise UpdateError("%s did not complete" % args[0]) from exc
        if process.returncode:
            # Commands may expose remote credentials or configuration; do not
            # include their output in the public receipt.
            raise UpdateError("%s failed (exit %s)" % (args[0], process.returncode))
        return process.stdout.strip()

    def git(self, *args):
        return self.command(["git", "-C", str(self.repo), *args], timeout=120)

    def systemctl(self, *args):
        return self.command(["sudo", "-n", "systemctl", *args], timeout=30)

    def api(self, path):
        request = urllib.request.Request(
            "http://127.0.0.1:%d%s" % (self.options["moonraker_port"], path)
        )
        if self.options.get("api_key"):
            request.add_header("X-Api-Key", self.options["api_key"])
        try:
            # Ignore inherited proxy settings for the printer's loopback API.
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            with opener.open(request, timeout=5) as response:
                envelope = json.load(response)
            result = envelope["result"]
            if not isinstance(result, dict):
                raise ValueError("invalid result")
            return result
        except (OSError, ValueError, KeyError) as exc:
            raise UpdateError("Moonraker did not return valid %s evidence" % path.split("?")[0]) from exc

    def service_state(self):
        raw = self.systemctl(
            "show", "klipper.service", "-p", "ActiveState", "-p", "SubState",
            "-p", "MainPID", "-p", "NRestarts", "-p", "User",
        )
        return dict(line.split("=", 1) for line in raw.splitlines() if "=" in line)

    def configured_objects(self):
        objects = self.api("/printer/objects/list").get("objects")
        if not isinstance(objects, list) or "mcu" not in objects:
            raise UpdateError("Moonraker omitted the primary controller")
        return objects

    def baseline(self):
        info = self.api("/printer/info")
        if info.get("state") != "ready":
            raise UpdateError("Klipper must be ready before an update")
        if info.get("klipper_path") != str(self.repo) or info.get("config_file") != str(self.data / "config/printer.cfg"):
            raise UpdateError("Live Klipper paths do not match the printer account")
        objects = self.configured_objects()
        self.expected_mcus = [name for name in objects if name == "mcu" or name.startswith("mcu ")]
        self.expected_beacon = "beacon" in objects
        return info

    def idle(self):
        info = self.baseline()
        heaters = self.api("/printer/objects/query?heaters")["status"].get("heaters", {}).get("available_heaters")
        if not isinstance(heaters, list) or not heaters:
            raise UpdateError("Moonraker omitted the configured heaters")
        names = ["print_stats", "pause_resume", "idle_timeout", *heaters]
        from urllib.parse import quote
        status = self.api("/printer/objects/query?" + "&".join(quote(name, safe="") for name in names))["status"]
        if status.get("print_stats", {}).get("state") not in ("standby", "complete", "cancelled"):
            raise UpdateError("Printer is not idle")
        if status.get("pause_resume", {}).get("is_paused") is not False:
            raise UpdateError("Printer pause state is not verified idle")
        if status.get("idle_timeout", {}).get("state") not in ("Idle", "Ready"):
            raise UpdateError("Printer is moving or its idle state is unknown")
        for name in heaters:
            target = status.get(name, {}).get("target")
            if isinstance(target, bool) or not isinstance(target, (int, float)) or not math.isfinite(target) or target != 0:
                raise UpdateError("All configured heaters must have verified zero targets")
        return info

    def validate_origin(self):
        if self.git("remote", "get-url", "origin") not in (
            UPSTREAM, UPSTREAM[:-4], "git@github.com:Klipper3d/klipper.git",
            "ssh://git@github.com/Klipper3d/klipper.git",
        ):
            raise UpdateError("Klipper origin must be the official Klipper3d repository")

    def configuration_digest(self):
        files = sorted((self.data / "config").rglob("*.cfg"))
        primary = self.data / "config/printer.cfg"
        if primary not in files or primary.stat().st_size == 0:
            raise UpdateError("The installed printer configuration is empty or missing")
        digest = hashlib.sha256()
        for path in files:
            digest.update(str(path.relative_to(self.data / "config")).encode())
            digest.update(b"\0")
            digest.update(path.read_bytes())
            digest.update(b"\0")
        return digest.hexdigest()

    def prepare(self):
        if self.repo.is_symlink() or (self.data / "config").is_symlink():
            raise UpdateError("Symlinked Klipper or configuration directories are unsupported")
        self.validate_origin()
        if self.git("status", "--porcelain", "--untracked-files=no"):
            raise UpdateError("Tracked Klipper files have local changes; refusing to overwrite them")
        if self.git("symbolic-ref", "--short", "HEAD") != "master":
            raise UpdateError("Klipper must be on its mainline master branch")
        before = self.git("rev-parse", "HEAD")
        self.result.update(before=before, after=before)
        # Fetch the advertised upstream branch; freeze the target for this
        # transaction instead of trusting a previously cached tracking ref.
        self.git("fetch", "origin", "master")
        target = self.git("rev-parse", "FETCH_HEAD")
        if not COMMIT.fullmatch(before) or not COMMIT.fullmatch(target):
            raise UpdateError("Git returned an invalid revision")
        self.result["target"] = target
        self.git("merge-base", "--is-ancestor", before, target)
        # Updating a shared extension environment cannot be rolled back by
        # reverting Git alone. Refuse dependency changes rather than perform a
        # partial package update or alter Beacon/Shake&Tune dependencies.
        if not self.options["check_only"] and self.git("diff", "--name-only", before, target, "--", "scripts/klippy-requirements.txt"):
            raise UpdateError("This update changes Python dependencies; a reviewed environment upgrade is required")

    def backup(self, digest):
        root = self.data / "backups"
        root.mkdir(mode=0o700, exist_ok=True)
        backup = Path(tempfile.mkdtemp(prefix="klipper-update-", dir=root))
        with tarfile.open(backup / "config-before.tar.gz", "w:gz") as archive:
            archive.add(self.data / "config", arcname="config")
        manifest = {
            "before": self.result["before"], "target": self.result["target"],
            "configuration_sha256": digest, "account": self.options["account"],
        }
        (backup / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        self.git("branch", "rollback/" + backup.name, self.result["before"])
        # These are recovery artifacts, not a change to Klipper's save logic.
        self.command(["sync"])
        self.result["backup"] = str(backup)

    def verify(self, revision, digest):
        deadline = time.monotonic() + self.deadline
        version = self.git("describe", "--tags", "--always")
        while True:
            service = self.service_state()
            if service.get("User") != self.options["account"]:
                raise UpdateError("Klipper service owner does not match the printer account")
            if service.get("ActiveState") in ("failed", "inactive") or service.get("NRestarts") != self.expected_restarts:
                raise UpdateError("Klipper service stopped or restarted during verification")
            try:
                info = self.api("/printer/info")
            except UpdateError:
                info = {}
            if info.get("state") in ("error", "shutdown"):
                raise UpdateError("Klipper entered %s; inspect klippy.log" % info["state"])
            if service.get("ActiveState") == "active" and service.get("SubState") == "running" and info.get("state") == "ready":
                if str(info.get("process_id")) != service.get("MainPID"):
                    raise UpdateError("Moonraker and systemd disagree on the Klipper process")
                if info.get("klipper_path") != str(self.repo) or not info.get("software_version", "").removesuffix("-dirty") == version:
                    raise UpdateError("The running Klipper version does not match the checkout")
                if self.git("rev-parse", "HEAD") != revision:
                    raise UpdateError("Klipper checkout changed during verification")
                names = self.expected_mcus + (["beacon"] if self.expected_beacon else [])
                from urllib.parse import quote
                status = self.api("/printer/objects/query?" + "&".join(quote(name, safe="") for name in names))["status"]
                if any(not status.get(name, {}).get("mcu_version") for name in self.expected_mcus):
                    raise UpdateError("A configured controller did not reconnect")
                if self.expected_beacon and not status.get("beacon", {}).get("last_received_sample"):
                    raise UpdateError("Beacon did not resume reporting samples")
                tuning = Path("/usr/local/sbin/qidi-plus4-host-tuning")
                if tuning.exists():
                    self.command(["sudo", "-n", str(tuning), "audit"])
                if self.configuration_digest() != digest:
                    raise UpdateError("Printer configuration changed during the update; it was not overwritten")
                self.result.update(after=revision, version=info["software_version"])
                return
            if time.monotonic() >= deadline:
                raise UpdateError("Klipper did not finish startup within the verification timeout")
            time.sleep(1)

    def rollback(self, digest):
        # If another client has started a job, never stop that live job to
        # perform an automatic rollback. Unknown/down states from this failed
        # startup can be stopped; a ready printer must pass the full idle gate.
        try:
            info = self.api("/printer/info")
        except UpdateError:
            info = {}
        if info.get("state") == "ready":
            self.idle()
        self.systemctl("stop", "klipper.service")
        if self.git("status", "--porcelain", "--untracked-files=no"):
            raise UpdateError("Klipper source changed; automatic rollback refused")
        head = self.git("rev-parse", "HEAD")
        if head not in (self.result["before"], self.result["target"]):
            raise UpdateError("Klipper revision changed; automatic rollback refused")
        # No reset --hard: reject local changes and return only this transaction's
        # branch to its retained predecessor, preserving all extension files.
        self.git("checkout", "-B", "master", self.result["before"])
        self.result["after"] = self.result["before"]
        self.command(["sync"])
        self.expected_restarts = "0"
        self.systemctl("start", "--no-block", "klipper.service")
        self.verify(self.result["before"], digest)
        self.result["status"] = "rolled_back"

    @contextlib.contextmanager
    def lock(self):
        import fcntl
        with open(self.repo / ".git/printerlab-update.lock", "a") as lockfile:
            try:
                fcntl.flock(lockfile, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError as exc:
                raise UpdateError("Another printerlab Klipper update is running") from exc
            yield

    def run(self):
        with self.lock():
            self.prepare()
            self.baseline()
            service = self.service_state()
            if service.get("User") != self.options["account"] or service.get("ActiveState") != "active" or service.get("SubState") != "running":
                raise UpdateError("The existing Klipper service is not running under the printer account")
            digest = self.configuration_digest()
            if self.options["check_only"] or self.result["before"] == self.result["target"]:
                self.expected_restarts = service.get("NRestarts")
                if self.expected_restarts is None:
                    raise UpdateError("Klipper restart count is unavailable")
                self.verify(self.result["before"], digest)
                self.result.update(ok=True, status="current" if self.result["before"] == self.result["target"] else "available")
                return self.result
            self.idle()
            self.backup(digest)
            # Fetch/backup may have taken time. Recheck immediately before the
            # disruptive operation, and retain a stable configuration baseline.
            self.idle()
            if self.configuration_digest() != digest:
                raise UpdateError("Configuration changed while preparing; update aborted")
            if self.git("rev-parse", "HEAD") != self.result["before"] or self.git("symbolic-ref", "--short", "HEAD") != "master" or self.git("status", "--porcelain", "--untracked-files=no"):
                raise UpdateError("Klipper checkout changed while preparing; update aborted")
            try:
                self.systemctl("stop", "klipper.service")
                self.git("merge", "--ff-only", self.result["target"])
                self.result["after"] = self.result["target"]
                self.command(["sync"])
                self.systemctl("start", "--no-block", "klipper.service")
                self.verify(self.result["target"], digest)
            except BaseException as exc:
                self.result["error"] = str(exc) if isinstance(exc, UpdateError) else "Klipper update was interrupted or failed"
                # Complete recovery even if the SSH client is interrupted again.
                for name in ("SIGHUP", "SIGTERM", "SIGINT"):
                    if hasattr(signal, name):
                        signal.signal(getattr(signal, name), signal.SIG_IGN)
                try:
                    self.rollback(digest)
                except Exception as rollback_error:
                    self.result.update(status="rollback_failed", rollback_error=str(rollback_error))
                return self.result
            self.result.update(ok=True, status="updated")
            return self.result


def main(options):
    os.umask(0o077)
    update = KlipperUpdate(options)
    def interrupted(signum, frame):
        raise UpdateError("Klipper update interrupted")
    for name in ("SIGHUP", "SIGTERM", "SIGINT"):
        if hasattr(signal, name):
            signal.signal(getattr(signal, name), interrupted)
    try:
        import pwd
        if pwd.getpwuid(os.geteuid()).pw_name != options["account"] or update.home != Path("/home") / options["account"]:
            raise UpdateError("SSH helper is not running as the expected printer account")
        update.run()
    except Exception as exc:
        update.result["error"] = str(exc) if isinstance(exc, UpdateError) else "Klipper update preflight failed"
    print(json.dumps(update.result))
    return 0 if update.result["ok"] else 1


if __name__ == "__main__":
    # The native SSH backend supplies this object through stdin.
    raise SystemExit(main(globals()["PLAB_OPTIONS"]))
