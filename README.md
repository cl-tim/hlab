# hlab

A single compiled binary for managing a homelab fleet of Linux machines over
SSH, and the Klipper printers beside them.

Describe your machines once in a YAML inventory, then act on them individually,
by group, or by tag — in parallel, with structured output when you want to pipe
it somewhere.

```console
$ hlab run '#docker' -- 'docker ps --format "{{.Names}}" | wc -l'

=== docker-01 ===
14
=== docker-02 ===
6

HOST        STATUS   EXIT   TIME
docker-01   ok       0      412ms
docker-02   ok       0      388ms

2/2 ok
```

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

Find the machines on your network, and choose which to manage:

```bash
hlab discover                  # sweeps your local subnets
hlab discover 192.0.2.0/24     # or a CIDR, an address, or a range (192.0.2.10-50)
```

```console
ADDRESS      PORT   HOSTNAME    OS       SSH              MANAGED   SINCE LAST SCAN
192.0.2.1    22     -           -        Server           no        new
192.0.2.10   22     nas-01      Debian   OpenSSH_10.0p2   no        new
192.0.2.20   22     docker-01   Ubuntu   OpenSSH_9.6p1    no        new
```

In a terminal, the scan then offers to enrol each unmanaged machine. Once some
are enrolled:

```bash
hlab ping all                  # reachability, host key trust and auth
hlab sync                      # record what each machine is and runs
hlab run all -- uptime
```

No setup is needed first: the config is created the first time a command needs
it. To write the inventory by hand instead, start from `hlab config init` and
[examples/homelab.yaml](examples/homelab.yaml).

## The config

Everything hlab owns lives in one directory under your home — `~/.config/hlab`,
or `%AppData%\hlab` on Windows. `hlab config home` prints it.

```
config.yaml     the inventory: servers, printers, and what scans have found
facts.yaml      volatile state from `hlab sync`; machine-owned, not hand-edited
id_hlab         hlab's management private key
```

```yaml
defaults:
  user: root
  connectTimeout: 10s

hosts:
  - name: nas-01
    address: 192.0.2.10
    tags: [storage, debian]

  - name: docker-01
    address: 192.0.2.20
    user: youruser
    tags: [docker, debian]

groups:
  compute: [docker-01]

printerlab:                  # printers, managed with `hlab printer`
  printers:
    - name: printer-a
      address: 192.0.2.60
```

**The working directory is never searched.** hlab is often run from inside a
checkout, and `id_hlab` is root-equivalent on every enrolled machine; nothing
it stores should be one `git add -A` away from being published. A per-project
inventory has to be asked for with `--config` or `$HLAB_CONFIG`, and even then
the key stays in hlab's home. `$HLAB_HOME` relocates everything.
`hlab config show` reports every resolved path.

Scanned and managed machines share the one `hosts:` list, told apart by
`managed:`. An entry you write by hand with no `managed:` key is managed; only
hlab writes `managed: false`. Enrolling promotes the scanned record in place
rather than adding a second one.

## Selectors

Every command that touches a machine takes a selector. Terms are comma
separated and unioned; `!` subtracts, wherever it appears.

| Term       | Matches                           |
| ---------- | --------------------------------- |
| `all`      | every host                        |
| `@group`   | every member of a group           |
| `#tag`     | every host carrying a tag         |
| `nas-01`   | one host by name                  |
| `docker-*` | host names matching a glob        |
| `!term`    | subtract these matches            |

```bash
hlab run 'all,!#retired' -- uptime
hlab run '@compute,!docker-02' -- 'systemctl restart caddy'
```

A term that matches nothing is an error, so a typo fails loudly. Selectors
only ever resolve to **managed** machines; unmanaged matches are reported as
skipped, never contacted.

## Commands

