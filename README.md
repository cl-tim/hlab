# hlab

A single compiled binary for managing a homelab fleet of Linux machines over SSH.

Describe your servers once in a YAML inventory, then act on them individually,
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

## Klipper and QIDI printers

`hlab printer` includes the Printerlab commands for Moonraker discovery,
printer status, Klippee workspaces, and QIDI firmware maintenance.

```powershell
hlab printer printers
hlab printer status
hlab printer discover scan
```

Existing Printerlab installations can use the same printer inventory, journals,
and credentials through `hlab printer`. See the
[printer guide](internal/printerlab/README.md) for configuration and workflows.

## Status

Early, but the core loop works end to end: discover machines, enrol them, then
manage them. The inventory, selector engine, SSH transport, parallel executor,
both halves of discovery and interactive enrolment are complete and tested. The
command surface is currently `discover`, `enroll`, `sync`, `repair`, `hosts`,
`ping`, `run` and `config`.

## Install

Requires Go 1.26+. A newer toolchain is fetched automatically if `go.mod`
asks for one.

From a checkout, on Windows:

```powershell
.\build.ps1 install     # -> %LOCALAPPDATA%\Programs\hlab, added to your PATH
.\build.ps1 uninstall
```

On Linux or macOS:

```bash
make install            # -> ~/.local/bin
sudo make install PREFIX=/usr/local
make uninstall
```

By default both install for the current user and need no administrator
rights. Open a new terminal afterwards, then check with `hlab version`.
Re-running the install replaces the binary in place, including on Windows
while hlab is running.

Or with Go alone, into `$(go env GOPATH)/bin`:

```bash
go install github.com/cl-tim/hlab/cmd/hlab@latest
```

To build without installing: `make build` or `.\build.ps1` (-> `bin/`).

## Getting started

If you already have machines running and no inventory yet, start by finding
them:

```bash
hlab discover                    # sweeps your local subnets
hlab discover 192.0.2.0/24     # or a range you name
```

```console
ADDRESS         PORT   HOSTNAME      OS       SSH              STATUS
192.0.2.1     22     -             -        Server           new
192.0.2.10    22     nas-01        Debian   OpenSSH_10.0p2   new
192.0.2.20    22     docker-01     Ubuntu   OpenSSH_9.6p1    new
192.0.2.30    22     pi-01         Debian   OpenSSH_10.0p2   new

4 responded, 4 not in inventory
```

Then turn the new ones into inventory entries:

```bash
hlab discover 192.0.2.0/24 --new --emit > homelab.yaml
```

