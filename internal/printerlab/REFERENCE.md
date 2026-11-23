# Printer command reference

For a QIDI firmware update or fixes without enrollment or Klippee, start with
the [step-by-step printer guide](README.md). This page covers saved printers,
fleet management, Klippee integration and technical details.

- [Saved printers and configuration](#configure)
- [Status and discovery](#discover-and-inspect)
- [Klippee](#klippee-workspaces)
- [Firmware migration](#firmware)
- [Stock QIDI fixes and rollback](#stock-qidi-fixes)
- [Stock QIDI updates, offline files and recovery](#updating-stock-qidi-firmware)

`hlab printer` manages a fleet of Klipper printers through Moonraker: discovery,
status and health audits, a record of each printer's parts, Klippee
configuration workspaces, and QIDI Plus4 firmware migration and updates.

Any Moonraker printer can be inventoried and monitored. Stock QIDI upgrades use
an embedded catalogue of reviewed model/release profiles. The initial profile
is Plus4 1.8.2 → 2.1.1; unreviewed models/releases are refused. Mainline flashing
currently supports the Plus4.

The printer commands ship in the same binary as the rest of hlab — see the
[install instructions](../../README.md#install). Every command has `--help`.

## Check your download

Download `SHA256SUMS` from the same release as the program. Run the command
for your computer in the download folder, then compare the printed hash with
the line for the **original download filename** in `SHA256SUMS`. Renaming the
program does not change its hash.

| Computer | Command after renaming the program |
| --- | --- |
| Windows PowerShell | `Get-FileHash ./hlab.exe -Algorithm SHA256` |
| Mac | `shasum -a 256 ./hlab` |
| Linux | `sha256sum ./hlab` |

The hashes must match; letter case does not matter. If they differ, download
the program and checksum file again from the same release before using it.

## Configure

Printers live in hlab's `config.yaml`, under a `printerlab:` section beside the
servers:

```powershell
hlab printer config init             # add the section to the hlab config
hlab printer config path             # show which file holds the inventory
hlab printer discover scan --emit    # print what is on the network as a printerlab: section
```

```yaml
hosts:
  - name: nas-01
    address: 192.0.2.10

printerlab:
  defaults:
    user: mks
    moonrakerPort: 7125
    klippee:
      executable: klippee
      workspaceRoot: C:\src\printer-workspaces

  printers:
    - name: printer-a
      address: stock-plus4.example
      model: plus4
      firmware: stock
      tags: [plus4, stock]

    - name: printer-b
      address: mainline-plus4.example
      model: plus4
      firmware: mainline
      tags: [plus4, mainline]

  groups:
    plus4: [printer-a, printer-b]
```

The section has its own `defaults` and `groups`, separate from the host ones:
the account hlab uses on a server is not the one it uses on a printer, and
`hlab run all` never reaches a printer.

- **Firmware** is `stock`, `mainline` or `auto`. Discovery records `auto`,
  because Moonraker alone cannot prove which is installed; set it yourself.
- **API keys** never go in the inventory. Set `apiKeyEnv` to the name of an
  environment variable that holds the key.
- **Journals, backups and build artifacts** are kept under `%AppData%\plab`.
  `PLAB_HOME` relocates them.
- **A standalone printer file** — `defaults`, `printers` and `groups` at the
  top level — is still read and edited when named with `--config` or
  `PLAB_CONFIG`. The working directory is never searched.
- **An inventory from before the merge**, at `%AppData%\plab\config.yaml`,
  keeps working and says so on each command. `hlab printer config migrate`
  moves it into the hlab config, comments included, backs the hlab config up
  first, and keeps the old file as `config.yaml.migrated`.

### Add a Voron

For a Voron running Klipper and Moonraker, add an entry to `printerlab.printers`.
Set `model` to your build's model and add the `voron` tag for fleet selection.
Use `firmware: mainline` for upstream Klipper or `firmware: auto` if unsure.

```yaml
- name: workshop-voron
  address: voron.example
  model: voron-2.4
  firmware: mainline
  tags: [voron]
  # apiKeyEnv: VORON_MOONRAKER_API_KEY
```

The default Moonraker port is `7125`. Set `moonrakerPort` for a different
direct port, or `moonrakerURL` to the API base URL when using a reverse proxy.
Set `apiKeyEnv` if the endpoint requires a Moonraker API key. For SSH commands,
set `user` and `printerUser` to the login and Klipper owner on the Voron host.

```powershell
hlab printer printers '#voron'
hlab printer status workshop-voron
hlab printer audit '#voron'
hlab printer components capture workshop-voron --dry-run
```

`discover scan` finds reachable Moonraker endpoints. With `--emit`, fill in
the model and tags after reviewing the generated entry. Klippee commands use
the same workspace setup described below. Printer-side Klippee operations
such as `printer-check` and `deploy` require a configured Klippee agent.
The `firmware` commands, `refresh-agent`, `upgrade-agent`, and QIDI Box
operations are specific to the QIDI Plus4.

## Discover and inspect

```powershell
hlab printer discover scan                  # the local subnet
hlab printer discover scan 192.0.2.0/24     # or the ranges you name
hlab printer printers all
hlab printer status '#mainline'
hlab printer audit '@plus4,!#maintenance'
hlab printer -o json status all
```

Selectors are the same as for hosts: `all`, `@group`, `#tag`, a name or glob,
and `!` to subtract. With no CIDR, discovery scans the subnet of the interface
carrying the default IPv4 route.

## Components

Record the physical parts fitted to any printer, and capture the hardware its
Klipper configuration declares:

```powershell
hlab printer components                           # saved inventory, works offline
hlab printer components set example-voron hotend-upgrade --type hotend --model "Example hotend" --status spare
hlab printer components set example-voron hotend-upgrade --status installed
hlab printer components remove example-voron hotend-upgrade
hlab printer components capture all               # add --dry-run to preview
```

`set` needs `--type` and `--model` for a new record, and an exact printer name.
Status is `installed` (the default), `planned`, `spare` or `removed`; use
`removed` rather than `remove` to keep a record of an old part.

`capture` reads the configuration Klipper last started with, using Moonraker
GET requests only, and saves the controllers, steppers, probes, heaters, fans
and sensors it declares. That is evidence, not identity: a configured sensor
type does not tell you which hotend is fitted, so record that with `set`.
A capture never changes your records, replaces the previous snapshot, and
saves nothing if any selected printer fails.

## Job history and Spoolman

```powershell
hlab printer jobs history '#voron' --since 7d --limit 25
hlab printer jobs history all -o json
hlab printer spoolman status all
```

History reads Moonraker's most recent jobs, up to 50 per printer by default.
`--since` filters by job start time. The report includes job status, print time,
filament used in millimetres, and all spool IDs Moonraker recorded for each job.
Jobs without recorded spool IDs have an empty spool column.

Configure `[spoolman]` on each Moonraker instance to enable spool tracking.
`spoolman status` shows the current spool ID, connection state, and queued
usage reports. A mixed fleet can include printers where the endpoint is
unavailable. History and Spoolman use the printer's existing Moonraker URL
and authentication. See [Moonraker's Spoolman documentation](https://moonraker.readthedocs.io/en/latest/external_api/integrations/#spoolman).

## Component maintenance

```powershell
hlab printer components set workshop-voron nozzle --type nozzle --model "0.4 mm brass"
hlab printer maintenance set workshop-voron nozzle --interval-hours 100
hlab printer maintenance record workshop-voron nozzle
hlab printer maintenance due '#voron'
```

Set an interval on an existing component. After servicing or replacing the
part, run `record` between jobs to save the service timestamp and the current
Moonraker print-time total in the local inventory. `due` checks installed parts
with an interval and uses print time accrued since each part was serviced.
An interval without a baseline is `unrecorded`. An unavailable or reduced
counter produces `unknown`. Re-record service after servicing the part if the
Moonraker history totals have been reset. Print time comes from finalized jobs.

## Klippee workspaces

Klippee stores cloud configuration. hlab implements target identity, desired
state comparison, backups and capture in Go, and maps each printer to a local
workspace folder. No separately installed client is needed. The
[native Klippee guide](../../docs/klippee.md) also covers standalone folders.

`defaults.klippee.workspaceRoot` gives each printer `<root>\<printer>`;
`printers[].klippee.workspace` overrides it for one printer, for example to
point at its profile in a Git checkout. Two printers may not share a workspace,
and `.klippee/` must stay out of Git — it holds the repository ID and token.

Initialise each folder with the desktop setup command Klippee generates. hlab
requires that folder's own `.klippee\config.json` and refuses Klippee's global
connection fallback, which can silently select the wrong printer.

**On your desktop** — these never claim the printer changed:

```powershell
hlab printer klippee path printer-a
hlab printer klippee target printer-a           # verify service, target and workspace
hlab printer klippee check printer-a --diff     # workspace vs desired configuration
hlab printer klippee apply printer-a            # bring desired configuration into the folder
hlab printer klippee capture printer-a --note "Edited in VS Code"
hlab printer klippee variable-set printer-a beacon_serial /dev/serial/by-id/usb-Beacon_...
```

Variables live only in the ignored `klippee_variables.cfg`; committed files
keep `${KLIPPEE:name}` placeholders, so device serials are never hardcoded.
`variable-list`, `variable-unset` and `variable-doctor` complete the set.

**On the printer** — through its Klippee agent, and only while it is idle:

```powershell
hlab printer klippee printer-check printer-a    # read-only drift check
hlab printer klippee deploy printer-a --wait    # apply desired configuration
hlab printer klippee restart printer-a -y       # reload Klipper's configuration
hlab printer klippee printer-capture printer-a -y   # make the printer's config the desired state
```

- **Without `--wait`, a successful `deploy` means accepted, not completed.**
  `--wait` follows that exact operation for up to four minutes
  (`--wait-timeout`) and fails on anything but confirmed success.
  `operation-status` reads a result later.
- **A deploy never restarts Klipper.** Use `restart` for a configuration
  reload, `firmware-restart` to reinitialise the MCUs, and `service-restart`
  when installed Python modules must be re-imported — a G-code `RESTART` keeps
  the old module loaded.
- **Nothing is retried or fallen back on.** A timeout is ambiguous, and
  retrying could run the same change twice. `--legacy` selects an older
  agent's methods and is only ever chosen by you.
- **`printer-variable-set`** sets a variable in the printer's own workspace and
  returns a hash of the value, never the value.

**Agent maintenance and diagnosis:**

| Command | Purpose |
| --- | --- |
| `diagnostics` | Upload logs and print a private link, valid 48 hours. `--ssh` if the agent is stuck; `--previous` for the log rotated away by a power cycle |
| `printer-diff`, `agent-events` | Read-only diff on the printer; recent agent messages |
| `upgrade-agent -y` | Upgrade the printer's Klippee client and agent from the official source |
| `refresh-agent -y` | Restart the agent when it rejects work on a stale `paused` state |
| `deploy --ssh-force -y` | Fixed `klippee pull --force`, after you have reviewed the physical diff |

`upgrade-agent` and `refresh-agent` first prove no agent operation is running.
`--recover-unresponsive` lets them rely on Moonraker's idle state instead, for
an agent that cannot answer.

### QIDI Box

| Command | Purpose |
| --- | --- |
| `box-state` | Read the slot and sensor state |
| `box-guard-status` | Verify the watchdog and output-clock mitigations are active |
| `box-reset-reason` | Read why the Box MCU reset |
| `box-auto-insert-disable`, `-enable` | Turn automatic filament insertion off or on |
| `install-box-output-clock-guard -y` | Install the guard module; it does not activate it |
| `harden-box-motion-template -y` | Switch the Box motion sensor to polling in the stock template |
| `restore-box-motion-template -y` | Put QIDI's IRQ and debounce settings back |

- **Read the reset reason before recovering.** After a Box communication
  failure, run `box-reset-reason` before any firmware restart or power cycle,
  which would replace the evidence. The result is always marked possibly stale,
  because the flags stay set until firmware clears them.
- **Installing the guard changes no configuration.** To activate it, add
  `[box_output_clock_guard]` through the normal workspace, capture and deploy
  flow, then run `service-restart` while idle.
- **The template commands change only the stock `cfg_with_N` template** that
  QIDI firmware regenerates `box.cfg` from. Follow with a deploy and restart.
  `box-guard-status` reports the vendor IRQ mode as unsafe, so a restored
  template stays visible.

## Firmware

[QIDI_MAINLINE_KLIPPER.md](QIDI_MAINLINE_KLIPPER.md) is the complete Plus4
migration guide: workstation setup, SSH access, backups, builds, microSD and
eMMC flashing, toolhead flashing and commissioning.

```powershell
hlab printer firmware prepare printer-a     # set up, back up stock, build, generate config
hlab printer firmware migrate printer-a     # guided flashing, resumable
hlab printer firmware status printer-a
hlab printer firmware write-emmc            # write and verify Armbian on a spare eMMC
```

Individual stages and recovery tools are under `firmware advanced`;
`firmware advanced paths PRINTER` shows where the tools, artifacts and backups
for a printer live. [migration/README.md](migration/README.md) covers the
bundled assets and their provenance.

### Updating mainline Klipper

```powershell
hlab printer firmware update printer-a --check   # report only; changes nothing
hlab printer firmware update printer-a -y
```

An update needs a ready, idle printer with **every heater target at zero**,
chamber included. It refuses stock or `auto` printers, non-upstream origins,
other branches, diverged history, local edits and changed Python requirements.

Before stopping Klipper it archives the configuration under
`printer_data/backups/klipper-update-*` and keeps the previous revision in a
rollback branch. Afterwards the running process must match the checkout, every
controller must reconnect, and the host-tuning audit must pass, within three
minutes (`--wait-timeout`). A failed startup rolls the host source back; the
command still exits with failure and reports whether recovery worked.

It updates the Klipper host only. It does not flash controllers, update
Moonraker or extensions, or touch `SAVE_CONFIG`.

### Stock QIDI fixes

Apply the reviewed fixes independently of an upgrade, including after installing
2.1.1 with QIDI's own updater. No inventory, enrollment or Klippee agent is
required. The same `--target`, `--model`, `--user`, `--identity`, `--ssh-port`,
`--moonraker-port` and `--api-key-env` options as the updater are supported.
Alternatively supply a saved printer declaring `firmware: stock` and model
`plus4`. This command checks the **installed** version; it does not flash firmware.

```powershell
hlab printer firmware fixes --target printer.local --check
hlab printer firmware fixes --target printer.local -y
# Optional watchdog containment, in both active configuration and Box templates:
hlab printer firmware fixes --target printer.local --box-motion-polling --check
hlab printer firmware fixes --target printer.local --box-motion-polling -y
# Optional startup optimization and persistent USB evidence:
hlab printer firmware fixes --target printer.local --trim-runout-waits --usb-event-log --check
hlab printer firmware fixes --target printer.local --trim-runout-waits --usb-event-log -y
hlab printer firmware fixes --target printer.local --fast-probing --box-drying --check
hlab printer firmware fixes --target printer.local --fast-probing --box-drying -y
```

| Fix | Stock 1.8.2 | Stock 2.1.1 |
| --- | --- | --- |
| Box output-clock guard | Install and activate | Install and activate; live review confirmed the defect remains |
| Hardened power-loss recovery | Install manifest-bound runtime and checkpoint patch | Same; the vendor still supplies the legacy recovery builder |
| Journal size/retention limits and archive cleanup timer | Apply to detected small Armbian ramlog layout | Same layout check; skip with a note on other layouts |
| Box `is_tool_change` status shim | Install and activate | Preserve an existing shim; do not add one because continued need is unproven |
| Box motion-sensor polling | Optional `--box-motion-polling` | Optional mitigation for the unchanged Box IRQ path; a watchdog failure was not reproduced on 2.1.1 |
| Remove runout-flag delays | Optional `--trim-runout-waits`; match only the reviewed PRINT_START loop | Same |
| Persistent USB evidence | Optional `--usb-event-log`; bounded root-managed event log | Same |
| Tilt/mesh speed | Optional `--fast-probing`; reviewed 150/200 mm/s settings become 300 mm/s | Same; includes Beacon scan speed |
| Faster durable variable saves | Optional `--fast-variable-saves`; exact reviewed QIDI source fingerprints required | Same |
| Timed Box drying | Optional `--box-drying`; requires an active Box and valid QIDI filament limits | Same |

The clock guard also quarantines **automatic RFID insertion** unless the existing
saved variable `qidi_box_auto_insert=1` explicitly enables it. Manual loading is
preserved. Polling keeps the motion sensor enabled, sets `use_irq: False` and
removes the IRQ-only debounce option in active Box sections and existing
`cfg_with_1` through `cfg_with_4` templates. It does not flash or repair MCU
interrupt code, and neither guard fixes USB/power faults. No loading, motion,
heating or power-loss recovery operation is run to test installation.

`--trim-runout-waits` removes the `G4 P100` inside the recognized sixteen-flag
`PRINT_START` loop, retaining every synchronous `SAVE_VARIABLE`. It preserves
other waits and customized Beacon workflows. Missing or unfamiliar loops are
skipped with a note; already-trimmed loops remain unchanged. It edits the active
include graph, not QIDI's reset templates. Keep this change in the configuration
source of truth too, and check again after a vendor configuration reset.

`--usb-event-log` installs a root-only Unix datagram socket, a udev sender and a
socket-activated writer. The sender does no storage writes; the writer fsyncs
each record and retains two files of at most 256 KiB each in
`/var/lib/hlab-usb-events/`. Records contain action, kernel port, vendor/product
IDs, boot UUID, Unix time and monotonic time, without USB serial numbers.
Service readiness is acknowledged only after the first record is persisted.
Klippee CLI 2026.11.23 or later includes these files in diagnostics. A missing
event is inconclusive: the queue can overflow and an event cannot be recorded
after host power has disappeared. Check mode does not create the socket or log.
Rollback restores service policy and installed files while retaining collected
evidence. This host option currently shares the stock-QIDI 1.8.2/2.1.1 gate;
it does not install on mainline firmware through this command.

After code installation, hardware acceptance remains a supervised Box print
start and a controlled idle USB/reboot test. Do not disconnect a controller
during a print to validate logging.

The command requires existing SSH access, noninteractive root sudo, the stock
`mks` installation layout and ready/idle Klipper with all heater targets at zero.
Only **Plus4 1.8.2 and 2.1.1** are accepted. Configuration includes are preserved;
add-on symlinks can be read only within the printer home. Unsafe paths, duplicate
sections, unexpected PLR source or a nonstandard recovery command are refused.
An existing Klippee agent must prove quiescent and is stopped during edits.
If another tool manages printer configuration, retain the added guard sections
and optional polling settings in that tool's desired configuration as well.

`--check` reads the live layout, firmware, configuration and PLR plan. It stages
the bundled installer in a temporary directory but edits no live configuration,
enables no service and performs no restart. It reports the exact planned paths
and skipped fixes. Apply first retains originals and metadata in a root-owned
transaction, copies `recovery.tar.gz` to `PLAB_HOME/backups/qidi-fixes/`, and
checks its SHA-256 before authorizing changes. It then stops Klipper, installs,
restarts the host service and checks controllers and guard status. Repeat runs
with the same options make no edits when everything is already current.

`--fast-probing` updates recognized `[z_tilt]` and `[bed_mesh]` speeds in the
active include graph to 300 mm/s, preserving positions, sample counts and
acceleration. Other custom speeds are skipped with a note. The effective
loaded speeds are verified after restart. The bundled mainline Beacon profile
also uses 300 mm/s for both. Supervised repeatability/mesh-quality checks remain
necessary on the target printer before treating the tuning as commissioned.

`--box-drying` installs the `box_drying` Klipper extension and activates its
configuration section. It validates the printer's own `officiall_filas_list.cfg`
before writes, preserving that file on apply and rollback. No drying cycle is
started during installation. Use `KLIPPEE_BOX_DRY BOX=1 TEMP=45 HOURS=4 HUMIDITY=20`
and `KLIPPEE_BOX_DRY_STOP BOX=1` from the printer console while idle.

Each Box uses an independent monotonic deadline; positive fractional hours do
not round to an untimed cycle. `HOURS=0` is explicitly untimed. Every two seconds
the controller checks loaded slots against current vendor limits, capped at
65 C, and stops if metadata is unknown, disallows heating or now requires a
lower temperature. Humidity control has five-point hysteresis and refuses
invalid readings. Printing, restarts and `TURN_OFF_HEATERS` cancel managed
cycles. A changed external heater target ends ownership without resetting it.
Use the stop command to cancel a humidity-paused cycle: an external write of
the same zero target is indistinguishable from an unchanged target.

`--fast-variable-saves` installs `[save_variables_fast]` for the reviewed stock
`SAVE_VARIABLE` and Box direct-save paths. Identical saves avoid disk IO;
changed values use a unique sibling file, file fsync, atomic replacement and
directory fsync, preserving file ownership and permissions. Values, case
normalization, defaults and error behaviour follow QIDI's file format. Unusual
names or values use the original handler; an IO failure never retries through
its truncating writer. Installation makes no saved-variable changes and checks
both loaded hooks after restart. This option does not alter PLR checkpoints,
migrate Valve-Oil variable names, or install its firmware. A firmware update
that changes reviewed source fingerprints requires a new compatibility review.

The [Box heater refresh review](../../docs/plus4-box-heater-refresh.md) records
why heater refresh and sensor shutdown handling remain separate candidates.

#### Undo fixes

The result includes the remote recovery transaction path and desktop backup
path. To undo that transaction, pass its exact returned path:

```powershell
hlab printer firmware fixes --target printer.local --rollback /var/lib/hlab/qidi-fixes/TRANSACTION -y
```

Rollback restores original files and metadata, removes files introduced by that
transaction, restores journal-timer enablement, and verifies startup. It refuses
a firmware change or subsequent user file edits after a successful install.
The PLR installer's separate rollback manifest is also retained in the root-owned
transaction. A failed activation leaves Klipper stopped; inspect the receipt
and use its rollback handle rather than retrying installation blindly. Keep
backups private because they contain printer configuration. Log retention can
expire old journal history; rollback cannot restore expired logs. Firmware
upgrades and fixes share a lock and must not run concurrently. Firmware updates
do not apply these optional maintenance changes automatically; run `fixes`
after an upgrade to check them again.

References: [2.1.1 live findings](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/firmware/FIRMWARE-2.1.1-LIVE-REVIEW.md),
[PLR repair](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/fixes/PLR-TIMER-TOO-CLOSE.md),
[clock guard](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/fixes/QIDI-BOX-OUTPUT-CLOCK-GUARD.md),
[log limits](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/fixes/INSTALL-LOG-AND-BOX-FIXES.md).

### Updating stock QIDI firmware

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
[printer guide](README.md#3-check-ssh-access). Replace the example address
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
[undo command](#undo-fixes). If rollback refuses because files or firmware have
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

## Safety model

- Fleet reads run concurrently; a mutation targets exactly one printer.
- A printing or paused printer blocks mutations. Missing or unknown Moonraker
  state fails closed.
- Guarded operations need `-y`/`--yes` or an explicit prompt. hlab never
  supplies it for you.
- Nothing is retried after an ambiguous result, and accepted is never reported
  as completed.
- Klippee commands need a folder-scoped connection and never fall back to a
  global target.
- Moonraker requests never follow redirects, so an API key stays bound to the
  configured endpoint.
- Migration stages are journalled before they run, so an interrupted flash is
  visible afterwards rather than silently repeated. A disagreement between the
  journal and the printer stops the engine.
- Guided migration shows the resolved firmware images before approval and asks
  for fresh physical acknowledgements for wiring and host changes.
- Mainboard flashing uses the physical microSD route. eMMC writing checks the
  USB disk's identity first and verifies the image by read-back.
- Power-loss recovery deployment, activation and rollback are separate approved
  commands.

## Development

The printer code is Go and is covered by the repository's standard checks:

```powershell
go vet ./...
go test ./...
```

Optional tests for the Python assets that run on the printer are described in
[migration/README.md](migration/README.md).

The stock-QIDI updater also has dependency-free Linux fixtures (run through
WSL on Windows); CI runs these alongside the Go suite:

```bash
python3 -B -m unittest discover -s internal/printerlab/migration/tools/migration/tests -p 'test_qidi_*.py' -v
```
