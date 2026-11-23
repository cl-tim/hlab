# Stock QIDI firmware updates and recovery

## Updating stock QIDI firmware

```powershell
hlab printer firmware releases                       # works without an inventory
hlab printer firmware releases plus4 -o json          # versions, URLs and pinned SHA-256 values
hlab printer firmware update --target printer.local --check
hlab printer firmware update --target printer.local -y
hlab printer firmware update --target 192.0.2.20 --version 2.1.1 --user mks --identity ~/.ssh/printer -y
hlab printer firmware update --target printer.local --firmware-dir ./downloads -y
hlab printer firmware update printer-a --version 2.1.1 --check
hlab printer firmware update printer-a --version 2.1.1 -y
hlab printer firmware update printer-a --version 2.1.1 --firmware-dir ./downloads -y
```

**Standalone use:** `--target HOST` accepts one hostname or IPv4 address and
bypasses inventory entirely, including any `--config` or configuration-file
environment settings. It never enrolls a printer, saves an inventory entry,
creates a management account or installs an SSH key. Existing hlab installations
can use the same mode without inheriting connection settings from saved devices.

Without `--version`, direct targets select the newest release in hlab's reviewed
catalogue. The model is inferred only when that selection has one model; use
`--model plus4` if multiple models match. The printer's live device identity
must still match the selected profile before installation. `--version 2.1.1`
selects that reviewed version explicitly. There is no unreviewed `latest` URL.

#### Direct connection help

