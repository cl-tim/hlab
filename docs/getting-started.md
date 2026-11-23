# hlab

A single compiled binary for managing a homelab fleet of Linux machines over
SSH, and the Klipper printers beside them.

**Start with [Linux fleet management](linux/index.md)** for server inventory,
SSH access, commands, health, updates, networking, Compose stacks and recovery.

**Updating or fixing a QIDI printer?** Start with the
[printer guide](guides/qidi-plus4.md). No enrollment or Klippee is needed.

Download a release or build from source, then follow the steps below.

## Install

Download a binary for your platform from the
[releases page](https://github.com/cl-tim/hlab/releases), check it against
`SHA256SUMS`, and put it on your PATH.

Or build from a checkout. That requires Go 1.26+; a newer toolchain is fetched
automatically if `go.mod` asks for one.

```powershell
.\build.ps1 install     # Windows -> %LOCALAPPDATA%\Programs\hlab, added to PATH
```

```bash
make install            # Linux, macOS -> ~/.local/bin
go install github.com/cl-tim/hlab/cmd/hlab@latest   # or with Go alone
```

Both install for the current user and need no administrator rights. Open a new
terminal afterwards and check with `hlab version`. Re-running the install
upgrades in place; `uninstall` removes it.

## Getting started

Find machines on your network and choose which to manage:

```bash
hlab discover                  # scans local IPv4 subnets
hlab discover 192.0.2.0/24     # CIDR, address, or range also works
```

In an interactive terminal, discovery offers to enroll new machines. After
enrollment, check access and collect a read-only snapshot:

```bash
hlab ping all
hlab sync
hlab run all -- uptime
```

The inventory is created automatically when a command first needs it. To
create one deliberately, run `hlab config init`; edit its groups and tags to
match your fleet. See [inventory, selectors and discovery](linux/inventory.md).

## The config

Inventory paths, sample YAML, file locations and safe per-project overrides
are covered in [inventory and discovery](linux/inventory.md#where-configuration-lives).

## Selectors

Learn selector syntax and examples in the
[selectors guide](linux/inventory.md#selectors).

## Commands

Hlab commands are grouped by task in the
[Linux management guide](linux/index.md). The global options and command
coverage are summarized there and in component pages.

### Running commands

See [remote commands and logs](linux/commands.md) for quoting, confirmation,
output formats, concurrency and log filters.

## Discovery and enrolment

[Discovery](linux/inventory.md#discover-machines) and
[SSH enrollment and access](linux/access.md#enroll-a-machine) explain the scan
and enrollment choices.

## Keeping track of machines

Read about read-only collection and saved facts in
[collected facts and health checks](linux/facts-health.md#collect-a-machine-snapshot).

## Health, updates and backups

- [Health checks and endpoints](linux/facts-health.md#check-health)
- [Package and container updates, and reboots](linux/updates.md)
- [Backups and recovery](linux/backups.md)

## Printers

Klippee configuration sync is native to hlab. Run `hlab klippee` in a connected
configuration folder; no Python or separate Klippee CLI is needed for sync.
See the [Klippee guide](guides/klippee.md) for setup, existing connections,
variables, recovery, and compatibility with deployed printer agents.

For a stock QIDI Plus4, follow the [step-by-step printer guide](guides/qidi-plus4.md)
to download hlab, check access, update firmware and apply fixes. You can use the
printer's IP address directly; no enrollment, inventory file or Klippee is needed.

```bash
hlab printer firmware update --target 192.0.2.50 --check
hlab printer firmware fixes --target 192.0.2.50 --check
```

The [printer command reference](reference/printers.md) covers optional saved
printers, fleet status, print history, component maintenance, Spoolman, Klippee
and mainline firmware migration. Saved printers live under `printerlab:` in
hlab's config, separately from servers; `hlab run all` never reaches a printer.

## Authentication and host keys

SSH key order, host-key checking and enrollment security are described in
[SSH access and host security](linux/access.md#ssh-identity-and-host-keys).
