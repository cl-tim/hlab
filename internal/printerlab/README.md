# Printer management with hlab

`hlab printer` is a fleet-management CLI for QIDI printers running either stock QIDI
firmware or upstream Klipper/Moonraker.

It uses Moonraker as the common read and safety plane, then delegates
hardware-sensitive Plus4 migration stages to the reviewed tools bundled in
`hlab`. This repository includes the migration scripts, build configurations
and runtime profiles. Firmware flashing uses a sequence of guarded stages.

## Status

The first vertical slice is functional:

- strict YAML inventory with groups, tags, globs, and exclusions;
- per-printer component records and read-only Klipper hardware snapshots;
- bounded-parallel Moonraker discovery, status, and health audits;
- table, JSON, and YAML output;
- stock, mainline, and explicitly unresolved (`auto`) firmware modes;
- folder-scoped Klippee desktop workspaces for editing configuration in any
  local tool;
- Moonraker idle checks before remote mutation;
- complete Plus4 migration-stage wrappers for audit, backup, build, toolhead
  flashing, mainboard microSD staging, host bootstrap, smoke testing, config
  rendering, and host tuning.

Only the Plus4 has a reviewed flashing backend today. Other QIDI models can be
inventoried and monitored, but `hlab printer firmware` refuses them.

For migration steps and commands, see
[QIDI_MAINLINE_KLIPPER.md](QIDI_MAINLINE_KLIPPER.md).

## Install

