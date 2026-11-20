# Update or fix a QIDI printer

Use hlab from your computer to update a printer or apply fixes. **You do not
need to enroll the printer, create a configuration file, or use Klippee.**

This guide is for a **QIDI Plus4 running stock QIDI firmware**:

- **On 1.8.2:** you can update to 2.1.1, then apply the fixes.
- **Already on 2.1.1:** skip the firmware update and apply the fixes.
- For another model or firmware version, check the
  [support details](REFERENCE.md#updating-stock-qidi-firmware) before continuing.

For saved printers, fleet monitoring or Klippee, use the
[command reference](REFERENCE.md).

## 1. Download hlab

Open the [latest release](https://github.com/cl-tim/hlab/releases/latest) and
download the file for your computer:

| Computer | Download |
| --- | --- |
| Windows PC | `hlab-windows-amd64.exe` |
| Mac with an Apple M-series chip | `hlab-darwin-arm64` |
| Mac with an Intel processor | `hlab-darwin-amd64` |
| Linux PC with an Intel/AMD processor | `hlab-linux-amd64` |
| Linux on ARM | `hlab-linux-arm64` or `hlab-linux-arm`, to match your system |

Rename the downloaded file to **`hlab.exe` on Windows** or **`hlab` on Mac/Linux**.
It is a program, not a firmware file to copy onto the printer.
The release includes `SHA256SUMS` for [checking your download](REFERENCE.md#check-your-download).

Open a terminal **in the folder containing that file**:

- **Windows:** open the folder in File Explorer, type `powershell` in its
  address bar, and press Enter.
- **Mac/Linux:** open Terminal. If you saved it in Downloads, run `cd ~/Downloads`,
  then `chmod +x ./hlab` to allow it to run.

Check that it starts:

```sh
./hlab version
```

The `./hlab` commands below work in Windows PowerShell and Mac/Linux terminals.
If you already installed hlab on your PATH, you can use `hlab` instead.

## 2. Get the printer ready

1. Connect your computer and printer to the same network.
2. Find the printer's IP address in its network settings. In every command
   below, replace **`192.168.1.50`** with that address.
3. Finish or cancel any print, including a paused print.
4. Turn off nozzle, bed and chamber heating. Their **target temperatures must
   be 0°C**; you do not need to cool the printer to 0°C.
5. For a firmware update, connect and power the original touchscreen. The
   printer also needs internet access to download firmware. If it has no internet,
   follow the [offline update instructions](REFERENCE.md#offline-updates).

## 3. Check SSH access

SSH lets hlab connect to the printer. From your computer's terminal, run:

```sh
ssh mks@192.168.1.50
```

On the first connection, check that the address is your printer's before
accepting the connection prompt. Enter the printer's **SSH password** if asked.
Nothing appears as you type a password.

Once connected, run this **inside the printer's SSH session**:

```sh
sudo -n true
```

If it returns to the prompt without an error, run `exit` to return to your
computer. Run all remaining commands **on your computer**, not in the printer's
SSH session or the Fluidd console.

If either command fails, stop and resolve SSH access first. In particular,
`sudo: a password is required` means that account cannot yet run the update.
See [connection help](REFERENCE.md#direct-connection-help) if you use a different
login, an SSH key, or a different port. Enrollment is not required to resolve this.

<a id="updating-stock-qidi-firmware"></a>

## 4. Updating stock QIDI firmware

**Skip this step if the printer already has 2.1.1.** hlab refuses to reinstall
the same firmware or downgrade it.

First check that the printer is ready for the update:

```sh
./hlab printer firmware update --target 192.168.1.50 --check
```

Continue only if the check succeeds. Then run:

```sh
./hlab printer firmware update --target 192.168.1.50 -y
```

`-y` authorizes the changes. Keep the computer and printer powered and connected
until the command finishes. You may be asked for the SSH password more than once.

hlab backs up the printer, updates **both its Linux firmware and touchscreen**,
and keeps your existing printer settings. It prints `updated` after startup
checks pass. Check that the screen returns to its normal home page and shows
temperatures. Keep the backup paths printed by the command.

The firmware update does not apply hlab's fixes. Continue below if you want them.

<a id="stock-qidi-fixes"></a>

## 5. Stock QIDI fixes

You can apply these on either 1.8.2 or 2.1.1, including after an update done
with QIDI's own updater.

The fixes address Box output timing, harden power-loss recovery and limit
system-log space on the supported stock layout. The older Box status fix is
added on 1.8.2 only; an existing copy is kept on 2.1.1.

**For QIDI Box users:** the timing guard normally blocks automatic RFID-triggered
filament insertion. Manual loading still works. See the
[fix details](REFERENCE.md#stock-qidi-fixes) if you have explicitly enabled
automatic insertion or want the optional Box sensor polling mitigation.

Check what would change:

```sh
./hlab printer firmware fixes --target 192.168.1.50 --check
```

If the check succeeds and you want those changes, apply them:

```sh
./hlab printer firmware fixes --target 192.168.1.50 -y
```

hlab saves a backup, applies the fixes, restarts Klipper and checks startup.
`applied` means it finished; `unchanged` means the fixes were already current.
Keep the printed recovery path if you may want to
[undo the fixes](REFERENCE.md#undo-fixes).

## If a command fails

- **A check fails:** resolve the reported problem before running the command
  with `-y`. A printing or paused printer, or an enabled heater, blocks the work.
- **An update or installation fails or is interrupted:** keep the output and
  backups. Do not keep retrying or restart the printer to clear the error.
  Follow the [recovery instructions](REFERENCE.md#recovery).
- **You cannot find a backup:** the command prints its location on your computer
  and on the printer. Default locations are in the
  [backup reference](REFERENCE.md#backup-locations).

For other options, run `./hlab printer firmware update --help` or
`./hlab printer firmware fixes --help`.
