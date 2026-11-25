# Stock QIDI fixes and rollback

## Stock QIDI fixes

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

| Fix | Stock 1.6.0 | Stock 1.8.2 | Stock 2.1.1 |
| --- | --- | --- | --- |
| Box output-clock guard | Skipped; Box additions are refused | Install and activate | Install and activate; live review confirmed the defect remains |
| Hardened power-loss recovery | Skipped; base-image recovery entrypoint is not reviewed | Install manifest-bound runtime and checkpoint patch | Same; the vendor still supplies the legacy recovery builder |
| Journal/plain-log limits and cleanup timer | Apply only to the detected small Armbian ramlog layout | Apply to detected small Armbian ramlog layout | Same layout check; skip with a note on other layouts |
| Box `is_tool_change` status shim | Skipped | Install and activate | Preserve an existing shim; do not add one because continued need is unproven |
| Box motion-sensor polling | Refused | Optional `--box-motion-polling` | Optional mitigation for the unchanged Box IRQ path; a watchdog failure was not reproduced on 2.1.1 |
| Remove runout-flag delays | Skipped with a note; no stock Box loop | Optional `--trim-runout-waits`; match only the reviewed PRINT_START loop | Same |
| Persistent USB evidence | Optional `--usb-event-log` | Optional `--usb-event-log`; bounded root-managed event log | Same |
| Tilt/mesh speed | Optional `--fast-probing`; reviewed 150 mm/s settings become 300 mm/s | Optional `--fast-probing`; reviewed 150/200 mm/s settings become 300 mm/s | Same; includes Beacon scan speed |
| Faster durable variable saves | Optional `--fast-variable-saves`; pinned legacy `SAVE_VARIABLE` implementation | Optional `--fast-variable-saves`; exact reviewed QIDI source fingerprints required | Same |
| Timed Box drying | Refused | Optional `--box-drying`; requires an active Box and valid QIDI filament limits | Same |

### Firmware 1.6.0

Firmware is detected automatically. For the applicable optional fixes:

```powershell
hlab printer firmware fixes --target printer.local --usb-event-log --fast-probing --fast-variable-saves --check
hlab printer firmware fixes --target printer.local --usb-event-log --fast-probing --fast-variable-saves -y
```

The 1.6.0 profile requires a non-Box installation. Box modules, configuration
sections and controllers are refused. The exact two vendor `[resonance_tester]`
stanzas may remain together in one file; changed stanzas, extra duplicates
and duplicates split across includes are refused. Other configuration includes
and calibration values are preserved.

The 1.6.0 updater package does not contain all base-image includes, including
`plr.cfg`. Maintenance preserves those files and the existing recovery scripts;
it does not install or claim PLR hardening for this version. The firmware
updater's minimum version for upgrading to 2.1.1 remains 1.8.2.

Legacy variable saves have their own source fingerprints and loaded status
profile, `legacy-1.6.0`. Unchanged `SAVE_VARIABLE` commands avoid writes and
changed commands use the durable atomic writer. The vendor's separate
`save_variable(section, option, value)` helper is preserved and is not optimized.
Unknown sources and Box hooks are refused. Installation and rollback never
rewrite the saved-variable data file.

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
evidence. This host option shares the stock-QIDI 1.6.0/1.8.2/2.1.1 gate;
it does not install on mainline firmware through this command.

After code installation, hardware acceptance remains a supervised Box print
start and a controlled idle USB/reboot test. Do not disconnect a controller
during a print to validate logging.

The command requires existing SSH access, noninteractive root sudo, the stock
`mks` installation layout and ready/idle Klipper with all heater targets at zero.
Only **Plus4 1.6.0, 1.8.2 and 2.1.1** are accepted, with the version-specific
scope above. Configuration includes are preserved;
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

PLR checkpointing waits through startup and probing commands whose nozzle target
is below 150 C. These commands remain non-resumable without disabling checkpoints
for the rest of the print. Returning to a cold target invalidates an earlier
candidate. A subsequent new print can rearm a failed checkpoint writer only after
a durable line-zero reset succeeds; pause/resume cannot clear a writer error.

On the detected small Armbian ramlog layout, the cleanup timer also checks known
plain-text logs in `/var/log` and `/var/log.hdd` every minute. Files above 1 MiB
retain their newest complete lines within 512 KiB, preserving the inode, owner and
permissions. This covers `syslog`, `daemon.log`, `kern.log`, `messages`, `debug`,
`user.log` and their `.1` rotations. It does not edit authentication logs, compressed
archives or printer logs. Concurrent appends can be lost during truncation, as
with logrotate's copytruncate. Cleanup runs immediately during installation to
recover space before restarting journald. Expired log contents cannot be restored
by rollback.

The log fix stops rsyslog from duplicating the QIDI screen client's known
recovery-page message into `syslog` and `daemon.log`; the client's own rotating
`qidi.log` remains available. On 1.8.2/2.1.1, a fresh burst of at least fifty
recovery-page transitions in one second adds an explicit screen-client restart
to the maintenance plan. Apply restarts only an already-active client, after
Klipper is ready and idle, then requires fresh log output and a stable client
within thirty seconds. This does not alter recovery markers or start a print.
Normal page transitions do not trigger this action.

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

The [Box heater refresh review](../research/plus4-box-heater-refresh.md) records
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

References: [Plus4-Grease](https://github.com/JarvisAKF/Plus4-Grease)
(reference for runout-delay removal, persistent USB logging, probing speeds and Box drying),
[2.1.1 live findings](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/firmware/FIRMWARE-2.1.1-LIVE-REVIEW.md),
[PLR repair](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/fixes/PLR-TIMER-TOO-CLOSE.md),
[clock guard](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/fixes/QIDI-BOX-OUTPUT-CLOCK-GUARD.md),
[log limits](https://github.com/timo-quinn/qidi-plus4-firmware/blob/main/docs/fixes/INSTALL-LOG-AND-BOX-FIXES.md).
