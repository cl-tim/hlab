# Printer management with hlab

`hlab printer` manages a fleet of Klipper printers through Moonraker: discovery,
status and health audits, a record of each printer's parts, Klippee
configuration workspaces, and QIDI Plus4 firmware migration and updates.

Any Moonraker printer can be inventoried and monitored. Only the QIDI Plus4 has
a reviewed flashing backend; `hlab printer firmware` refuses other models.

The printer commands ship in the same binary as the rest of hlab — see the
[install instructions](../../README.md#install). Every command has `--help`.

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

Klippee owns configuration: target identity, credentials, desired state, diffs,
backups and history. hlab maps each printer to a local workspace folder and
runs the installed Klippee client there.

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
