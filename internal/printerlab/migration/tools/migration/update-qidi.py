#!/usr/bin/env python3
"""Reviewed stock QIDI updates. Python 3.7 compatible; streamed as root over SSH.

Preparation downloads pinned artifacts and creates recovery files. Go transfers
and verifies the recovery archive on the workstation before authorizing apply.
No automatic retry or rollback of an interrupted firmware installation.
"""

import configparser
import fcntl
import glob
import hashlib
import io
import json
import math
import os
from pathlib import Path, PurePosixPath
import pwd
import re
import select
import shutil
import stat
import subprocess
import tarfile
import tempfile
import termios
import time
import urllib.parse
import urllib.request


ROOT = Path("/")
TRANSACTIONS = Path("/var/lib/hlab/qidi-updates")
SERVICES = ("klipper.service", "moonraker.service", "makerbase-client.service", "QIDILink-client.service")


class UpdateError(Exception):
    pass


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, newurl):
        raise UpdateError("Moonraker redirect refused")


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def version_key(value):
    if not re.match(r"^\d+\.\d+\.\d+$", value):
        raise UpdateError("Unrecognized vendor firmware version")
    return tuple(int(part) for part in value.split("."))


def command(args, timeout=30):
    try:
        process = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                 universal_newlines=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise UpdateError("%s did not complete" % args[0]) from exc
    if process.returncode:
        raise UpdateError("%s exited %s; inspect the retained install log" % (args[0], process.returncode))
    return process.stdout.strip()


class Screen:
    """TJC/Nextion upload v1.1: require identification and every 4096-byte ACK.

    https://itead.cc/nextion/nextion-hmi-upload-protocol/comment-page-1/
    Failures after whmi-wri are ambiguous and must never be retried automatically.
    """
    def __init__(self, device):
        self.fd = os.open(device, os.O_RDWR | os.O_NOCTTY | os.O_NONBLOCK)
        self.original = termios.tcgetattr(self.fd)
        try:
            fcntl.ioctl(self.fd, termios.TIOCEXCL)
        except Exception:
            os.close(self.fd)
            raise

    def close(self):
        try:
            termios.tcsetattr(self.fd, termios.TCSANOW, self.original)
        finally:
            os.close(self.fd)

    def baud(self, rate):
        attrs = termios.tcgetattr(self.fd)
        attrs[0] = attrs[1] = attrs[3] = 0
        attrs[2] = termios.CS8 | termios.CREAD | termios.CLOCAL
        attrs[4] = attrs[5] = getattr(termios, "B" + str(rate))
        attrs[6][termios.VMIN] = attrs[6][termios.VTIME] = 0
        termios.tcsetattr(self.fd, termios.TCSANOW, attrs)

    def write(self, data):
        deadline = time.monotonic() + 10
        while data:
            if time.monotonic() >= deadline:
                raise UpdateError("Touchscreen write timed out; do not retry the flash")
            if select.select([], [self.fd], [], 0.2)[1]:
                try:
                    count = os.write(self.fd, data)
                except BlockingIOError:
                    continue
                if count <= 0:
                    raise UpdateError("Touchscreen write made no progress")
                data = data[count:]
        termios.tcdrain(self.fd)

    def read(self, seconds, terminator=None, read_size=4096):
        deadline, data = time.monotonic() + seconds, b""
        while time.monotonic() < deadline:
            if select.select([self.fd], [], [], min(0.1, max(0, deadline-time.monotonic())))[0]:
                data += os.read(self.fd, read_size)
                if len(data) > 8192:
                    raise UpdateError("Unexpected touchscreen response")
                if terminator and terminator in data:
                    return data
        return data

    def identify(self):
        for rate in (115200, 230400, 921600, 57600, 38400, 19200, 9600):
            self.baud(rate)
            termios.tcflush(self.fd, termios.TCIOFLUSH)
            self.write(b"\xff\xff\xffconnect\xff\xff\xff")
            # An empty instruction can generate its own terminated reply
            # before comok. Collect the whole identification response.
            response = self.read(0.5)
            if b"comok " in response:
                return rate
        raise UpdateError("Touchscreen did not identify on its UART; check screen power/cable before upgrading (or explicitly select --host-only)")

    def acknowledge(self):
        # Leave any post-upload boot packet queued after the final ACK.
        response = self.read(3, b"\x05", read_size=1)
        if response != b"\x05":
            raise UpdateError("Touchscreen did not acknowledge a firmware block; stop and use the retained recovery files")

    def upload(self, path):
        rate = self.identify()
        self.baud(rate)
        termios.tcflush(self.fd, termios.TCIOFLUSH)
        self.write(("whmi-wri %d,921600,0" % path.stat().st_size).encode("ascii") + b"\xff\xff\xff")
        self.baud(921600)
        self.acknowledge()
        with path.open("rb") as stream:
            for block in iter(lambda: stream.read(4096), b""):
                self.write(block)
                self.acknowledge()
        # The final ACK proves complete transfer, not a physical UI inspection.