| Command                         | Purpose                                            |
| ------------------------------- | -------------------------------------------------- |
| `hlab discover [target]`        | Find machines with SSH open and offer to enrol them |
| `hlab enroll [address...]`      | Bring machines under management                    |
| `hlab unenroll <host>`          | Revoke hlab's access and stop managing a machine   |
| `hlab repair [selector]`        | Re-assert hlab's account on managed machines       |
| `hlab hosts [selector]`         | List machines; `groups`, `history`, `rename`, `prune` |
| `hlab ping <selector>`          | Check reachability, host key trust and auth        |
| `hlab run <selector> -- <cmd>`  | Run a command everywhere, in parallel              |
| `hlab sync [selector]`          | Refresh what hlab knows about managed machines     |
| `hlab check [selector]`         | Assert machines are working, not merely reachable  |
| `hlab logs <selector> <name>`   | Fetch container or service logs across machines    |
| `hlab update [selector]`        | Report and install package and container updates   |
| `hlab reboot <selector>`        | Restart machines and wait for them to come back    |
| `hlab net`                      | Pin a static address, or turn off redundant wifi   |
| `hlab stack`                    | Deploy and manage Docker Compose stacks            |
| `hlab secret`                   | Manage the credentials a stack reads at runtime    |
| `hlab backup` / `hlab restore`  | Copy stack state off a machine, and put it back    |
| `hlab dns sync`                 | Publish the inventory as DNS records               |
| `hlab printer`                  | Manage Klipper printers, including Voron and QIDI  |
| `hlab config`                   | `init`, `path`, `home`, `show`                     |

Every command has `--help`. Global flags:

| Flag                  | Effect                                             |
| --------------------- | -------------------------------------------------- |
| `--config`            | Config file (else `$HLAB_CONFIG`, else the default) |
| `-o, --output`        | `table` (default), `json`, `yaml`                  |
| `-c, --concurrency`   | Max hosts acted on at once (default 10)            |
| `--connect-timeout`   | SSH connect timeout (default 10s)                  |
| `--insecure-host-key` | Skip host key verification — bootstrapping only    |

### Running commands

Put the command after `--`. The words are joined and handed to the remote login
shell, so pipes, redirects and `&&` work; quote the whole thing to protect it
from your local shell. `--confirm` lists the targets and asks first.

```bash
hlab run @storage -- 'df -h /'
hlab run '#debian' --confirm -- 'apt-get update && apt-get -y upgrade'
hlab -o json run @compute -- 'free -m' | jq -r '.[] | "\(.host): \(.exitCode)"'
```

`run` and `ping` exit non-zero if any host is unreachable or the command fails
anywhere. With `-o json` or `-o yaml` you get one array of results and nothing
else.

## Discovery and enrolment

`hlab discover` opens TCP connections and reads each SSH banner. It never
authenticates, so it is safe to point at a range. Everything found is recorded,
and the next scan reports what changed — a new machine, a rebuilt one, an SSH
version that moved. `--save=false` records nothing, `--scan-only` skips the
enrolment prompts, and `--new --emit` prints a YAML fragment for review instead.

`hlab enroll` with no arguments works through what earlier scans recorded, or
takes addresses directly. For each machine it asks how hlab should get access:

- **Create a dedicated account** (the default). hlab creates an `hlab` account,
  locks its password, installs its own key, and writes a sudoers fragment that
  is checked with `visudo -c` before being moved into place.
- **Use credentials you already have.** hlab records a username and key that
  already work. Nothing on the machine is changed.

Either way hlab reconnects to prove the access works before recording the
machine. Things worth knowing:

- **The password you type is never stored**, never put on a command line, and
  reaches sudo on stdin. Enrolment refuses to run without a terminal.
- **An unknown host key is shown and must be accepted.** A key that differs
  from the one already recorded is refused outright.
- **`--sudo nopasswd` is the default, and it is a real trade-off.** It is what
  makes unattended management work, and it means anyone holding hlab's private
  key has root on every enrolled machine. Use `--sudo password` or
  `--sudo none` (enough for `hlab sync`) if that is too much.

`hlab repair` re-asserts the account, sudoers fragment and key; it is
idempotent and can be scheduled. If the account is locked out,
`hlab repair <host> --as <admin-user>` rebuilds it from outside.

`hlab unenroll <host> --apply` removes hlab's key and sudo rule from the
machine and proves the key is refused **before** changing the inventory. Add
`--forget` to delete the record and `--remove-account` to delete an account
hlab created. `--offline` revokes nothing — the key stays authorised on that
machine if it ever comes back.

## Keeping track of machines

`hlab sync` connects to each managed machine once and collects the OS, kernel,
CPU and memory, networking, disks, running services, listening ports and
containers. It only reads; sudo is used where it changes the answer and is
never required.

