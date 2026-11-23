# Firmware migration and updates

## Firmware

[QIDI_MAINLINE_KLIPPER.md](https://github.com/cl-tim/hlab/blob/main/internal/printerlab/QIDI_MAINLINE_KLIPPER.md) is the complete Plus4
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
for a printer live. [migration/README.md](https://github.com/cl-tim/hlab/tree/main/internal/printerlab/migration) covers the
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