Review that file, set the right `user` for each machine, and you have an
inventory. See [Discovery](#discovery) for the details.

Alternatively, write a starter config by hand:

```bash
hlab config init
```

That drops a commented file in hlab's home directory — see
[Where hlab stores things](#where-hlab-stores-things).

Edit it to describe your fleet — see [examples/homelab.yaml](examples/homelab.yaml):

```yaml
defaults:
  user: root
  port: 22
  connectTimeout: 10s
  concurrency: 10

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
  storage: [nas-01]
```

Then check everything is reachable:

```bash
hlab ping all
```

## Where hlab stores things

Everything hlab owns — the config, its management key, and any state it grows
later — lives in one directory under your home:

```console
$ hlab config home
/home/alice/.config/hlab          # %AppData%\hlab on Windows
```

```
config.yaml     the inventory, plus what scans have found
id_hlab         hlab's management private key
id_hlab.pub     its public half, for reference
```

The config is created automatically the first time any command needs it, so
nothing has to be set up before running `hlab discover`.

**The working directory is never searched.** That is deliberate: hlab is often
run from inside a checkout, and one of these files is a private key that is
root-equivalent on every enrolled machine. Nothing it stores should be one
`git add -A` away from being published.

A per-project inventory is still perfectly possible — it just has to be asked
for, rather than picked up implicitly:

```bash
hlab --config ./homelab.yaml hosts list
HLAB_CONFIG=./homelab.yaml hlab hosts list
```

Even then the management key stays in hlab's home directory. Point `--key`
somewhere else if you really want it moved, but think about where that is.

To relocate everything (a synced dotfiles directory, a separate profile per
lab), set `$HLAB_HOME`:

```bash
HLAB_HOME=~/lab/hlab hlab config home
```

`hlab config show` reports every resolved path if you are unsure what is in use.

## Target selectors

Every command that touches a machine takes a selector. Terms are comma
separated and unioned; a `!` prefix subtracts.

| Term       | Matches                                           |
| ---------- | ------------------------------------------------- |
| `all`      | every host in the inventory                       |
| `@group`   | every member of a named group                     |
| `#tag`     | every host carrying a tag                         |
| `nas-01`   | one host by name                                  |
| `docker-*` | host names matching a glob                        |
| `!term`    | subtract these matches from the set               |

Exclusions are applied after all inclusions, wherever they appear:

```bash
hlab run 'all,!#retired' -- uptime
hlab run '@compute,!docker-02' -- 'systemctl restart caddy'
```

A term that matches nothing is an error rather than an empty run, so a typo
fails loudly instead of quietly doing nothing.

## Discovery

`hlab discover` sweeps a range of addresses for machines with SSH open,
reads each server's identification banner, resolves its name, and cross-checks
the result against your inventory so you can see what is not yet managed.

In a terminal, it offers to enroll unmanaged machines after the scan. Use
`--scan-only` to scan without prompts. JSON, YAML, and noninteractive runs
also scan only by default.

Targets can be given several ways, and combined:

```bash
hlab discover                              # local subnets, auto-detected
hlab discover 192.0.2.0/24               # a CIDR block
hlab discover 192.0.2.10                 # a single address
hlab discover 192.0.2.10-192.0.2.50    # a range
hlab discover 192.0.2.10-50              # last-octet shorthand
hlab discover 192.0.2.0/24 198.51.100.0/24   # several at once
```

Everything a scan finds is recorded, so the tool builds a picture of the network
across runs — and can tell you what has changed since last time:

```console
$ hlab discover 192.0.2.0/24
Recorded 10 new, 0 changed, 0 unchanged in ~/.config/hlab/config.yaml

ADDRESS         PORT   HOSTNAME      OS       SSH              MANAGED   SINCE LAST SCAN
192.0.2.1     22     -             -        Server           no        new
192.0.2.10    22     nas-01        Debian   OpenSSH_10.0p2   no        new
...

10 responded, 0 managed, 10 unmanaged, 10 new, 0 changed
```

```console
$ hlab discover 192.0.2.0/24        # a week later
ADDRESS         PORT   HOSTNAME      OS       SSH              MANAGED   SINCE LAST SCAN
192.0.2.10    22     nas-01        Debian   OpenSSH_10.0p2   yes       unchanged
192.0.2.30    22     pi-01         Debian   OpenSSH_10.0p2   no        ssh: SSH-2.0-OpenSSH_9.2p1 -> SSH-2.0-OpenSSH_10.0p2
192.0.2.99    22     newthing      Debian   OpenSSH_9.6p1    no        new

10 responded, 4 managed, 6 unmanaged, 1 new, 1 changed
```

Changes to the reverse DNS name, detected OS and SSH banner are all reported —
so a machine that quietly got rebuilt, or an SSH version that moved, shows up.

Pass `--save=false` for a one-off scan that records nothing.

### One list, with a managed flag

Scanned machines and managed machines live in the same `hosts:` list,
distinguished by `managed:`. Each record accumulates everything hlab has learned
about that machine:

```yaml
hosts:
  - name: nas-01
    address: 192.0.2.10
    managed: true                 # hlab has credentials for this one
    user: hlab
    identityFile: ~/.config/hlab/id_hlab
    tags: [storage]
    hostname: nas-01.lan
    os: Debian
    banner: SSH-2.0-OpenSSH_10.0p2 Debian-7+deb13u4
    firstSeen: 2026-07-27T11:13:12+10:00
    lastSeen: 2026-07-28T09:02:44+10:00

  - name: host-192-0-2-1
    address: 192.0.2.1
    managed: false                # seen on the network, not enrolled
    banner: SSH-2.0-Server
    firstSeen: 2026-07-27T11:13:12+10:00
    lastSeen: 2026-07-28T09:02:44+10:00
```

**Selectors only ever resolve to managed machines.** `hlab run all -- uptime`
targets the machines hlab can actually reach, never the printer it noticed on
the network. If a selector also matched unmanaged machines, it says so rather
than narrowing silently:

```console
$ hlab run '#debian' -- uptime
Skipping 2 unmanaged machine(s): host-192-0-2-1, printer-1
```

`hlab hosts list` shows the whole picture; `--managed` and `--unmanaged` narrow
it.

A host entry written by hand with no `managed:` key is treated as managed —
listing a machine in your config is what it means to manage it. Only hlab writes
`managed: false`.

Enrolment **promotes a record in place**: the machine a scan recorded as
`host-192-0-2-26` becomes `pi-dns` with `managed: true` and credentials
attached, keeping its `firstSeen` and everything else the scans learned. You do
not end up with two entries for one machine.

Useful flags:

| Flag               | Effect                                                    |
| ------------------ | --------------------------------------------------------- |
| `--ports`          | Ports to probe (default `22`)                              |
| `--save`           | Record what was found in the config (default true)          |
| `--new`            | Show only machines missing from the inventory              |
| `--emit`           | Print inventory YAML for the new machines                  |
| `--probe-timeout`  | Per-address TCP connect timeout (default 1s)               |
| `--banner-timeout` | Wait for an SSH banner once connected (default 2s)         |
| `--no-resolve`     | Skip reverse DNS                                           |
| `--max-targets`    | Refuse to scan a range larger than this (default 65536)    |

Scanning is far more concurrent than the SSH commands — 256 probes at once by
default, since these are cheap TCP connects rather than SSH sessions. Override
with `-c`. A `/24` takes a second or two; scans of more than 512 addresses
report progress on stderr.

### Enrolling machines

Run `hlab discover [target]` in a terminal to scan and choose which machines to
enroll. At each prompt, answer `y` to enroll, `n` to remember the machine as
ignored, `s` to skip it for this run, or `q` to quit. Ignored machines stay
visible in scan results and are skipped in future enrollment prompts. The
config's `ignored` list stores their SSH addresses and ports. Use
`--include-ignored` to offer them again, or remove an entry from the list to
clear that decision. Add `--sync` to save the first inventory snapshot after
enrolling.

If a previous scan already recorded the machine, use
`hlab enroll <inventory-name> --sync` to enroll it by name without scanning
again. Run `hlab enroll --sync` to go through all saved unmanaged machines.
`--sync` requires saving the inventory entry, which is the default.

The legacy `hlab discover scan` command remains scan-only unless you pass
`--enroll` or `--adopt`.


`hlab enroll` walks machines one at a time and asks about each. With no
arguments it works through whatever earlier scans recorded, so there is no need
to rescan:

```console
$ hlab enroll
10 machine(s) discovered but not managed.

[1/10] 192.0.2.10  pi  Debian  OpenSSH_10.0p2  last seen 2h ago
  Bring this machine under management? [y/n/q] y
  Inventory name: [pi] pi-dns
  Description (optional): Pi-hole DNS and DHCP
  Create a dedicated "hlab" account, or use credentials you already have? [c/e] c
  SSH username: tim
  SSH password (blank to use your ssh-agent or key):

  192.0.2.10:22 is not in ~/.ssh/known_hosts.
    key type:    ssh-ed25519
    fingerprint: SHA256:k04jKdIcwBqq0+sj50ZIXY4onh63+QFb3DIA9DDngjU
  Accept only if this matches the machine you expect; a password is about to be sent to it.
  Trust this host key? [y/N] y
  connecting...
  connected as tim
  created account hlab
  installed /etc/sudoers.d/60-hlab
  installed hlab's key in authorized_keys
  verified login as hlab with hlab's key
  verified passwordless sudo
  recorded in ~/.config/hlab/config.yaml

[2/10] 192.0.2.1  -  Server  last seen 2h ago
  Bring this machine under management? [y/n/q] n
  skipped
```

Answer `q` at any point to stop; anything you already enrolled is kept.

From then on hlab uses its own account and key — you never type that password
again:

```bash
hlab ping '#managed'
hlab run '#managed' -- uptime
```

You can also name machines directly, including one on a non-standard port, or
chain straight off a scan with `--adopt`:

```bash
hlab enroll 192.0.2.10
hlab enroll 198.51.100.5:2222
hlab enroll 192.0.2.0/24               # scans first, then asks about each
hlab discover 192.0.2.0/24 --adopt
```

### Two ways in

When you accept a machine, hlab asks how it should get access:

- **Create a dedicated account** (`c`, the default) — provisions the management
  account described below. hlab ends up with its own credentials, independent of
  yours.
- **Use credentials you already have** (`e`) — for a machine that is already
  keyed up, or one that must not be modified. hlab asks for the username and
  either a private key or a password, connects to confirm it works, and records
  that. **Nothing on the machine is changed**: no account, no sudoers file, no
  authorized_keys entry.

Adoption still verifies before recording, so a machine is never listed as
manageable on the strength of an assertion. It also reports whether the account
has passwordless sudo, since without it `hlab sync` will see less.

Pass `--provision` to skip the question and always create the account.

| Flag                 | Effect                                                       |
| -------------------- | ------------------------------------------------------------ |
| `--management-user`  | Account to create (default `hlab`)                            |
| `--sudo`             | `nopasswd` (default), `password`, or `none`                   |
| `--provision`        | Always create the account; don't ask about existing credentials |
| `--key`              | hlab's management key (default: in hlab's home directory)     |
| `--write-config`     | Record enrolled machines in the config (default true)         |

**What enrolment does to a machine.** It creates the account, locks its
password so it is key-only, writes `/etc/sudoers.d/60-hlab`, and appends hlab's
public key to that account's `authorized_keys`. It then disconnects and
reconnects as the new account to prove the machine is genuinely manageable
rather than merely reporting success. Re-running is idempotent: an existing
account and an already-authorised key are reported, not duplicated.

The sudoers fragment is written to a temporary file and checked with
`visudo -c` before being moved into place, because installing a malformed
sudoers file can make `sudo` unusable on the machine.

**About the credentials you type.** They are used for the enrolling connection
only. They are never written to the config, never passed on a command line, and
never stored. The sudo password travels on stdin, so it does not appear in the
remote process list. Enrolment refuses to run unless stdin is a terminal, since
a password cannot be read without echo otherwise.

**About the host key.** Enrolment sends a password to a machine hlab has not
spoken to before, so an unrecognised host is a real risk. The fingerprint is
shown and must be accepted explicitly; accepted keys are recorded in
`known_hosts`. A host that is already recorded under a *different* key is
refused outright and never offered for acceptance — that is the case host key
checking exists to catch.

> **`--sudo nopasswd` is the default, and it is a real trade-off.** It is what
> makes unattended management work, and it means anyone who obtains hlab's
> private key has root on every enrolled machine. Keep that key protected, or
> use `--sudo password` (privileged commands then need a password) or
> `--sudo none` (a read-only account, enough for `hlab sync`).

### Bringing machines under management

`--emit` writes a YAML fragment rather than editing your config, so you decide
what to keep:

```console
$ hlab discover 192.0.2.0/24 --new --emit
# Discovered by `hlab discover 192.0.2.0/24` on 2026-07-27 00:46.
# Review each entry and set the correct user, then merge into your config.
hosts:
  - name: pi
    address: 192.0.2.10
    tags: [discovered, debian]
    description: SSH-2.0-OpenSSH_10.0p2 Debian-7+deb13u4
  - name: docker
    address: 192.0.2.20
    tags: [discovered, ubuntu]
    description: SSH-2.0-OpenSSH_9.6p1 Ubuntu-3ubuntu13.16
```

Names come from reverse DNS where available and fall back to the address;
collisions with each other and with your existing inventory are resolved with a
numeric suffix. Every entry is tagged `discovered` plus its detected OS, so you
can select on them straight away:

```bash
hlab ping '#discovered'
hlab run '#debian' -- 'uname -r'
```

You still need to set the correct `user` per machine — discovery cannot know it,
because it never authenticates.

A machine already in your inventory is reported as `known` and excluded from
`--emit`. Matching works even when the inventory refers to a host by DNS name
rather than IP.

## Keeping hosts in line

`hlab repair` pushes what hlab currently expects onto each managed machine: the
management account, its sudoers fragment, and its authorised key. It is
idempotent and needs no input, so it can be scheduled.

```console
$ hlab repair
Repairing 6 host(s)...

HOST      ACCOUNT   SUDO         KEY           RESULT
docker    present   reasserted   present       ok
pi-2      present   reasserted   reinstalled   repaired
pi-dns    present   reasserted   present       ok

3/3 ok, 1 needed repair
```

Use it after hand-editing a machine, or after upgrading hlab, to bring hosts
back to the expected state. `--sudo` re-asserts a different privilege level
across the fleet, which is how you'd tighten `nopasswd` to `password` later.

**It normally connects as the management account**, which means it cannot fix a
host where that account is already locked out — no key, no sudo, no way in. It
says so rather than failing vaguely:

```console
$ hlab repair
  passwordless sudo is not available for this account: sudo: a password is required
  The hlab account is locked out, so repair cannot fix it from the inside. Come in
  through an administrative account instead:
    hlab repair rep-box --as <your-admin-user>
```

`--as` comes in through an account of your own and rebuilds the management
account from the outside — the rescue path. It asks for a password, so it needs
a terminal:

```bash
hlab repair pi-dns --as tim
```

## Removing a machine from management

`hlab unenroll` revokes hlab's access before it touches the inventory: it
removes hlab's key from every account on the machine and the passwordless sudo
rule enrolment installed, then proves the key is refused. Only then is the
record changed — kept as an unmanaged device, or deleted with `--forget`.

```bash
hlab unenroll pi-3                                   # preview
hlab unenroll pi-3 --apply
hlab unenroll pi-3 --forget --remove-account --apply # sold or given away
hlab unenroll dead-pi --offline --forget --apply     # already gone
```

`--remove-account` deletes the management account too, but only one hlab
created; on an adopted machine the account is yours and is left alone.
`--offline` revokes nothing, since nothing can be reached — the key stays
authorised on that machine, so wipe it if it ever turns up again. Services the
machine runs, and `/opt/hlab`, are not touched either way.

## Syncing managed hosts

`hlab sync` refreshes what hlab knows about the machines it manages: it connects
to each one, collects a snapshot, and writes it back. With no selector it covers
the whole managed fleet.

```bash
hlab sync                 # every managed machine
hlab sync '#debian'        # narrow it
hlab sync --emit-tags     # suggested tags from what each host runs
```

Unmanaged machines are never contacted: there are no credentials to connect
with. Use `hlab discover` to find those, and `hlab enroll` to bring them
in.

Once machines are under management, `hlab sync` refreshes what hlab knows about
them, and answers the other half of the discovery question — what is each box actually running?

With no selector it covers your whole managed fleet. Unmanaged machines are
never contacted: there are no credentials to connect with, so a selector that
also matches one reports it as skipped rather than trying.

```console
$ hlab sync

HOST        OS                 KERNEL           UPTIME   CPU   MEM   DISK   SVCS   PORTS   CTRS
docker-01   Ubuntu 24.04.4     6.8.0-51         14d 3h   8     41%   62%    52     14      11
nas-01      Debian GNU/Linux   6.1.0-18-amd64   61d 7h   4     75%   11%    47     8       0

=== docker-01 ===
  listening:
    22     all interfaces   sshd
    80     all interfaces   caddy
    5432   localhost        postgres
  containers:
    caddy                    caddy:2                          Up 3 days
    grafana                  grafana/grafana:11               Up 2 weeks
  services: caddy, containerd, cron, docker, ssh, systemd-journald, +46 more
  disks:
    /                    ext4      62% used  118.4G free
```

It collects the OS and kernel, architecture, CPU and memory, networking, real
filesystems, running services, listening TCP ports and any Docker or Podman
containers.

| Flag           | Effect                                                     |
| -------------- | ---------------------------------------------------------- |
| `--emit-tags`  | Print inventory tags derived from what each host runs        |
| `--save`       | Record what was collected (default true)                    |

### Where collected data goes

A collection is written to two places, because the two halves have different
lifetimes:

**Durable attributes go on the host record in `config.yaml`** — kernel,
architecture, CPU count, memory, networking, and when it was last collected.
These describe the machine itself, they change rarely, and they are small enough
to keep the config readable:

```yaml
  - name: docker
    address: 192.0.2.20
    managed: true
    user: hlab
    os: Ubuntu 24.04.4 LTS
    kernel: 6.8.0-51-generic
    arch: x86_64
    cpus: 8
    memoryMB: 15898
    network:
      gateway: 192.0.2.1
      dns: [192.0.2.10]
      search: [lan]
      interfaces:
        - name: eth0
          mac: aa:bb:cc:11:22:33
          mtu: 1500
          up: true
          addresses: [192.0.2.20/24, 'fe80::a8bb:ccff:fe11:2233/64']
    containers:
      - runtime: docker
        name: plex
        image: plexinc/pms-docker
        status: Up 3 hours (healthy)
    lastCollected: 2026-07-27T12:18:13+10:00
```

**Volatile state goes in `facts.yaml`** next to it — running services, listening
ports, disks, uptime and load. That file is machine-owned and grows
freely; nothing there is meant to be hand-edited. Collecting from one machine
leaves every other machine's entry untouched, and hosts are written in sorted
order so the file diffs cleanly between runs.

Keeping them apart means `config.yaml` stays something you can open and reason
about, while the full picture is still captured.

### Networking

Each collection records how the machine is attached: gateway, DNS servers and
search domains, what owns the network configuration, and every interface with
its kind, MAC, MTU, link state, speed, addressing mode and addresses.

```console
  network:  (managed by netplan)
    eth0       ethernet  up 1G           static 192.0.2.10/24
    wlan0      wifi      up              dhcp   192.0.2.90/24
    gateway    192.0.2.1
    note: wifi (wlan0) is active while ethernet (eth0) is carrying a link
```

**Interface kind** comes from `/sys`, not from the name — `phy80211` for
wireless, a `bridge/` directory for bridges, a `device` symlink for physical
NICs. Naming conventions are a guess; this is not.

**Link state is two separate things.** `up` is the administrative state;
*carrier* is whether a link is actually present — a cable plugged in, or a
wireless association. An interface can be up with no carrier, and that
distinction is the difference between a working connection and a dead port.

**DHCP versus static** is read from the kernel's `dynamic` flag on each address
rather than from config files. A leased address is flagged dynamic with a finite
lifetime; a static one reports `valid_lft forever`. That works the same whether
the machine runs NetworkManager, netplan, dhcpcd or ifupdown.

**Manager** records what you would *edit* to change the addressing. On Ubuntu
that is netplan even though systemd-networkd or NetworkManager is what actually
runs — editing those directly gets overwritten on the next `netplan apply`.

MAC addresses are worth having beyond diagnostics — they identify the hardware
across DHCP changes, so a machine that moves address can still be recognised.

Loopback is dropped, and so are the `veth*` pairs a container host sprouts per
container, which would otherwise swamp the record. Bridges like `docker0` and
`br-*` are kept: they describe how the machine is actually wired.

How it works, and what that implies:

- **One round trip per host.** A single POSIX `sh` script gathers everything, so
  a fleet-wide snapshot costs one SSH session per machine.
- **It only reads.** Nothing is installed, started or configured.
- **It uses sudo only where that changes the answer.** Process names behind
  listening sockets, and containers on a host where the management account is
  not in the docker group, are invisible otherwise. Everything else is read
  unprivileged. Sudo is best-effort: a machine reachable only as an
  unprivileged account still reports what it can, and says so.
- **It never modifies anything.** Nothing is installed, started or configured.
- **It degrades instead of failing.** Every tool is probed with `command -v`
  first, so systemd and OpenRC both work, `ss` falls back to `netstat`, and a
  minimal box simply reports fewer sections.

Ports bound several times by one service (the usual IPv4 plus IPv6 pair) are
collapsed into a single entry, keeping the widest binding, so the count reflects
services rather than sockets and `localhost` never hides something that is
actually reachable.

### Deriving tags from what is running

`--emit-tags` turns the snapshot into inventory tags:

```console
$ hlab sync all --emit-tags
# Suggested tags, derived from what each host is running.
# Merge these into the matching host entries in your config.
# docker-01
    tags: [docker, ubuntu, web, x86_64]
# nas-01
    tags: [aarch64, debian, fileserver, zfs]
```

The OS id, architecture and container runtimes come through directly;
well-known services map to a role (`nginx`/`caddy` → `web`, `smbd` → `fileserver`,
`unbound` → `dns`, and so on). The mapping is deliberately small — an
unrecognised service contributes no tag, because a wrong tag is worse than none.

## Commands

| Command                          | Purpose                                        |
| -------------------------------- | ---------------------------------------------- |
| `hlab discover [target]`     | Sweep the network for machines with SSH open   |
| `hlab enroll [address...]`        | Bring machines under management interactively  |
| `hlab sync [selector]`            | Refresh what hlab knows about managed hosts    |
| `hlab repair [selector]`          | Re-assert hlab's account on managed hosts      |
| `hlab unenroll <host>`            | Revoke hlab's access and stop managing a host  |
| `hlab hosts [selector]`           | List every known machine, managed or not       |
| `hlab hosts groups`               | List groups and their members                    |
| `hlab hosts rename <old> <new>`   | Rename a machine's inventory record              |
| `hlab ping <selector>`          | Verify reachability, host key trust and auth     |
| `hlab run <selector> -- <cmd>`  | Run a command everywhere, in parallel            |
| `hlab config init`              | Write a starter config                           |
| `hlab config path`              | Show which config file is in use                 |
| `hlab config home`              | Show the directory hlab stores its data in       |
| `hlab config show`              | Show the resolved config with defaults applied   |
| `hlab version`                  | Print version and build metadata                 |

Global flags:

| Flag                     | Effect                                              |
| ------------------------ | --------------------------------------------------- |
| `--config`               | Config file path (else `$HLAB_CONFIG`, else the default) |
| `-o, --output`           | `table` (default), `json`, `yaml`                    |
| `-c, --concurrency`      | Max hosts acted on at once (default 10)              |
| `--connect-timeout`      | SSH connect timeout (default 10s)                    |
| `--insecure-host-key`    | Skip host key verification — bootstrapping only      |

### Running commands

Put the command after `--` so hlab does not try to parse its flags:

```bash
hlab run all -- uptime
hlab run @storage -- 'df -h /'
hlab run '#debian' --confirm -- 'apt-get update && apt-get -y upgrade'
```

The words after `--` are joined and handed to the remote login shell, so pipes,
redirects and `&&` work. Quote the whole command to protect it from your local
shell. `--confirm` lists the target hosts and asks before proceeding.

`hlab run` and `hlab ping` exit non-zero if any host fails to connect or the
command exits non-zero anywhere, so they drop straight into scripts and CI.

### Structured output

```bash
hlab -o json run @compute -- 'free -m' | jq -r '.[] | "\(.host): \(.exitCode)"'
hlab -o json hosts list '#docker' | jq -r '.[].address'
```

In JSON and YAML modes the per-host stdout blocks and summary line are
suppressed; you get one array of result objects and nothing else.

## Backup, service health, and maintenance

- `hlab backup [selector]` validates the checksum and reads each new archive
  before saving it. Use `backup verify` later to check archives at rest.
- `hlab stack deploy apps-1 web --apply --sync` deploys the Compose file from
  `stacks/web/compose.yaml` under hlab's home and refreshes that host's
  inventory afterward. Use `--with` for any extra files you want to ship
  alongside Compose. Existing `--file` and `--name` invocations still work.
- `hlab update @servers --apply --reboot --rolling` also handles reboot requests
  first raised during package installation, then checks each updated host
  before moving to the next one.

- `hlab backup verify @servers --max-age 168h` checks the newest per-host
  archive, its checksum, and whether it can be read. Use `--dir` for backups
  stored outside the default directory. Missing or stale backups fail the check.
- `hlab check @servers` also runs endpoints configured for the selected
  hosts. Probes run from the machine invoking hlab and time out after five
  seconds. HTTP requires a 2xx response, TCP requires a connection, and DNS
  resolves a name using the invoking machine's resolver.
- `hlab stack diff apps-1 web --file ./compose.yaml` compares the local file's
  checksum with the deployed Compose file. Without `--file`, it uses
  `stacks/<project>/compose.yaml` under hlab's home. Omit the project to compare
  all running hlab-managed projects. File contents are kept out of the report.
- `hlab update @servers --apply --rolling` verifies each updated host and its
  configured endpoints before moving on. A failed or incomplete health check
  stops the rollout.
- `hlab hosts history apps-1` shows the last 100 observed kernel, interface
  address, and running service changes for that host. A saved, complete
  `hlab sync` establishes the baseline on its first run.
- `hlab backup drill drill-1 --from ./backup.tar.gz` previews a recovery drill.
  Tag the disposable host `recovery-drill`, configure at least one endpoint
  pointing at its restored services, and keep it isolated from production.
  With `--apply`, the drill replaces the host's hlab state, starts the restored
  stacks, and checks the host and endpoints.

Configure probes under the top-level `endpoints` key, grouped by inventory host.

```yaml
endpoints:
  apps-1:
    - {name: web, kind: http, target: "https://apps.example.test/health"}
    - {name: database, kind: tcp, target: "apps.example.test:5432"}
    - {name: dns, kind: dns, target: "apps.example.test"}
```

## Authentication and host keys

Credentials are tried in this order:

1. The host's `identityFile`, or `defaults.identityFile`.
2. Keys loaded in your `ssh-agent` (the OpenSSH named pipe on Windows,
   `$SSH_AUTH_SOCK` elsewhere).

Passphrase-protected key files are not unlocked interactively — load them into
your agent with `ssh-add` instead.

Host keys are verified against `~/.ssh/known_hosts` (override with
`defaults.knownHostsFile`). If a host is not in that file the connection is
refused. Connect once with `ssh` to record the key. `--insecure-host-key`
disables verification entirely; it defeats man-in-the-middle protection and
exists only for first-run bootstrapping of machines you physically control.

## Layout

```
cmd/hlab/            entry point
internal/cli/        cobra command tree, output rendering, exit codes
internal/config/     config file loading, defaults resolution
internal/inventory/  host model, groups, selector engine
internal/discover/   network sweep, banner fingerprinting, inventory emission
internal/enroll/     management key, remote bootstrap script, enrolment flow
internal/facts/      remote collection script, snapshot parsing, tag inference
internal/sshx/       SSH dialling, auth, host key policy, parallel executor
internal/sshtest/    in-process SSH server used by tests
internal/output/     table / JSON / YAML rendering
internal/buildinfo/  version metadata stamped at link time
```

`internal/sshx` has no dependency on `internal/inventory` — the CLI maps between
them — so the transport and the fleet model can evolve independently.

## Development

```bash
make check      # fmt, vet, test
make test
make release    # cross-compile into dist/
make help       # list targets
```

On Windows use `.\build.ps1 check`, `.\build.ps1 test`, and so on.

The SSH code paths are tested against an in-process SSH server
([internal/sshtest](internal/sshtest/server.go)), so the happy path, non-zero
exits, fan-out, concurrency limiting and cancellation are all covered without
needing a real machine.

Enrolment is tested against a real container running `sshd` and `sudo`, because
`useradd`, `visudo` validation and `authorized_keys` permissions cannot be
verified any other way. See
[internal/enroll/testdata/enroll-target](internal/enroll/testdata/enroll-target/README.md);
those tests skip unless `HLAB_TEST_SSH_ADDR` is set.

Fact parsing is tested against output captured from a real Ubuntu system by
running `CollectScript` verbatim
([internal/facts/testdata](internal/facts/testdata/ubuntu-wsl.txt)), alongside
hand-written fixtures for the cases that machine cannot produce: OpenRC,
`netstat` fallback, dual-stack port collapsing, and truncated output.