```console
$ hlab sync
HOST        OS                 KERNEL           UPTIME   CPU   MEM   DISK   SVCS   PORTS   CTRS
docker-01   Ubuntu 24.04.4     6.8.0-51         14d 3h   8     41%   62%    52     14      11
nas-01      Debian GNU/Linux   6.1.0-18-amd64   61d 7h   4     75%   11%    47     8       0
```

Slow-changing attributes (kernel, hardware, network, containers) are written to
the host record in `config.yaml`; services, ports, disks and load go to
`facts.yaml`, so the config stays readable. `--emit-tags` suggests tags from
what each machine runs, and `hlab hosts history <host>` shows what changed
between syncs. A machine whose DHCP address moved is followed, on proof of its
recorded host key.

## Health, updates and backups

```bash
hlab check @servers                              # disks, failed units, containers, endpoints
hlab update @servers                             # report what is pending
hlab update @servers --apply --reboot --rolling  # install, one machine at a time
hlab stack deploy apps-1 web --apply --sync      # stacks/web/compose.yaml under hlab's home
hlab stack diff apps-1                           # deployed compose files vs local copies
hlab backup @servers                             # archive each machine's stack state
hlab backup verify @servers --max-age 168h       # newest archive present, intact, recent
hlab backup drill drill-1 --from ./backup.tar.gz # rehearse a restore on a disposable host
```

`update`, `stack deploy` and `backup drill` only preview until given `--apply`,
and an update never reboots without `--reboot`. A rolling update checks each machine and its endpoints before
moving to the next, and stops on a failure. Backups are checksummed on the
machine and again after transfer.

Endpoint probes run from the machine invoking hlab. HTTP needs a 2xx, TCP a
connection, DNS a resolution:

```yaml
endpoints:
  apps-1:
    - {name: web, kind: http, target: "https://apps.example.test/health"}
    - {name: database, kind: tcp, target: "apps.example.test:5432"}
    - {name: dns, kind: dns, target: "apps.example.test"}
```

## Printers

`hlab printer` covers Moonraker discovery, status, job history, Spoolman status,
component maintenance, and Klippee workspaces for Klipper printers, including
Voron and QIDI. Firmware commands include stock-QIDI upgrades using reviewed
release profiles (initially Plus4 1.8.2 → 2.1.1) and Plus4 mainline migration.

```bash
hlab printer discover scan --emit    # prints a printerlab: section
hlab printer printers
hlab printer status
hlab printer firmware releases
hlab printer firmware update --target printer.local --check
hlab printer firmware update --target printer.local -y
hlab printer firmware update printer-a --version 2.1.1 --check
```

Stock QIDI firmware can be updated directly with `--target HOST`, using existing
SSH access and the newest reviewed release. No enrollment or inventory file is
needed. Add `--version`, `--model`, `--user` or `--identity` when needed.

Printers managed in an inventory are kept in the same `config.yaml`, under
`printerlab:`, with their own `defaults` and `groups`. They are not hosts:
`hlab run all` never reaches a printer. See the
[printer guide](internal/printerlab/README.md).

## Authentication and host keys

Credentials are tried in order: the host's `identityFile` (or
`defaults.identityFile`), then keys in your `ssh-agent`. Passphrase-protected
key files are not unlocked interactively — load them with `ssh-add`.

Host keys are verified against `~/.ssh/known_hosts` (override with
`defaults.knownHostsFile`), and an unknown host is refused. Enrolling records
the key, or connect once with `ssh`. `--insecure-host-key` disables
verification and with it man-in-the-middle protection; it exists only for
bootstrapping machines you physically control.

## Development

```bash
make check      # fmt, vet, test        (.\build.ps1 check on Windows)
make release    # cross-compile into dist/
```

SSH code paths are tested against an in-process SSH server
([internal/sshtest](internal/sshtest/server.go)), so no real machine is needed.
Enrolment tests run against a container and skip unless `HLAB_TEST_SSH_ADDR`
is set; see
[internal/enroll/testdata/enroll-target](internal/enroll/testdata/enroll-target/README.md).
[CLAUDE.md](CLAUDE.md) documents the architecture and the reasoning behind it.