class QIDIUpdate:
    def __init__(self, options):
        self.options = options
        self.release = options["release"]
        self.home = ROOT / "home" / self.release["account"]
        self.config = self.home / "printer_data/config"
        self.transaction = None
        self.result = dict(schema=1, ok=False, status="failed", model=self.release["model"],
                           target=self.release["version"], before="", after="",
                           check_only=options["check_only"], host_only=options["host_only"],
                           config_policy=options["config_policy"], screen_status="unchanged",
                           backup="", backup_sha256="", local_backup=options.get("local_backup", ""),
                           config_verified=False, startup_verified=False, error="")

    def api(self, path, data=None):
        request = urllib.request.Request("http://127.0.0.1:%d%s" % (self.options["moonraker_port"], path))
        if data is not None:
            request.data = json.dumps(data).encode("utf-8")
            request.add_header("Content-Type", "application/json")
        if self.options.get("api_key"):
            request.add_header("X-Api-Key", self.options["api_key"])
        try:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
            with opener.open(request, timeout=5) as response:
                value = json.load(response)["result"]
            if not isinstance(value, dict):
                raise ValueError()
            return value
        except (OSError, ValueError, KeyError) as exc:
            raise UpdateError("Missing Moonraker evidence for %s" % path.split("?")[0]) from exc

    def version(self):
        parser = configparser.ConfigParser()
        parser.read(str(ROOT / self.release["version_file"].lstrip("/")))
        return parser.get("version", "soc").strip().lstrip("Vv")

    def idle(self):
        info = self.api("/printer/info")
        if info.get("state") != "ready" or info.get("klipper_path") != str(self.home / "klipper") or info.get("config_file") != str(self.config / "printer.cfg"):
            raise UpdateError("Klipper must be ready with the expected vendor paths")
        heaters = self.api("/printer/objects/query?heaters").get("status", {}).get("heaters", {}).get("available_heaters")
        if not isinstance(heaters, list) or not heaters or not all(isinstance(name, str) for name in heaters):
            raise UpdateError("Configured heaters could not be verified")
        names = ["print_stats", "pause_resume", "idle_timeout"] + heaters
        status = self.api("/printer/objects/query?" + "&".join(urllib.parse.quote(name, safe="") for name in names)).get("status", {})
        if status.get("print_stats", {}).get("state") not in ("standby", "complete", "cancelled") or status.get("pause_resume", {}).get("is_paused") is not False or status.get("idle_timeout", {}).get("state") not in ("Ready", "Idle"):
            raise UpdateError("Printer must be idle, not paused or moving")
        for name in heaters:
            target = status.get(name, {}).get("target")
            if isinstance(target, bool) or not isinstance(target, (int, float)) or not math.isfinite(target) or target != 0:
                raise UpdateError("Every heater target must be verified zero")
        objects = self.api("/printer/objects/list").get("objects")
        if not isinstance(objects, list) or "mcu" not in objects:
            raise UpdateError("Missing controller inventory")
        return [name for name in objects if name == "beacon" or name == "mcu" or name.startswith("mcu ")]

    def screen_free(self):
        device = self.release["screen_device"]
        for fd in glob.glob("/proc/[0-9]*/fd/*"):
            try:
                if os.readlink(fd) == device:
                    raise UpdateError("Touchscreen UART is in use; stop its owning service before upgrading")
            except (FileNotFoundError, PermissionError):
                pass

    def preflight(self):
        if (ROOT / "dev_info.txt").read_text().strip() != self.release["device_marker"]:
            raise UpdateError("Live printer model does not match the selected release")
        if os.uname().machine != self.release["architecture"]:
            raise UpdateError("Printer CPU does not match the release")
        before = self.version()
        self.result.update(before=before, after=before)
        if not version_key(self.release["minimum_version"]) <= version_key(before) < version_key(self.release["version"]):
            raise UpdateError("Supported upgrade starts at %s and must advance to %s; reinstall/downgrade is refused" % (self.release["minimum_version"], self.release["version"]))
        if self.config.is_symlink() or (self.home / "klipper").is_symlink():
            raise UpdateError("Symlinked vendor configuration/checkout roots are unsupported")
        controllers = self.idle()
        if self.agent_active():
            status = self.api("/server/extensions/request", {"agent": "klippee", "method": "klippee_operation_status"})
            if status.get("running") is not False:
                raise UpdateError("Klippee agent must prove no configuration operation is running")
        for program in ("dpkg", "dpkg-deb", "systemctl", "tar"):
            if not shutil.which(program):
                raise UpdateError("Required vendor installer tool is missing: " + program)
        if command(["dpkg", "--audit"]):
            raise UpdateError("dpkg already has unfinished work")
        if shutil.disk_usage(str(ROOT)).free < 1024 * 1024 * 1024:
            raise UpdateError("At least 1 GiB free disk space is required")
        if not self.options["host_only"]:
            # An active vendor UI owns this port. Its service is stopped only in
            # apply; check can still verify model/version/idle without disturbing it.
            if self.service_state("makerbase-client.service") != "active":
                self.screen_free()
                screen = Screen(self.release["screen_device"])
                try:
                    screen.identify()
                finally:
                    screen.close()
            self.result["screen_status"] = "pending"
        return controllers

    def user_systemctl(self, *args):
        account = self.release["account"]
        uid = pwd.getpwnam(account).pw_uid
        return command(["runuser", "-u", account, "--", "env", "XDG_RUNTIME_DIR=/run/user/%d" % uid,
                        "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%d/bus" % uid, "systemctl", "--user"] + list(args))

    def agent_active(self):
        if not (self.home / ".config/systemd/user/klippee-moonraker.service").exists():
            return False
        return self.user_systemctl("show", "klippee-moonraker.service", "-p", "ActiveState", "--value") == "active"

    def service_state(self, name):
        return command(["systemctl", "show", name, "-p", "ActiveState", "--value"])

    def enabled_state(self, name):
        # disabled/static return nonzero; their stdout is still valid state.
        process = subprocess.run(["systemctl", "is-enabled", name], stdout=subprocess.PIPE,
                                 stderr=subprocess.DEVNULL, universal_newlines=True, timeout=10)
        return process.stdout.strip()

    def fingerprint(self, paths):
        entries = {}
        for path in paths:
            metadata = path.lstat()
            value = [stat.S_IMODE(metadata.st_mode), metadata.st_uid, metadata.st_gid]
            if path.is_symlink():
                value += ["link", os.readlink(str(path))]
            elif path.is_file():
                value += ["file", sha256(path)]
            elif path.is_dir():
                value += ["directory"]
            else:
                raise UpdateError("Unsupported file type in configuration")
            entries[str(path.relative_to(ROOT))] = value
        return entries

    def protected_paths(self):
        paths = [self.config] + list(self.config.rglob("*"))
        paths += [ROOT / name for name in self.release["preserve_paths"] if (ROOT / name).exists()]
        return sorted(set(paths))

    def download(self, artifact, name):
        target = self.transaction / name
        if self.options.get("firmware_staging"):
            directory = self.options["firmware_staging"]
            if not re.match(r"^/var/lib/hlab/qidi-payloads/[0-9a-f]{32}$", directory):
                raise UpdateError("Invalid workstation firmware staging path")
            source = ROOT / directory.lstrip("/") / name
            if source.resolve() != source or source.stat().st_uid != os.geteuid() or not source.is_file():
                raise UpdateError("Workstation firmware staging file is not root owned and regular")
            if source.stat().st_size != artifact["bytes"] or sha256(source) != artifact["sha256"]:
                raise UpdateError("Workstation firmware does not match the reviewed release")
            shutil.copyfile(str(source), str(target))
            return
        digest, count = hashlib.sha256(), 0
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(artifact["url"], timeout=30) as source, target.open("xb") as output:
            if not source.geturl().startswith("https://"):
                raise UpdateError("Firmware download redirected outside HTTPS")
            while True:
                data = source.read(1024 * 1024)
                if not data:
                    break
                count += len(data)
                if count > artifact["bytes"]:
                    raise UpdateError("Vendor payload changed; this release needs a fresh review")
                output.write(data)
                digest.update(data)
        if count != artifact["bytes"] or digest.hexdigest() != artifact["sha256"]:
            raise UpdateError("Vendor payload checksum differs from the reviewed release; installation refused")

    def package_paths(self):
        package = self.transaction / "host.deb"
        if command(["dpkg-deb", "-f", str(package), "Package"]).lower() != self.release["package_name"] or command(["dpkg-deb", "-f", str(package), "Architecture"]) != self.release["package_arch"]:
            raise UpdateError("Unexpected vendor package identity")
        process = subprocess.run(["dpkg-deb", "--fsys-tarfile", str(package)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=60)
        if process.returncode or len(process.stdout) > 512 * 1024 * 1024:
            raise UpdateError("Could not inspect vendor package")
        paths = []
        with tarfile.open(fileobj=io.BytesIO(process.stdout), mode="r:") as archive:
            for member in archive:
                relative = PurePosixPath(member.name)
                if relative.is_absolute() or ".." in relative.parts or not (member.isfile() or member.isdir()):
                    raise UpdateError("Unexpected vendor archive path or file type")
                path = ROOT.joinpath(*relative.parts)
                if member.isfile():
                    paths.append(path)
            version_member = next(member for member in archive.getmembers() if member.name.lstrip("./") == self.release["version_file"].lstrip("/"))
            embedded = archive.extractfile(version_member).read().decode("utf-8")
        if "V" + self.release["version"] not in embedded or self.release["screen_version"] not in embedded:
            raise UpdateError("Vendor embedded version does not match the requested release")
        return paths

    def write_json(self, name, value):
        target = self.transaction / name
        temporary = target.with_suffix(".tmp")
        with temporary.open("w") as stream:
            json.dump(value, stream, indent=2)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(str(temporary), str(target))

    def archive(self, target, paths):
        with tarfile.open(str(target), "w:gz", dereference=False) as archive:
            for path in sorted(set(paths)):
                archive.add(str(path), arcname=str(path.relative_to(ROOT)), recursive=False)

    def prepare(self, controllers):
        TRANSACTIONS.mkdir(mode=0o700, parents=True, exist_ok=True)
        self.transaction = Path(tempfile.mkdtemp(prefix="", dir=str(TRANSACTIONS)))
        # The Go receipt validator accepts a fixed opaque transaction ID only.
        renamed = TRANSACTIONS / os.urandom(16).hex()
        self.transaction.rename(renamed)
        self.transaction = renamed
        self.download(self.release["host"], "host.deb")
        if not self.options["host_only"]:
            self.download(self.release["screen"], "screen.tft")
        supplied = self.package_paths()
        protected = self.protected_paths()
        before = self.fingerprint(protected)
        existing = [path for path in supplied if path.exists() or path.is_symlink()]
        for pattern in self.release["removed_globs"]:
            existing += [Path(path) for path in glob.glob(str(ROOT / pattern))]
        existing += [ROOT / "var/lib/dpkg"] + list((ROOT / "var/lib/dpkg").rglob("*"))
        for path in (ROOT / "root/800_480.tft", ROOT / "root/800_480.tft.bak"):
            if path.exists():
                existing.append(path)
        self.archive(self.transaction / "config.tar.gz", protected)
        self.archive(self.transaction / "files-before.tar.gz", existing)
        manifest = dict(release=self.release, before=self.result["before"], host_only=self.options["host_only"], config_policy=self.options["config_policy"],
                        protected=before, controllers=controllers, status="prepared", created=time.time(), hostname=os.uname().nodename,
                        klipper_pid=self.api("/printer/info").get("process_id"),
                        enabled_services={name: self.enabled_state(name) for name in SERVICES},
                        supplied=[str(path.relative_to(ROOT)) for path in supplied],
                        config_sha256=sha256(self.transaction / "config.tar.gz"), files_sha256=sha256(self.transaction / "files-before.tar.gz"))
        self.write_json("manifest.json", manifest)
        with tarfile.open(str(self.transaction / "recovery.tar.gz"), "w:gz") as archive:
            for name in ("manifest.json", "config.tar.gz", "files-before.tar.gz"):
                archive.add(str(self.transaction / name), arcname=name)
        os.sync()
        self.result.update(ok=True, status="prepared", backup=str(self.transaction), backup_sha256=sha256(self.transaction / "recovery.tar.gz"))

    def inhibit(self, services):
        for name in services:
            directory = ROOT / "etc/systemd/system" / (name + ".d")
            directory.mkdir(parents=True, exist_ok=True)
            path = directory / "90-hlab-qidi-update.conf"
            content = "[Unit]\nConditionPathExists=%s/allow-service-start\n" % self.transaction
            if path.exists():
                if path.is_symlink() or path.read_text() != content:
                    raise UpdateError("An unrelated upgrade already inhibits this service")
            else:
                with path.open("x") as stream:
                    # Persistent conditions also protect a reboot after an
                    # interrupted installer, including units defined in /etc.
                    stream.write(content)
        command(["systemctl", "daemon-reload"])
        command(["systemctl", "stop"] + list(services))

    def uninhibit(self, services):
        for name in services:
            (ROOT / "etc/systemd/system" / (name + ".d/90-hlab-qidi-update.conf")).unlink()
        command(["systemctl", "daemon-reload"])

    def restore_config(self):
        command(["tar", "-xzpf", str(self.transaction / "config.tar.gz"), "--same-owner", "-C", str(ROOT)], timeout=60)

    def verify(self, manifest):
        deadline = time.monotonic() + self.options["wait_seconds"]
        failure = "Klipper did not become ready"
        while time.monotonic() < deadline:
            try:
                self.idle()
                info = self.api("/printer/info")
                service = dict(line.split("=", 1) for line in command(["systemctl", "show", "klipper.service", "-p", "MainPID", "-p", "ActiveState", "-p", "User"]).splitlines() if "=" in line)
                if service.get("ActiveState") != "active" or service.get("User") != self.release["account"] or str(info.get("process_id")) != service.get("MainPID") or not info.get("process_id") or info["process_id"] == manifest["klipper_pid"]:
                    raise UpdateError("A new Klipper process with the expected owner was not verified")
                status = self.api("/printer/objects/query?" + "&".join(urllib.parse.quote(name, safe="") for name in manifest["controllers"])).get("status", {})
                for name in manifest["controllers"]:
                    if not status.get(name, {}).get("last_received_sample" if name == "beacon" else "mcu_version"):
                        raise UpdateError("A configured controller has not reconnected")
                if self.version() != self.release["version"]:
                    raise UpdateError("Installed version did not advance")
                if self.options["config_policy"] == "preserve":
                    actual = self.fingerprint([ROOT / name for name in manifest["protected"]])
                    if actual != manifest["protected"]:
                        raise UpdateError("Preserved configuration changed during startup")
                self.result.update(config_verified=True, startup_verified=True, after=self.version())
                return
            except UpdateError as exc:
                failure = str(exc)
                time.sleep(1)
        raise UpdateError("Startup verification failed: %s. Services remain stopped for recovery; inspect klippy.log and install.log" % failure)

    def apply(self):
        path = self.options["backup"]
        if not re.match(r"^/var/lib/hlab/qidi-updates/[0-9a-f]{32}$", path):
            raise UpdateError("Invalid prepared transaction path")
        self.transaction = ROOT / path.lstrip("/")
        # main requires euid 0. Comparing with euid also permits unprivileged
        # filesystem fixtures to exercise the complete transaction in tests.
        if self.transaction.resolve() != self.transaction or self.transaction.stat().st_uid != os.geteuid() or stat.S_IMODE(self.transaction.stat().st_mode) != 0o700:
            raise UpdateError("Prepared transaction is not private and root owned")
        manifest = json.loads((self.transaction / "manifest.json").read_text())
        self.result.update(backup=path, backup_sha256=self.options["backup_sha256"], before=manifest["before"], after=manifest["before"])
        if manifest["status"] != "prepared" or time.time()-manifest["created"] > 3600 or manifest["release"] != self.release or manifest["host_only"] != self.options["host_only"] or manifest["config_policy"] != self.options["config_policy"]:
            raise UpdateError("Prepared transaction expired, changed or already started; do not retry an interrupted installation")
        for name, digest in (("recovery.tar.gz", self.options["backup_sha256"]), ("config.tar.gz", manifest["config_sha256"]), ("files-before.tar.gz", manifest["files_sha256"]), ("host.deb", self.release["host"]["sha256"])):
            if sha256(self.transaction / name) != digest:
                raise UpdateError("Prepared recovery or firmware file changed")
        if not self.options["host_only"] and sha256(self.transaction / "screen.tft") != self.release["screen"]["sha256"]:
            raise UpdateError("Prepared screen image changed")
        self.preflight()
        if self.fingerprint(self.protected_paths()) != manifest["protected"]:
            raise UpdateError("Configuration changed after backup; prepare a fresh transaction")
        services = [name for name in SERVICES if command(["systemctl", "show", name, "-p", "LoadState", "--value"]) != "not-found"]
        active = [name for name in services if self.service_state(name) == "active"]
        agent_active = self.agent_active()
        if any((ROOT / "etc/systemd/system" / (name + ".d/90-hlab-qidi-update.conf")).exists() for name in services):
            raise UpdateError("An earlier upgrade still inhibits services; inspect its recovery journal")
        manifest.update(status="installing", active_services=active, inhibited_services=services, agent_active=agent_active)
        self.write_json("manifest.json", manifest)
        # Any error beyond this intent marker is a recovery case, never a retry.
        self.result["status"] = "recovery_required"
        install_started = False
        try:
            if agent_active:
                self.user_systemctl("stop", "klippee-moonraker.service")
            # Close the gate again after stopping the configuration agent.
            self.idle()
            self.inhibit(services)
            if not self.options["host_only"]:
                self.screen_free()
                screen = Screen(self.release["screen_device"])
                try:
                    screen.identify()
                finally:
                    screen.close()
            install_started = True
            with (self.transaction / "install.log").open("wb") as log:
                process = subprocess.run(["dpkg", "-i", "--force-overwrite", str(self.transaction / "host.deb")], stdout=log, stderr=subprocess.STDOUT, timeout=300,
                                         env=dict(os.environ, DEBIAN_FRONTEND="noninteractive"))
            if process.returncode:
                raise UpdateError("Vendor installer failed; inspect install.log")
            if self.options["config_policy"] == "preserve":
                self.restore_config()
            # Keep the previous connection-service policy; the vendor scripts
            # may enable or disable QIDILink as an installation side effect.
            for name, state in manifest["enabled_services"].items():
                if state in ("enabled", "disabled"):
                    command(["systemctl", "enable" if state == "enabled" else "disable", name])
            if not self.options["host_only"]:
                self.result["screen_status"] = "transfer_started"
                manifest["status"] = "screen_transfer_started"
                self.write_json("manifest.json", manifest)
                screen = Screen(self.release["screen_device"])
                try:
                    screen.upload(self.transaction / "screen.tft")
                finally:
                    screen.close()
                self.result["screen_status"] = "acknowledged"
            self.uninhibit(services)
            command(["systemctl", "start"] + active)
            self.verify(manifest)
            if agent_active:
                self.user_systemctl("start", "klippee-moonraker.service")
                if not self.agent_active():
                    raise UpdateError("Klippee agent did not restart")
            manifest["status"] = "updated"
            self.write_json("manifest.json", manifest)
            self.result.update(ok=True, status="updated")
        except Exception:
            if not install_started:
                # No firmware was touched. Undo the reversible service pause
                # if a second idle/screen check fails before invoking dpkg.
                self.uninhibit([name for name in services if (ROOT / "etc/systemd/system" / (name + ".d/90-hlab-qidi-update.conf")).exists()])
                if active:
                    command(["systemctl", "start"] + active)
                if agent_active:
                    self.user_systemctl("start", "klippee-moonraker.service")
                manifest["status"] = "aborted_before_install"
                self.write_json("manifest.json", manifest)
                self.result["status"] = "failed"
                raise
            # Prevent a failed configuration or partial update from starting
            # hardware. Preserve the journal, package and screen image for recovery.
            self.inhibit(services)
            if self.options["config_policy"] == "preserve":
                self.restore_config()
            manifest["status"] = "recovery_required"
            self.write_json("manifest.json", manifest)
            raise

    def run(self):
        if self.options["phase"] == "apply":
            self.apply()
        else:
            controllers = self.preflight()
            if self.options["phase"] == "check":
                self.result.update(ok=True, status="checked")
            else:
                self.prepare(controllers)
        return self.result


def main(options):
    os.umask(0o077)
    update = QIDIUpdate(options)
    lock = None
    try:
        if os.geteuid() != 0:
            raise UpdateError("Vendor firmware upgrades require root via sudo")
        # Do not retain credentials in a plan, archive or command line.
        lock = open("/run/lock/hlab-qidi-firmware.lock", "a")
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        update.run()
    except Exception as exc:
        update.result["error"] = str(exc) if isinstance(exc, UpdateError) else "QIDI upgrade failed (%s); inspect the retained transaction" % type(exc).__name__
        if options["phase"] == "prepare" and update.transaction is not None and update.transaction.parent == TRANSACTIONS and re.match(r"^[0-9a-f]{32}$", update.transaction.name):
            # Preparation has not modified firmware or supplied a backup to Go.
            shutil.rmtree(str(update.transaction))
    finally:
        if lock is not None:
            lock.close()
    print(json.dumps(update.result))
    return 0 if update.result["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main(globals()["PLAB_OPTIONS"]))