Build or install `hlab` from the repository root using the
[homelab installation instructions](../../README.md#install). The same binary
includes the printer commands, migration tools, and profiles.

```powershell
.\build.ps1 install
hlab printer version
hlab printer --help
```

Existing inventories, journals, and credentials under `%AppData%\plab`
are read in place. The printer commands also continue to honor `PLAB_HOME`
and `PLAB_CONFIG`.

## Configure

Create the default config under `%AppData%\plab`:

```powershell
hlab printer config init
hlab printer config path
```

Alternatively use a project-local inventory explicitly:

```powershell
hlab printer --config .\internal\printerlab\examples\printerlab.yaml printers
```

The working directory is never searched implicitly. `PLAB_HOME` relocates
printerlab's home and `PLAB_CONFIG` selects a specific inventory.

```yaml
defaults:
  user: mks
  port: 22
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

Set `apiKeyEnv` to the name of an environment variable when Moonraker requires
an API key. The key itself never belongs in the inventory.

Migration inventory settings, one-time SSH key bootstrap and the optional
management account are documented in
[QIDI_MAINLINE_KLIPPER.md](QIDI_MAINLINE_KLIPPER.md).

### Component inventory

Record physical parts for any printer model, including Vorons. Component IDs
are local names such as `hotend`, `toolhead`, `probe` or `hotend-upgrade`;
use separate IDs for multiple fans, controllers or spare parts.

```powershell
hlab printer components                             # Saved inventory, works offline
hlab printer components example-voron -o json
hlab printer components set example-voron hotend-upgrade --type hotend --model "Example hotend" --status spare
hlab printer components set example-voron hotend-upgrade --status installed
hlab printer components capture example-voron --dry-run
hlab printer components capture all                 # Save configured hardware locally
hlab printer components remove example-voron hotend-upgrade
```

`set` requires `--type` and `--model` for new records. Updates preserve fields
whose flags were omitted; an explicitly empty optional flag clears that field.
Status defaults to `installed`; `planned`, `spare` and `removed` are also
supported. Use `set --status removed` to retain the record of an old part.
`--source` records identity evidence or a reference URL, and `--config-section`
links a part to a Klipper section such as `extruder` or `mcu EBBCan`.
Set and remove require an exact printer name, never a fleet selector.

```yaml
printers:
  - name: example-voron
    address: example-voron.example
    components:
      toolhead:
        type: toolhead
        model: Stealthburner
        status: installed
      hotend-upgrade:
        type: hotend
        manufacturer: Phaetus
        model: neXt G
        status: spare
        notes: HF/UHF configuration unconfirmed
```

`capture` uses only Moonraker GET requests to read `configfile.config` from
Klipper's last startup/restart. It saves timestamped evidence for configured
controllers, extruders, steppers, drivers, probes, heaters, fans, temperature
and filament sensors, accelerometers, lighting and servos. Only selected
hardware settings are retained; macros and unrelated configuration are excluded.
Configured sensor types, MCU connection IDs and nozzle dimensions are evidence,
but do not identify the physical hotend, extruder mechanism, board model, fan
size or printed toolhead. Record those identities with `set`.

The captured `componentSnapshot` stays separate from the `components` records.
Capture never changes an upgrade's status or overwrites manually recorded
identities. A new snapshot replaces the previous one, removing disappeared
configuration sections; it does not keep a growing history. `components`
shows configured evidence alongside recorded identities, with capture times
in the table and full fields in JSON/YAML.

Capture supports the usual printer selectors and bounded concurrency. If any
selected printer fails, no snapshots are saved; capture available printers
individually to update them while others are offline. An inventory edit during
collection also cancels saving. Local edits preserve unrelated settings and
comments, validate before replacement, and retain an exact backup named
`config.yaml.before-components-*`. No printer configuration, heating, homing
or restart is performed.

`hlab printer firmware advanced paths PRINTER` shows the bundled tools and that printer's output
paths. Reviewed tools are verified and extracted under `PLAB_HOME/tooling`;
downloads, build outputs and rendered configuration use `PLAB_HOME/artifacts`,
and recovery backups use `PLAB_HOME/backups`. See [migration/README.md](migration/README.md)
for the included assets, provenance and optional custom overrides.

### Desktop configuration workspaces

Klippee remains authoritative for configuration target identity, credentials,
desired state, diffs, backups, variables, and history. `plab` only maps each
inventory printer to a local desktop workspace and runs the installed Klippee
client there:

```powershell
hlab printer klippee path printer-a
hlab printer klippee target printer-a
hlab printer klippee check printer-a --diff
hlab printer klippee apply printer-a
hlab printer klippee capture printer-a --note "Edited in VS Code"
hlab printer klippee variable-set printer-a beacon_serial /dev/serial/by-id/usb-Beacon_...
hlab printer klippee variable-doctor printer-a
hlab printer klippee printer-variable-set printer-a beacon_serial /dev/serial/by-id/usb-Beacon_... -y
hlab printer klippee printer-check printer-a
hlab printer klippee printer-capture printer-a -y
hlab printer klippee deploy printer-a --wait
hlab printer klippee diagnostics printer-a
hlab printer klippee box-reset-reason printer-a
hlab printer klippee box-guard-status printer-a
hlab printer klippee box-state printer-a
hlab printer klippee harden-box-motion-template printer-a -y
hlab printer klippee install-box-output-clock-guard printer-a -y
hlab printer klippee operation-status printer-a [OPERATION_ID]
hlab printer klippee upgrade-agent printer-a -y
hlab printer klippee refresh-agent printer-a -y
hlab printer klippee firmware-restart printer-a -y
hlab printer klippee restart printer-a -y
```

`defaults.klippee.workspaceRoot` derives a unique `<root>\<printer>` folder.
Use `printers[].klippee.workspace` only when one printer needs an explicit
location. That explicit location may be the printer's canonical profile inside
a Git checkout (for example `C:\printer-configs\printer-a`) so VS Code,
Git, and Klippee all edit the same files. Two printers may not share a
workspace, and `.klippee/` must remain ignored by Git.

Initialize each folder with the desktop setup command generated by Klippee.
Every command except `path` requires a real
`<workspace>\.klippee\config.json` whose `client_mode` is `desktop` and whose
`config_dir` exactly matches that workspace. `plab` deliberately refuses
Klippee's legacy global connection fallback because it can silently select the
wrong printer. Repository IDs and tokens stay in the folder-scoped connection
file and must not be copied into the inventory or committed to Git.

To replace an older duplicate workspace, initialize a new desktop connection
in the canonical profile with Klippee's generated private setup command, point
the inventory entry at that profile, then run `target` and `check` before
retiring the old folder. `plab` does not offer a credential-copying
`relocate-workspace` command: copying and rewriting a token-bearing connection
file would undermine the folder-scoped credential boundary and would still
leave ambiguous file-merge and inventory-edit semantics.

`apply` is the explicit operation that brings desired configuration down into
the desktop folder. It displays the target and changed paths, prompts before
replacement, and lets Klippee create its normal backup. The global `-y`/`--yes`
flag approves that choice and every other guarded Printerlab operation, but is
never supplied implicitly. It can appear before or after the subcommand.
`capture` makes edits from VS Code or another local tool the desired
configuration. Because these are desktop connections, neither command claims
that the physical printer changed.

`variable-list`, `variable-set`, `variable-unset`, and `variable-doctor` expose
Klippee's folder-scoped variable workflow without changing directories by
hand. Values live only in the ignored `klippee_variables.cfg`; desired config
files retain `${KLIPPEE:name}` placeholders, allowing later printer captures
to restore placeholders instead of hardcoding device serials.

`printer-variable-set` performs the corresponding physical-workspace update
through the bundled backend's fixed installed-CLI operation. It requires
the printer idle, the agent quiescent, and explicit `-y`/`--yes` approval; it runs
Klippee's variable doctor afterward and returns only a value hash. It never
deploys configuration or acts on Klipper.

`printer-check` and `deploy` are deliberately separate physical-printer
operations. They call the connected agent named `klippee` through Moonraker's
official `POST /server/extensions/request` endpoint; they do not use SSH or
read configuration files from the printer. `deploy` first requires Moonraker
to report an idle print state, then requests `klippee_apply`. The printer agent
repeats its own state guard and pulls the desired configuration using the
printer's folder-scoped Klippee connection. `printer-check` requests the
read-only `klippee_check` method.

`printer-capture` is the explicit reverse physical operation for verified
printer-generated changes. It requires an idle printer, a quiescent current
agent, and `-y`/`--yes` approval before submitting
exactly one `klippee_capture`; it then waits for that exact operation. It never
deploys or retries. Pull the resulting desired state into the desktop workspace
with `hlab printer klippee apply` before editing or committing it.

Older installed agents register `klippee_pull` and `klippee_status`. Use
`deploy --legacy` or `printer-check --legacy` only when that older agent is
known to be installed. `plab` deliberately does not fall back after an error:
a network timeout is ambiguous, so retrying another mutation method could
start the same deployment twice. Upgrade the printer-side Klippee client and
agent to make the current methods the default long-term path. Legacy deploy is
explicitly non-waitable because those agents do not expose operation status.

Current agents return an `operation_id` when accepting work and expose
`klippee_operation_status`. `operation-status` reads the current or most recent
result; pass the ID printed by deploy to ensure a newer operation is never
mistaken for the requested one. The agent intentionally retains only its
current and last operation.

`diagnostics` requests `klippee_upload_diagnostics`, waits for that exact
operation, and prints only a structurally validated private HTTPS
`/log-diagnostics?upload=<UUID>` URL. The bundle expires after 48 hours. It
fails closed if another agent operation is running, the retained result does
not belong to the requested method, or the private URL/result is malformed.
If the Moonraker agent is stuck, `diagnostics --ssh` is an explicit bounded
fallback. It uses key-only inventory SSH to execute exactly the printer
account's `klippee upload-diagnostics`; Printerlab never reads the logs and
never retries the upload.

After a hard power cycle has rotated away the failure tail,
`diagnostics --ssh --previous` selects only the newest rotated `klippy.log.*`
under the fixed printer log directory and passes it to the installed Klippee
client for private upload. It neither returns the log over SSH nor accepts a
caller-supplied remote path.

`box-reset-reason` requests the fixed read-only
`klippee_query_box_reset_reason` agent method and waits for its exact accepted
operation ID. It is intended for a Box communication failure that has already
left fresh Klippy state at `shutdown`; run it before a firmware restart,
power-cycle, or another Box reset can replace the evidence. The agent must
prove that the configured `mcu_box1` endpoint is unowned and exclusively open,
then sends the reviewed reset-reason query with correlation OID 165. Printerlab
accepts only schema v2, the echoed correlation tag, the fixed read-only
`RCC_CSR` query, the known Box MCU/USB identity and reset-reason vocabulary,
and explicit proof that no reset, flag clear, configuration change, or Klipper
action occurred. It recomputes the ordered asserted-flag list from the raw
32-bit register and rejects inconsistent evidence. Missing, duplicate,
unknown, case-aliased, or conflicting fields fail closed. The command has no
SSH fallback, never reads printer files, never retries the query, and supports
the normal table, JSON, and YAML output formats. Because `RCC_CSR` flags remain
sticky until firmware clears RMVF, every accepted result remains marked as
potentially stale.

`install-box-output-clock-guard` first proves that the Klippee agent has no
running operation, then invokes only the bundled backend's reviewed
`install-box-output-clock-guard` operation with the selected inventory
connection after `-y`/`--yes` approval. Printerlab exposes no source,
destination, owner, mode, or service option. It requires the result to bind the
reviewed digest to
`/home/mks/klipper/klippy/extras/box_output_clock_guard.py`, account `mks`, mode
`0644`, and a canonical transaction backup when present. It also requires
proof that neither Klipper nor configuration was changed and that remote
staging cleanup succeeded. A cleanup failure is reported after the install and
backup evidence is printed, but the command still exits unsuccessfully. The
operation never activates the module or restarts Klipper: add
`[box_output_clock_guard]` through the normal Klippee workspace/capture/deploy
flow, then use the separately approved `klippee service-restart` command while
the printer is idle. A G-code `RESTART` or `FIRMWARE_RESTART` is insufficient
for a Python module upgrade because the running host process may retain the
already imported module.

After activation, `box-guard-status` reads a fixed set of non-sensitive
Moonraker objects and submits no G-code. It fails unless the output-clock guard
reports both Box outputs bound to `mcu_box1`, the post-load `RELOAD_ALL` guard
is active, `box_motion_sensor` has `use_irq: False`, and automatic insertion is
disabled (either by its stored variable or by the guard's safe default).
`box-auto-insert-disable` and `box-auto-insert-enable` are the corresponding
idle-gated, approval-required controls. They refuse to act until the post-load
guard is proven active, issue only the fixed enable or disable macro, and read
the status back to verify the saved setting. Manual `LOAD_Tn` operations remain
available while automatic insertion is disabled.

Stock QIDI firmware regenerates `box.cfg` from `/home/mks/cfg_with_N` when
Klipper starts. `harden-box-motion-template` detects `N` from fixed Moonraker
configuration evidence, requires the printer idle and Klippee agent quiescent,
then delegates to the bundled backend's guarded installer. The installer
backs up and changes only that fixed template, verifies `use_irq: False` and no
`debounce_us`, and performs no active config change or Klipper action. Follow
it with the normal Klippee deploy and approved restart workflow.

`restore-box-motion-template` is the explicit experimental inverse. It creates
a new backup and restores QIDI's `use_irq: True` and `debounce_us: 50` in only
the detected `cfg_with_N` template. It does not change active `box.cfg` or
restart Klipper; update the desktop workspace, capture, deploy, and restart
through Klippee separately. `box-guard-status` reports the live debounce value
and deliberately marks this vendor IRQ mode unsafe so the experiment remains
visible.

Without `--wait`, a successful deploy still means **accepted**, not
**completed**. `deploy --wait` first verifies that the current agent supports
operation status, submits exactly one apply, then polls that exact operation
for up to four minutes by default. `--wait-timeout` changes that bound. A
failed, timed-out, nonzero, missing, or internally inconsistent result makes
the command fail; it never retries apply. After confirming success,
`klippee restart` provides a separate, idle-gated and explicitly approved
in-process Klipper configuration restart. Deployment never restarts Klipper
implicitly. When installed Python code must be re-imported, use
`klippee service-restart`: it restarts exactly `klipper.service` over the
key-only inventory SSH connection, proves the process PID changed, and waits
for Moonraker to report Klippy ready.

For diagnosis, `klippee printer-diff` runs only the installed account's
read-only `klippee check --diff`, while `klippee agent-events` returns a
bounded view of recent Moonraker console messages that mention Klippee. If
reviewed physical changes have already been incorporated into desired state,
the explicitly approved `deploy --ssh-force -y` command is the idle-gated
fallback for the fixed `klippee pull --force` operation; it cannot be combined
with the normal, legacy, or waiting deploy paths. `idle-timeout` reads only
Klipper's resolved timeout setting, and
`cancel-print -y` cancels only a print first proven to be printing or paused and
then verifies it is no longer active.

When Klipper is shut down because an MCU disconnected or reset, preserve the
retained reset evidence before recovery (for a QIDI Box, use
`box-reset-reason`). Then `klippee firmware-restart` submits exactly one fixed
`FIRMWARE_RESTART` through Moonraker after confirming the printer is idle. It
reinitializes every MCU and loads the deployed configuration; it does not
deploy configuration. The command requires `-y`/`--yes` and never
retries an ambiguous timeout. Use the ordinary `restart` only when MCU
reinitialization is not required.

`upgrade-agent` upgrades only the selected Plus4 printer's managed Klippee
client and Moonraker agent. It first requires `klippee_operation_status` to
prove that no asynchronous operation is running, then invokes the firmware
repository's fixed `upgrade-klippee-agent` operation with inventory connection
arguments after `-y`/`--yes` approval. Printerlab exposes no source,
destination, account, or service overrides. It accepts only the official
`https://klippee.xyz/install/klippee.py` source, account `mks`, CLI path
`/home/mks/.local/bin/klippee`, and the
`klippee-moonraker.service` user unit. The result must contain valid date-style
before/after Klippee versions and SHA-256s, an active service with a positive
new PID, zero upgrade/state exit codes, explicit upgrade/restart success, and
proof of zero Klipper actions and configuration deployments. `changed` must
exactly match the version/digest comparison. Missing, duplicate, unknown,
case-aliased, trailing, or inconsistent evidence fails closed and is never
retried.

If Moonraker is idle but a current Klippee agent rejects work using a cached
`paused` state, use `refresh-agent`. Before SSH, it requires
`klippee_operation_status` to prove that no asynchronous operation is running;
unsupported, malformed, or running status fails closed with no legacy fallback
and no restart. It then delegates to the reviewed firmware utility, restarts
exactly the `klippee-moonraker.service` **user** unit, and verifies it is active.
It does not restart Klipper, deploy configuration, inspect printer files, or
retry the failed deployment. Re-run `printer-check` and then `deploy`
explicitly.

When an unhealthy agent cannot answer operation status, `refresh-agent
--recover-unresponsive -y` and `upgrade-agent --recover-unresponsive -y` may
use Moonraker's independently verified idle print state instead. The recovery
flags do not weaken the fixed service, account, source, digest, or proof that
neither Klipper nor configuration was changed by those commands.

## Discover and inspect

```powershell
hlab printer discover scan
hlab printer discover scan --emit
hlab printer discover scan 192.0.2.0/24
hlab printer discover scan 192.0.2.0/24 --emit
hlab printer printers all
hlab printer status all
hlab printer status '#mainline'
hlab printer audit '@plus4,!#maintenance'
hlab printer -o json status all
```

Discovery records firmware as `auto`: Moonraker alone cannot reliably prove
whether a vendor fork or upstream Klipper is installed. Review emitted entries
and declare `stock` or `mainline`.

With no CIDR, discovery asks the operating system which local address serves
the default IPv4 route and scans that interface's subnet. This works with
bridged Hyper-V networking as well as ordinary Ethernet and Wi-Fi. If no
default route can be resolved, it falls back to active physical-looking
interfaces while excluding loopback, point-to-point, link-local, and common
virtual/container interfaces. Pass one or more CIDRs to override detection.

## Mainline Klipper migration

Follow [QIDI_MAINLINE_KLIPPER.md](QIDI_MAINLINE_KLIPPER.md) for the complete
Plus4 migration: workstation setup, SSH access, backups, firmware builds,
mainboard microSD flashing, eMMC replacement, toolhead flashing and
single-material commissioning. It also covers interrupted migrations and
optional QIDI Box integration with Happy Hare afterward.

Prepare the target, then follow the guided migration:

```powershell
hlab printer firmware prepare printer-a
hlab printer firmware migrate printer-a
```

For a commissioned mainline printer, check or update the Klipper host:

```powershell
hlab printer firmware update printer-a --check
hlab printer firmware update printer-a -y
```

The command fetches the current official Klipper `master` revision over the
inventory SSH connection. `--check` verifies the running host and reports
whether an update is available; it changes neither the checkout nor the service.
An already current host is verified without restarting it.

An update requires a ready, idle printer, no motion or pause, and zero targets
for **every configured heater**, including the chamber. It rejects stock or
unresolved inventory entries, non-upstream origins, other branches, diverged
history, tracked local edits, empty configurations and changed Python dependency
requirements. It operates on `/home/<printerUser>/klipper` and the corresponding
`printer_data` directory, using non-interactive sudo when SSH uses a management
account. Moonraker must be reachable on the printer's loopback interface at the
inventory `moonrakerPort`; `apiKeyEnv` is supported and the key travels only in
SSH stdin.

Before stopping Klipper, it archives the configuration under
`printer_data/backups/klipper-update-*` and retains the preceding Git revision
in a rollback branch. After the fast-forward update it requires the running
process to match the checkout, the full systemd startup to finish, all previously
configured controllers to reconnect, Beacon to report samples when installed,
and the installed QIDI host-tuning audit to pass. Verification defaults to three
minutes (`--wait-timeout` changes it). A startup failure triggers rollback of
the **host source**, followed by the same checks; the command still exits with
failure and reports whether recovery succeeded. It will not stop a new active
job for automatic rollback. Interrupted connections or failed recovery require
checking the retained backup and live state before retrying.

Configuration, calibration and extension files are preserved and checked for
changes. The command does not flash controllers, update Moonraker or extensions,
install permanent helpers, alter `SAVE_CONFIG`, or rewrite the migration build
pins. Controller protocol incompatibility is a failed startup and rolls the host
back. Updates that change Python requirements need a reviewed environment
upgrade; this command does not partially update the shared extension environment.

## Command overview

```text
hlab printer config init
hlab printer discover scan [CIDR...]
hlab printer printers [selector]
hlab printer status [selector]
hlab printer audit [selector]
hlab printer firmware prepare PRINTER
hlab printer firmware migrate PRINTER [--dry-run]
hlab printer firmware update PRINTER [--check | -y] [--wait-timeout DURATION]
hlab printer firmware write-emmc [--image PATH] [--device WHOLE_USB_DISK]
hlab printer firmware status PRINTER
hlab printer firmware advanced --help
hlab printer klippee path PRINTER
hlab printer klippee target PRINTER
hlab printer klippee check PRINTER [--diff]
hlab printer klippee apply PRINTER [-y|--yes]
hlab printer klippee capture PRINTER [--note TEXT]
hlab printer klippee variable-list PRINTER
hlab printer klippee variable-set PRINTER NAME VALUE
hlab printer klippee variable-unset PRINTER NAME
hlab printer klippee variable-doctor PRINTER
hlab printer klippee printer-variable-set PRINTER NAME VALUE -y
hlab printer klippee printer-check PRINTER [--legacy]
hlab printer klippee printer-capture PRINTER -y [--wait-timeout DURATION]
hlab printer klippee deploy PRINTER [--wait] [--wait-timeout DURATION] [--legacy]
hlab printer klippee diagnostics PRINTER [--ssh] [--previous] [--wait-timeout DURATION]
hlab printer klippee box-reset-reason PRINTER [--wait-timeout DURATION]
hlab printer klippee box-guard-status PRINTER
hlab printer klippee box-state PRINTER
hlab printer klippee harden-box-motion-template PRINTER -y
hlab printer klippee install-box-output-clock-guard PRINTER -y
hlab printer klippee operation-status PRINTER [OPERATION_ID]
hlab printer klippee upgrade-agent PRINTER -y
hlab printer klippee refresh-agent PRINTER -y
hlab printer klippee firmware-restart PRINTER -y
hlab printer klippee restart PRINTER -y
```

## Safety model

- Fleet reads may run concurrently.
- Mutations target exactly one printer.
- Printing and paused states block mutations.
- Missing or unknown Moonraker state fails closed.
- Offline transition stages require an explicit override and physical check.
- Guarded operations require `-y`/`--yes` or an explicit guided-workflow prompt.
- Unsupported models are rejected before invoking firmware tooling.
- Klippee commands require a unique folder-scoped desktop connection and never
  fall back to a global target.
- Klippee apply never supplies `--yes` unless the operator explicitly passed
  Printerlab's global `-y`/`--yes` flag.
- Klippee deploy uses only Moonraker's fixed agent method, checks the physical
  printer is idle first, and reports asynchronous acceptance as acceptance.
- Klippee deploy waiting preflights status support, follows the exact accepted
  operation ID to a bounded timeout, and never retries an ambiguous apply.
- Klippee Box reset-reason reads require shutdown, an unowned and exclusively
  opened Box endpoint, the fixed `RCC_CSR` and reset-reason queries, and
  structured no-action proofs; they never restart Klipper, reset an MCU, clear
  flags, deploy configuration, use SSH, or infer a result from command output.
- Klippee Box clock-guard installation exposes only the reviewed firmware
  operation, validates the target digest/account/
  mode and cleanup proof, and never edits config or restarts Klipper.
- Klippee agent upgrade fixes the official source, account, CLI path, and user
  service; it requires quiescence plus version/digest/PID/restart proofs and
  attests that no Klipper action or configuration deployment occurred.
- Klippee agent refresh first proves no asynchronous operation is running, can
  target only its fixed user service, and attests that it performed no Klipper
  action or configuration deployment.
- Moonraker requests never follow HTTP redirects, so API keys remain bound to
  the configured endpoint.
- PLR deployment, activation, and rollback are separate approved commands;
  results must prove the inventory account and canonical paths, and exact
  tool-returned backup/safety manifests are passed forward without discovery.
- Mainboard flashing uses the proven physical microSD route.
- Stage attempts are journalled before they run, so an interrupted flash is
  visible afterwards rather than silently repeated.
- Journal and probe disagreements stop the engine instead of being guessed at.
- Guided migration shows the resolved firmware images before approval and
  always requires fresh physical acknowledgements for wiring and host changes.
- Armbian eMMC writing is offered by guided migration and is also available
  as `firmware write-emmc`. It requires a fresh `[y/N]` approval, checks USB disk
  identity before writing, and verifies the complete written image by read-back.

## Development

The CLI and workstation migration backend are Go. The standard checks need
Go and run the migration tests with the rest of the CLI tests:

```powershell
go vet ./...
go test ./...
```

On Linux, use `make check`. Optional tests for the Python assets that run on
the printer are documented in [migration/README.md](migration/README.md).
