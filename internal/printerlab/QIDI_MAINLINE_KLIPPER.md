# QIDI mainline Klipper migration

The normal workflow is two commands, with physical work guided between them:

~~~powershell
hlab printer firmware prepare my-plus4
hlab printer firmware migrate my-plus4
~~~

Preparation collects and saves the settings, backs up the stock printer, builds
the MCU images, generates a conservative commissioning configuration, and
downloads verified Armbian. Migration writes and verifies the spare eMMC,
stages the microSD, verifies the physical
mainboard flash, guides host replacement and toolhead flashing, and installs
the reviewed commissioning configuration. Re-run **migrate** to continue.

Complete single-material commissioning before attempting the
[optional QIDI Box migration](#optional-qidi-box-migration-with-happy-hare).
These examples use a generic printer name; substitute your own.

**Supported hardware:** QIDI Plus4, with the reviewed X-7 V1.0/Beacon profile.
Its fan and probe modifications must be checked against your actual machine.
Other models, PCB revisions and probes need separately reviewed firmware and
configuration. Changing an inventory model does not make hardware supported.

## Before you start

Have these available:

- Windows with PowerShell 7, Git, OpenSSH and an Ubuntu WSL distribution.
  Go 1.26+ is required to build/install plab from source. The installed CLI
  does not require workstation Python.
- A compatible spare eMMC and USB reader. Plab's Windows writer requires
  512-byte sectors and a reader reporting a serial number and unique disk ID.
  Retain the original eMMC untouched and disconnected from the workstation.
- A microSD with one FAT32 partition no larger than 32 GiB.
- An ST-Link and SWDIO, SWCLK, RESET and GND leads; an isolated, removed
  toolhead also needs a compatible 3.3 V supply.
- Ethernet for the replacement host's first boot, and physical access to
  the power switch.

From the repository root, install hlab with the printer commands:

~~~powershell
.\build.ps1 install
~~~

If Ubuntu's build tools are not installed, run this once from PowerShell:

~~~powershell
wsl -d Ubuntu -- bash -lc 'sudo apt-get update && sudo apt-get install -y git make gcc-arm-none-eabi binutils-arm-none-eabi libnewlib-arm-none-eabi python3 python3-venv'
~~~

Python here is a dependency of upstream Klipper's Linux build tools; plab's
workstation orchestration is Go. Preparation checks for missing tools and
prints the required installation command.

The initial setup is headless. Use Moonraker with a web UI or a separate
KlipperScreen host. The stock touchscreen and any Box remain on their
retained firmware during the base migration.

## 1. Prepare the stock printer

With stock firmware running, the printer idle and all heater targets off:

~~~powershell
hlab printer firmware prepare my-plus4
~~~

For a new target, enter its address, SSH account and key path, then select
the WSL distribution. Existing inventory entries are reused. You do not
need to run **config init** or edit YAML first. Preparation supplies build
paths and saves the printer settings, preserving other printers, groups
and comments. Before changing an existing inventory, it keeps an exact
**config.yaml.before-firmware-\*** recovery copy.

Preparation installs/verifies the SSH key, shows a live audit and asks you
to review it before taking the backup. SSH can prompt for the initial
password and host-key verification. An existing **sshPasswordEnv** is used
only when that variable is populated; no password is saved in the inventory.

It then detects the Beacon serial, saves it for this printer, prepares clean
pinned Klipper/Katapult checkouts, builds and validates the three MCU images,
renders a commissioning configuration and downloads Armbian with its checksum.
If multiple Beacon devices are present, select the exact serial path.

The reviewed install pins remain fixed; host installation and MCU builds must
match. Existing source checkouts with another commit, remote or local changes
are refused rather than reset. Existing rendered configuration is reused only
when its metadata matches the printer's commissioning settings; local edits
are preserved. Preparation can be repeated before cutover.

Paths are printed at the end. New targets have separate firmware directories.
By default, outputs and backups are below **%AppData%\plab**; **PLAB_HOME** relocates
them. The executable includes the build configurations, profiles and helper
assets. This repository requires no companion repository or hlab installation.

Review the generated hardware.cfg and beacon.cfg against your wiring,
fan modifications, heater limits and board revision. The commissioning entry
point enables hardware and Beacon with conservative motion defaults. Box,
production macros, service-area moves and other optional integrations remain
disabled. Keep printer.cfg as the generated commissioning entry point for
guided installation; custom entry points require a deliberate manual deployment.

## 2. Write Armbian to the spare eMMC

The guided **migrate** command offers this necessary write before mainboard
flashing. Attach only the **spare** eMMC through its USB reader and run migrate
from an **Administrator PowerShell** terminal. Select the USB disk by number.
Plab validates the download receipt and checksum, decompresses the entire image
locally, and shows the image and disk identity before asking **[y/N]**.
Press **y** to replace the selected disk's contents; Enter or **n** cancels.
Boot, system, read-only and unidentifiable disks are refused.

Plab flushes the write and reads back the complete written image to verify
its SHA-256. It saves a verification receipt and leaves the disk offline.
Disconnect the reader before removing the module. Keep enough free workstation
space for the compressed download and decompressed image. A failed write or
read-back must be resolved before installing that module.

To perform the write separately, use the public command:

~~~powershell
hlab printer firmware write-emmc
~~~

Optional **--image** and **--device** select the source and destination directly;
the **[y/N]** prompt still appears. An external image writer is also supported:
complete its read-back verification, then acknowledge the prepared spare when
migrate asks. Plab's built-in writer currently supports Windows.

Keep the factory eMMC as the fastest host rollback. A filesystem backup is
not a bootable disk image. If you need a sector image of a reader-attached
factory eMMC, the separate read-only utility is available:

~~~powershell
hlab printer firmware advanced image-factory-emmc --device \\.\PhysicalDriveN --output C:\Backups\factory-emmc
~~~

Replace N only after identifying the physical disk in Windows. This optional
utility requires a read-only source, validates the reviewed 32 GB MBR layout,
and saves evidence in a new output directory. It asks **[y/N]** before creating
the backup; it never writes to the source disk.

## 3. Run the guided migration

With the spare eMMC reader and microSD attached to the workstation, use an
Administrator PowerShell terminal if plab will write the eMMC:

~~~powershell
hlab printer firmware migrate my-plus4
~~~

Follow the prompts in order. Leave the command open while performing the
physical steps, or stop and re-run the same command to continue.

1. Write and verify the spare Armbian eMMC as described above, or acknowledge
   that a verified spare is already prepared. Identify the intended FAT32
   microSD root. Plab waits for approval, then writes and verifies
   qd_mcu.bin. It then asks you to power off, insert the card into the
   **mainboard** slot, boot and wait for the update, power off, and return
   the card to the workstation. It checks that qd_mcu.CUR matches the built
   firmware and that qd_mcu.bin is gone before proceeding.
2. With power off, replace the factory eMMC with the prepared spare. Boot
   with Ethernet, finish Armbian's first-boot setup, create the Klipper owner
   (normally mks), and configure passwordless sudo using visudo.
   Verify the new SSH fingerprint at the console before accepting it.
   Enter the replacement address and accounts when prompted.
3. Plab reinstalls/verifies the SSH key and bootstraps pinned Klipper,
   Moonraker, Katapult, Beacon and ST-Link tools, plus persistent RK3328
   CPU/UART tuning. It fetches Moonraker authentication through verified SSH
   for the running command. Only an environment variable name is saved;
   the API key is never written into YAML or printed.
4. Power off before connecting the ST-Link. Check J1 numbering against the
   actual PCB using the table below. Secure the leads, then boot the host
   and connect the programmer's USB to the **printer host**.
   Plab verifies a 64 KiB recovery dump locally and checks another attached
   board read before erasing, installing Katapult and verifying read-back.
5. Power off, disconnect the ST-Link and temporary leads, restore the
   toolhead harness and cover, and boot again. Plab flashes the application
   through Katapult over UART.
6. Plab checks the inert two-MCU smoke configuration and host tuning. Review
   the rendered hardware configuration when prompted. Installation accepts
   only the exact inert smoke configuration as the remote starting point,
   snapshots the remote configuration, copies the reviewed .cfg files,
   restarts Klipper and verifies both MCU connections.

| Toolhead J1 | ST-Link |
| --- | --- |
| Pin 1 | SWDIO |
| Pin 3 | SWCLK |
| Pin 5 | RESET |
| Pin 6 | GND |
| Pin 2 | 3.3 V **only for an isolated, removed board** |

For an installed board, use the printer harness for power and leave J1 pins
2 and 4 unconnected. For a removed board, disconnect the harness and use a
compatible 3.3 V supply on pin 2. Use exactly one power source.

Migration pauses at **[y/N]** before each destructive operation and physical
acknowledgement. Enter means no; **y** or **yes** proceeds. No **--confirm** token
is needed. **-y** cannot bypass these prompts for disk writes, wiring, an eMMC
swap or configuration review. If the fresh host
cannot report idle, plab asks for physical confirmation of idle and heaters
off; printing, paused state and authentication failures remain stop conditions.

Direct SSH mainboard flashing is disabled. Later toolhead application updates
can use Katapult/UART without repeating the ST-Link erase.

## 4. Commission single-material printing

Ready MCU links do not prove that the printer is calibrated. Follow the
[Klipper configuration checks](https://www.klipper3d.org/Config_checks.html)
and the included profile notes:

1. Confirm both MCUs connect and all room-temperature readings are plausible.
   Check this board revision's chamber-heater power limit.
2. Query/trigger endstops by hand and verify fans individually.
3. Verify heater/sensor mapping and calibrate each heater on this printer.
4. Check motor direction and home one axis at a time at reduced speed.
5. Calibrate Beacon/Z, Z tilt and bed mesh for the actual mount.
6. Calibrate input shaping and pressure advance before raising motion limits.
7. Review and enable single-material operating includes individually. Install
   their optional host dependencies before enabling them.
8. Use a separate mainline slicer profile and complete a small print, including
   pause, resume and cancel checks.

Keep this printer's SAVE_CONFIG, Beacon model and mesh. Do not overwrite
calibration with a newly rendered production printer.cfg. Bring reviewed
operating includes into the calibrated entry point instead. The source profile
contains another machine's tuning and service-area moves; do not enable it
wholesale. Mark the inventory **firmware: mainline** after successful commissioning.

## Progress and recovery

Check progress or preview the remaining sequence:

~~~powershell
hlab printer firmware status my-plus4
hlab printer firmware migrate my-plus4 --dry-run
~~~

Status combines live SSH probes and the workstation journal. The dry run
only probes and previews; it never stages a card, changes settings or flashes.
Continue with the same firmware migrate command after resolving a problem.
An uncertain or interrupted flash stops rather than being repeated automatically.
Existing journals remain compatible with the command rename.

Individual operations live under **hlab printer firmware advanced**. Use its **--help**
when investigating a specific stage. Only record an externally completed stage
after verifying the evidence:

~~~powershell
hlab printer firmware advanced mark my-plus4 bootstrap-host --state completed
~~~

Mark changes the journal only; contradictory live evidence still stops
progress. For stock rollback, restore the retained eMMC **and** compatible MCU
firmware. The mainboard uses QIDI's microSD image; the toolhead uses its full
saved dump through ST-Link or a deliberately built stock-compatible 8 KiB-offset
image through Katapult. The old eMMC alone does not undo MCU flashing.

## Optional QIDI Box migration with Happy Hare

Start only after single-material commissioning has produced a working printer.
Leave the Box MCU on stock firmware during the base migration, exclude QIDI
Box configuration, and feed filament through the single-spool path.

**Compatibility check:** Happy Hare v4 has a
[QIDI Box v2 preset](https://github.com/moggieuk/Happy-Hare/blob/main/installer/boards/custom/Kconfig.qidi_box).
It is not a validated drop-in configuration for every Plus4/Box revision.
Check the controller, stock protocol, sensor polarity, cutter geometry and
MCU names before activation. In particular, the preset references `THR:PA1`,
whereas the reviewed Plus4 profile uses different MCU names; its environment
sensor defaults also differ from the recovered earlier Box map. Keep RFID and
dryer control disabled for initial commissioning. There is no `plab` Box
firmware-flash or Happy Hare installation command.

Use the [current v4 installation guide](https://moggieuk.github.io/Happy-Hare-Doc/Installation/),
not the older v3 examples. Existing v3 installations require the separate
[v3-to-v4 migration](https://moggieuk.github.io/Happy-Hare-Doc/Upgrade-v3-v4/).

### Back up and install the host integration

First take another workstation backup:

Set these to the inventory name, replacement host address and SSH key used
in the guided migration:

~~~powershell
$Printer = 'my-plus4'
$PrinterAddress = 'plus4.example'
$Key = Join-Path $env:USERPROFILE '.ssh\my-plus4-plab-ed25519'
~~~

```powershell
hlab printer firmware advanced backup $Printer --include-logs
ssh -i $Key "mks@$PrinterAddress"
```

In that printer SSH session, as the user who owns Klipper:

```bash
stamp=$(date +%Y%m%d-%H%M%S)
cp -a ~/printer_data/config ~/printer_data/config-before-happy-hare-$stamp
git -C ~/klipper rev-parse HEAD
git -C ~/moonraker rev-parse HEAD
ls -l /dev/serial/by-id/
sudo systemctl stop klipper
cd ~
git clone https://github.com/moggieuk/Happy-Hare.git
cd ~/Happy-Hare
git rev-parse HEAD
./install.sh -i -z -s \
  -k "$HOME/klipper" \
  -m "$HOME/moonraker" \
  -c "$HOME/printer_data/config"
```

Record and review the cloned commit before installation. `-i` opens v4
menuconfig, `-z` prevents the installer from self-updating during this run and
`-s` suppresses service restarts until review. Run the installer as the Klipper
owner, without `sudo` ([installer flag reference](https://github.com/moggieuk/Happy-Hare/blob/main/install.sh)).

Select the QIDI Box preset only if it matches your controller. Otherwise stop
at configuration preparation and resolve the revision differences first. Use
four gates for one four-lane Box, its exact USB by-id path and the actual
printer's sensor/MCU mapping. Do not retain QIDI's `multi_color_controller`,
`box_extras` or `box_stepper` alongside Happy Hare's material state machine.

Review generated includes and client macro integration for duplicate
`PAUSE`/`RESUME`/`CANCEL_PRINT` definitions. Preserve the working printer's
existing macros and adapt their Happy Hare hooks deliberately. Stock Box
protocol compatibility must be established before motion; this guide does not
provide a Box MCU flashing route. If different Box firmware is necessary,
qualify that board's bootloader and recovery separately before changing it.

### Verify, calibrate and enable multi-material printing

After the reviewed configuration is ready, in printer SSH:

```bash
sudo systemctl restart moonraker
sudo systemctl restart klipper
systemctl --no-pager --full status moonraker klipper
```

In the Klipper console, inspect state before moving anything
([v4 command reference](https://moggieuk.github.io/Happy-Hare-Doc/Reference-Commands/)):

```gcode
MMU_STATUS SHOWCONFIG=1
MMU_SENSORS
MMU_HELP TESTING=1
MMU_MOTORS_OFF
```

Follow v4's [hardware validation](https://moggieuk.github.io/Happy-Hare-Doc/Hardware-Validation/)
and [calibration](https://moggieuk.github.io/Happy-Hare-Doc/Calibration/): verify
sensors by hand, then short low-speed moves on one gear at a time. Calibrate
rotation distance, Bowden/toolhead lengths and cutter behavior for this printer.
Prove repeatable load/unload on every gate and one controlled toolchange before
enabling runout recovery or automatic spool handoff.

Keep the single-material slicer profile. Create a separate MMU profile using
the [v4 slicer instructions](https://moggieuk.github.io/Happy-Hare-Doc/Slicer-Setup/)
and test a small two-tool print. Add UI controls, RFID and dryer features only
after their own compatibility and hardware checks. Printerlab's stock-QIDI Box
diagnostic and guard commands are not Happy Hare commissioning commands.

If Box integration fails, stop Klipper, restore the pre-Happy-Hare config and
use the installer's uninstall option with the same explicit paths:

```bash
cd ~/Happy-Hare
./install.sh -d -s \
  -k "$HOME/klipper" \
  -m "$HOME/moonraker" \
  -c "$HOME/printer_data/config"
```

Review `moonraker.conf` for residual Happy Hare entries, restart Moonraker and
Klipper, and verify single-material operation again. This is a host/config
rollback; any separately changed Box firmware needs its own recovery.

## Optional management account

The base examples connect directly as `mks`. For a separate management account,
create it through an existing account with passwordless sudo:

Use the same `$Printer`, `$PrinterAddress` and `$Key` variables as the optional
Box section above, substituting your actual inventory settings.

```powershell
hlab printer firmware advanced setup-user $Printer --pubkey "$Key.pub" `
  --via-user mks --via-identity $Key -y
```

Then set `user: plab` and keep `printerUser: mks` on the printer entry. The new
account is key-only, has `dialout` and passwordless sudo, and its sudoers fragment
is validated before installation. Current tooling runs bootstrap as the declared
printer owner through sudo; it does not install Klipper into the management
account's home. Guided configuration installation runs as the printer owner
to preserve file ownership.

The included [PLR hardening tools](migration/plr-hardening/README.md) target
QIDI's stock power-loss-recovery extension. They are a stock-firmware maintenance
operation and are not a step in the mainline migration.

For included tools and provenance, see [migration/README.md](migration/README.md)
and [migration/SOURCES.md](migration/SOURCES.md). Build settings are documented
in [migration/firmware-configs/README.md](migration/firmware-configs/README.md),
and hardware and operating details in the
[bundled profile README](migration/mainline-configs/mainline-klipper-beacon/README.md).
This guide's command sequence was checked against the CLI/backend and Happy Hare
v4 sources on 2026-10-04; it does not qualify untested hardware revisions.