Start with `ssh mks@192.168.1.50` and `sudo -n true` as shown in the
[printer guide](../guides/qidi-plus4.md#3-check-ssh-access). Replace the example address
with your printer's IP. You need its SSH login details; hlab does not create
an account or change access permissions in direct mode.

| Problem | What to do |
| --- | --- |
| `ssh` is not found | Install the OpenSSH client for your computer, then try again. |
| Connection refused or timed out | Check the printer's power, IP address and network connection, and that SSH is available. |
| Permission denied | Check the SSH username, password or private key. |
| Host key verification failed | Confirm the printer's identity before correcting its entry in your SSH known-hosts file. |
| `sudo: a password is required` | Use a printer account configured for root sudo without a password. The firmware commands cannot grant that access. |

Direct targets default to the profile's printer account (`mks` for Plus4), SSH
port 22 and loopback Moonraker port 7125. Use `--user`, `--ssh-port`, `--identity`,
`--moonraker-port` and `--api-key-env` to supply connection settings. SSH uses
normal OpenSSH authentication and host-key handling; existing working SSH access
is required. A different SSH management account can be used with `--user`; the
Klipper installation owner remains the account required by the release profile.
Local tool caches and verified recovery backups are still kept under `PLAB_HOME`.

For example, if your existing login is `mylogin` and you use a private key:

```sh
./hlab printer firmware update --target 192.168.1.50 --user mylogin --identity ~/.ssh/printer_key --check
```

Replace the login and key path with your own. Include the same connection
options when running the update with `-y` and when running `firmware fixes`.
With password authentication, OpenSSH may prompt again for each connection;
the password is not saved in an hlab inventory.

#### Saved printers

**Saved printers:** omit `--target` and supply a saved printer name. Its inventory
must declare `firmware: stock` and the matching model; `--version` is required.
Connection overrides above require `--target` and are rejected for saved names.
A misspelled saved name never becomes a direct network target automatically.

Both modes require noninteractive root sudo, ready/idle Klipper, every heater
target at zero, and at least 1 GiB free disk. This is independent of Klippee:
an ordinary QIDI installation does not need a Klippee account or workspace.
If a Klippee agent is installed, it must prove no operation is running; hlab
stops it during installation and restores it afterwards.

`--check` reads live prerequisites without downloading/installing firmware or
restarting services. If the active vendor UI owns the touchscreen port, its
identification check is deferred until the approved installation pauses that
service. A failed check before invoking the installer restores service state.

The default updates **both the Linux firmware and stock touchscreen**. It
does not directly flash the mainboard, toolhead or Box MCU. QIDI's normal Box
firmware detection resumes with Klipper. `--host-only` explicitly leaves the
physical touchscreen unchanged. A connected touchscreen must identify before
any firmware is installed, and acknowledge every block of its transfer.
The final acknowledgement proves delivery, rather than a visual inspection of
the panel. Screen power/cable faults stop the operation; there is no automatic
fallback to a Linux-only update.

`--config-policy preserve` is the default: restore existing configuration,
calibration, saved variables, Box templates and installed PLR script before
restarting services, then verify their original contents and metadata. This
also preserves customized machines. `--config-policy vendor` explicitly accepts
the release's configuration defaults; recalibration may be necessary. Files
not supplied by the update remain in place under either policy. New vendor
files such as material profiles are retained. Existing local Python extensions
outside the vendor replacement paths remain installed. A successful startup
does not certify every custom macro's runtime compatibility with new APIs.

Before installation, hlab downloads the exact reviewed payloads over HTTPS,
checks their lengths and SHA-256 hashes, and checks the embedded version and
package identity. QIDI's Debian package version is not the firmware version.
Moving OTA URLs are never treated as `latest`: if their contents change, the
update refuses installation until a new release profile is reviewed.

#### Offline updates

For a printer without internet access, download the exact catalogue payloads
on your PC and pass `--firmware-dir DIRECTORY`. Keep their vendor filenames
(for Plus4: `QD_Plus4_SOC` and `QD_Plus4_UI`). hlab verifies both files locally,
transfers them over SSH, verifies the staged bytes on the printer and verifies
them again when preparing the transaction. No printer-side network access is
needed in this mode. `--check --firmware-dir DIRECTORY` also validates the local
files without transferring or installing them.

1. On the computer, run `./hlab printer firmware releases plus4 -o json` to
   see the reviewed firmware's `host.url` and `screen.url`.
2. Download both URLs into one folder, keeping the filenames `QD_Plus4_SOC`
   and `QD_Plus4_UI`.
3. Pass that folder to both the check and update commands. For example, for a
   folder named `firmware` beside the hlab program:

   ```sh
   ./hlab printer firmware update --target 192.168.1.50 --firmware-dir ./firmware --check
   ./hlab printer firmware update --target 192.168.1.50 --firmware-dir ./firmware -y
   ```

If hlab reports that the files do not match the reviewed release, stop. The
vendor download may have changed; a similarly named file is not sufficient.

#### Backup locations

Both firmware updates and fixes print the exact backup paths. By default,
computer copies are saved under:

| Computer | Backup folder |
| --- | --- |
| Windows | `%APPDATA%\plab\backups` |
| Mac | `~/Library/Application Support/plab/backups` |
| Linux | `~/.config/plab/backups` (or `$XDG_CONFIG_HOME/plab/backups` if set) |

`qidi-firmware` contains update backups; `qidi-fixes` contains fix backups.
Keep these files private. If `PLAB_HOME` is set, use its `backups` folder instead.

Recovery data is created on the printer under
`/var/lib/hlab/qidi-updates/<transaction>/`. A recovery archive containing the
previous configuration, files overwritten/removed by the installer, dpkg
metadata and a manifest is then transferred to
`%AppData%\plab\backups\qidi-firmware\<transaction>\recovery.tar.gz`
(`PLAB_HOME` relocates this). Its hash must match on both machines **before**
installation can start. These archives can include private configuration;
retain them as recovery assets. Preparation expires after one hour, and changed
configuration requires a new backup.

The vendor installer is prevented from starting Klipper or the screen service
while defaults are temporarily in place. Persistent systemd conditions also
protect an unexpected reboot during installation. Prior service enablement and
active state are restored after installation; an intentionally disabled vendor
screen controller stays disabled. Startup verification requires ready/idle
Klipper, every previously configured controller and Beacon (if present), the
target firmware version and the selected configuration policy.

#### Recovery

**If applying fixes fails:** Klipper is left stopped after an activation failure.
Keep the output and use the exact recovery transaction path with the
[undo command](stock-fixes.md#undo-fixes). If rollback refuses because files or firmware have
changed, retain the backups and get help before making further changes.

**If a firmware update fails:** keep the output, the desktop recovery archive
and the printer transaction. Do not retry the update, remove service blockers
or restart services until the failure has been reviewed. A disconnected or
interrupted touchscreen may require QIDI's physical recovery procedure. Open
an [issue](https://github.com/cl-tim/hlab/issues) with the model, firmware
version and error text; do not upload private backups or credentials.

**A failed/interrupted firmware installation is never automatically retried or rolled
back.** The result reports `recovery_required`, the recovery archive location
and the error. Preserve the transaction's `manifest.json` and `install.log`.
Service inhibition is recorded in
`/etc/systemd/system/<service>.d/90-hlab-qidi-update.conf`; do not remove it or
restart the printer until the failed firmware/configuration has been reviewed.
The desktop archive includes `files-before.tar.gz`, `config.tar.gz` and the
original manifest. Restore only the affected vendor files/configuration after
review; the dpkg metadata is recovery evidence, not an instruction to replace
the entire live package database. Interrupted screen flashing can require
physical recovery using QIDI's update procedure.

Adding support for another QIDI model or release means reviewing its official
payloads, installer effects, device identity, version rules, preservation paths
and touchscreen transport, then adding a profile to
`migration/tools/migration/qidi-releases.json` and the corresponding tests.
An arbitrary URL or matching firmware version alone is insufficient evidence
that a package fits a different QIDI model.
