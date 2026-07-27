# hlab

A Go CLI for managing a homelab fleet of Linux machines over SSH. Single
compiled binary, cross-compiled to Linux/macOS/Windows.

## Build and test

Go lives at `C:\Program Files\Go\bin` and may not be on PATH in a fresh shell.

```bash
go build ./...
go test ./...
go vet ./...
```

Windows has no `make`; use `.\build.ps1 <task>` (build, test, check, release).
The `Makefile` is for CI and Linux checkouts. `make race` / `go test -race`
needs cgo, which is unavailable on this machine, so it cannot be run here.

There is no CI: GitHub Actions was removed deliberately to avoid spending
minutes on a private repo. Run `go vet`, `go test` and the cross-compile sweep
locally before pushing — nothing else will.

## Architecture

```
cmd/hlab/            entry point; maps ExitError to a process exit code
internal/cli/        cobra command tree, result rendering, exit codes
internal/config/     YAML loading, defaults resolution, ~ expansion
internal/inventory/  host model, groups, selector engine
internal/discover/   network sweep, banner fingerprinting, inventory emission
internal/enroll/     management key, remote bootstrap script, enrolment flow
internal/facts/      remote collection script, snapshot parsing, tag inference
internal/netcfg/     static addressing, wifi disable, dead-man rollback
internal/stack/      compose deploy, pull, and adoption of hand-made stacks
internal/sshtest/    in-process SSH server used by tests (not linked into hlab)
internal/sshx/       SSH dialling, auth, host key policy, parallel executor
internal/output/     table / JSON / YAML rendering
internal/buildinfo/  version metadata stamped via -ldflags
```

Key boundary: `internal/sshx` does not import `internal/inventory`. It defines
its own `Target` struct and the CLI maps between the two, so the transport and
the fleet model stay independently changeable. Keep it that way.

## Conventions

- **Adding a command**: new file in `internal/cli/`, a `newXxxCmd(a *app)`
  constructor returning `*cobra.Command`, registered in `NewRootCmd`. Take the
  app struct so global flags and the loaded config are available.
- **Selectors**: any command acting on machines takes a selector string and
  resolves it with `a.selectTargets(expr)`. Never take a bare host list.
- **Output**: build both a `output.Table` and a serialisable view, then call
  `output.Write(w, a.format, view, tbl)`. Check `a.format.Structured()` before
  printing anything decorative — JSON/YAML output must stay machine-parseable.
- **Exit codes**: return `&cli.ExitError{Code: n}` rather than calling
  `os.Exit`. Commands acting on hosts exit 1 if any host failed.
- **Errors**: a host that ran a command and exited non-zero is a *successful
  execution* with a non-zero `ExitCode`, not a `Result.Err`. `Err` means the
  host was unreachable or the command could not be started. Don't conflate them.
- **Config**: the loader uses `KnownFields(true)`, so every new config key needs
  a struct field or existing configs break. Durations use the `config.Duration`
  wrapper, which accepts `"10s"` or a bare integer of seconds.

## Discovery

`internal/discover` is deliberately unauthenticated: it opens and closes TCP
connections and reads the SSH identification banner, nothing more. Keep it that
way — `hlab discover scan` is safe to point at a range precisely because it
cannot log in or change anything.

Two constraints on anything that writes inventory YAML:

- **Never edit the user's config in place.** `--emit` writes a fragment to
  stdout for review. Discovery cannot know the right `user` for a machine, so
  its output is a draft, not a result.
- **Banners are untrusted input.** They are whatever the listening service sends.
  `yamlScalar` strips control characters and quotes the value so a crafted
  banner cannot inject YAML keys; `TestEmitInventoryNeutralisesHostileBanners`
  guards this. Anything else that renders remote-supplied text into a config
  needs the same treatment.

The emitted YAML must survive `config.Load`, which uses `KnownFields(true)`.
`TestEmitInventoryRoundTripsThroughTheConfigLoader` is the contract test; extend
it when adding emitted fields.

Scan concurrency defaults to 256 (`defaultScanConcurrency`), not the global
executor default of 10 — these are cheap TCP connects. The global `-c` flag only
overrides it when explicitly passed, checked via `cmd.Flags().Changed`.

## Where collected data goes

A collection splits across two files, deliberately:

- **`config.yaml`, on the host record** — durable attributes: `kernel`, `arch`,
  `cpus`, `memoryMB`, `network`, `containers`, `lastCollected`. Small,
  slow-changing, and descriptive of the asset. Written by
  `config.UpdateHostAttributes`, which touches only those fields and leaves
  names, credentials and tags alone.
- **`facts.yaml`** (`config.FactsPath()`) — volatile state: services, ports,
  disks, uptime, load. Machine-owned, grows freely, keyed by host name. Written
  by `facts.SaveStore`, which merges so collecting one host never drops
  another's entry, and sorts keys so runs diff cleanly.

Containers are on the **host record**, not in the store: they describe what a
machine is *for*, and a homelab's containers change about as often as its
installed services. They are applied wholesale, including when empty, so a
container that has been removed disappears rather than lingering as a permanent
lie (`TestUpdateHostAttributesRemovesStoppedContainers`).

The split exists so `config.yaml` stays hand-editable. Do **not** move service
or port lists onto the host record — six hosts would add ~500 lines and make the
config unreviewable. `TestSnapshotExcludesDurableAttributes` guards the other
direction: durable attributes must not be duplicated into the store, or the two
can disagree.

`SnapshotOf` is the only place that decides which half a field belongs to.

## Networking

`parseNetwork` reads three sections: `interfaces` (`ip -o link` then
`ip -o addr`, or `ifconfig -a`), `gateway` (`ip route show default`, or
`route -n`), and `dns` (`/etc/resolv.conf`).

`ip -o` output uses a literal backslash-plus-spaces continuation
(`... qlen 1000\    link/ether aa:bb:...`); field splitting handles it, and
`TestParseRealNetworkOutput` pins the exact captured form.

`skipInterface` drops `lo` and `veth*`. The veth exclusion matters: a container
host has one veth per container and they are ephemeral. Bridges (`docker0`,
`br-*`) are kept on purpose — they describe real wiring.

MAC addresses are stored because they identify hardware across DHCP changes.

### Interface kind, link state and addressing

Three things are collected that later features depend on, and each has a
deliberate source:

- **Kind** (`ethernet`/`wifi`/`bridge`/`virtual`/`loopback`) comes from `/sys`:
  `phy80211` for wireless, `bridge/` for bridges, `device` for physical NICs.
  Never infer it from the interface name.
- **`Up` and `Carrier` are separate on purpose.** `Up` is administrative;
  `Carrier` is whether a link actually exists. `Interface.Working()` requires
  both. Conflating them would let a plugged-in-but-dead port look like a
  working connection.
- **DHCP** is read from the kernel's `dynamic` flag on the address, not from
  config files. It is the only signal that holds across NetworkManager,
  netplan, dhcpcd and ifupdown alike.

`NetworkInfo.Manager` records what an operator would **edit**, which is why
netplan outranks the systemd-networkd or NetworkManager it renders for —
editing those directly is undone by the next `netplan apply`.

`WorkingEthernet()` requires up + carrier + **an address**; `WiFiRedundant()`
is true only when active wifi coexists with working ethernet. Anything that
disables wifi must gate on that, and the two negative tests
(`TestWiFiNotRedundantWhenEthernetHasNoCarrier`,
`TestWorkingEthernetRequiresAnAddress`) exist because getting this wrong
strands a machine with no route back in.

## Fact collection

`internal/facts` gathers a host snapshot in **one round trip**: `CollectScript`
is a single POSIX `sh` program whose output is split into `##HLAB#name#`
sections and parsed. Adding a fact means adding a section to the script and a
parser, not another SSH call — keep it that way.

Rules for `CollectScript`:

- **POSIX sh only.** `/bin/sh` is dash on Debian and Ubuntu; bashisms break.
- **Probe before use.** Every external tool goes behind `command -v`, so a
  minimal host reports fewer sections rather than failing.
- **Read-only, but privileged where it matters.** The script must never install,
  start or change anything. It *does* use `sudo -n` for the two things that are
  otherwise invisible: process names behind listening sockets (`ss -tlnp`), and
  the container list on a host where the management account is not in the
  `docker` group.
- **Sudo is best-effort, never required.** `SUDO` is set only when `sudo -n true`
  succeeds, so a machine adopted with an unprivileged account still reports
  everything it can instead of hanging on a password prompt. The `privileged`
  section records which of `root`/`sudo`/`none` applied, and the CLI says so
  when it is `none` — a thin result should never look like a quiet machine.
- **Docker is asked twice**, unprivileged then via sudo, because either can be
  the one that works. `parseContainers` dedupes on runtime+name, so keep that
  dedup if you touch it (`TestParseContainersDeduplicates`).

Parsing is deliberately lenient — an unparseable line is skipped, never fatal.
The one hard signal is the trailing `end` marker: its absence means the output
was truncated and sets `Facts.Partial`.

Ports are collapsed by `(port, process)` because a service normally binds both
IPv4 and IPv6. When collapsing, `ExposureRank` keeps the **widest** binding, so
a merged entry never reports `localhost` for something reachable off-box. This
was a real bug found by running against a live host; `TestParseCollapsingKeeps
WidestBinding` guards it.

`SuggestedTags` maps well-known service names to roles. Keep `serviceRoles`
small and obvious — an unrecognised service yields no tag on purpose, because a
wrong tag is worse than none.

To regenerate the fixture, run `CollectScript` on a real Linux host and save the
output to `internal/facts/testdata/`. Don't assert on values that drift between
captures (uptime, load); pin exact parsing in the hand-written fixtures instead.

## The asset model

There is **one** list of machines, `hosts:`, and each record carries
`managed: true|false`. A record accumulates everything known about a machine:
what scans observed, plus — once enrolled — how to reach it.

The load-bearing rule:

> **`Inventory.Select` returns managed machines only.** It is the sole thing
> standing between `hlab run all` and every device on the network. Unmanaged
> records have no credentials, so targeting them could only fail, and doing so
> silently would be worse. `TestSelectNeverTargetsUnmanagedMachines` guards it.

- `Select(expr) (selected, skipped, err)` — for commands that **connect**.
  Skipped unmanaged matches are returned so the CLI can report them; never drop
  them silently. Errors when matches exist but none are managed.
- `SelectAll(expr)` — for **listing and reporting** only. Never feed its result
  to anything that opens a connection.

`config.MergeObservations` folds scan results in: it enriches existing records,
never flips `managed` to false, and never touches credentials, tags or names. A
scan cannot un-enrol a machine. `describeChanges` produces the before/after
strings that make a rescan able to say "this machine's SSH version moved".

`config.AddHost` is an **upsert keyed on address+port**, so enrolment promotes
the record a scan created rather than duplicating it. `FirstSeen` and observed
fields survive the promotion.

Only the `hosts:` node is rewritten, so comments elsewhere survive; comments
inside it do not, since hlab maintains it. Records sort numerically by IP
(`lessByAddress`), not by address string, which would put .119 before .1.

**`managed:` is a `*bool` on the wire** (`config.hostEntry`) so an absent key is
distinguishable from an explicit false. Absent means **managed**: a hand-written
entry is one the operator intends to manage, and defaulting the other way would
silently make every existing inventory unreachable. Everything hlab writes states
it outright. `TestHandWrittenHostsDefaultToManaged` guards this.

A retired `discovered:` section is still read and folded in by
`migrateLegacyDiscovered`, then dropped on the next write.

`config.EnsureExists` creates a scaffold when no config exists, so no command
needs `hlab config init` first. It is intentionally **not** the `config init`
template: that one has example hosts, and a file conjured as a side effect of an
ordinary command must not invent machines that were never real. Only the default
location is auto-created — an explicit `--config`/`$HLAB_CONFIG` that is missing
still errors, so a typo cannot silently become a new empty config.

## Where data lives

`config.Home()` is the single directory hlab stores anything in: config,
management key, future state. It resolves to `$HLAB_HOME`, else
`os.UserConfigDir()/hlab` (always under the user's home on every OS).

**The working directory is never searched and never written to by default.**
This is a security property, not a preference: hlab is routinely run from inside
a git checkout, and `id_hlab` is root-equivalent on every enrolled machine.
`TestStoredDataNeverDefaultsIntoTheWorkingDirectory` is the regression guard —
if you add anything hlab persists, route it through `config.Home()` and extend
that test.

`config.ManagementKeyPath()` is deliberately **not** derived from the config
file's location. An explicit `--config ./homelab.yaml` must not drag the private
key into the checkout alongside it.

Per-project inventories are still supported, but only explicitly, via `--config`
or `$HLAB_CONFIG`.

## Enrolment

Two modes, both of which **verify before recording** — a machine is never
listed as manageable on an unverified claim:

- `ModeProvision` — creates the account, installs the key, sets up sudo.
- `ModeAdopt` — records credentials the operator already has and changes
  **nothing** on the machine. `TestIntegrationAdoptChangesNothing` runs against
  a fresh container and asserts no account and no sudoers fragment appear.

The interactive walk lives in `cli/enroll.go` and takes `[]candidate`
(`cli/candidate.go`), which is built either from a live `discover.Result` or
from a stored unmanaged `inventory.Host`. `hlab enroll` with no arguments works
through `Inventory.Unmanaged()`, so enrolling never requires a rescan. Keep new
sources of machines flowing through `candidate` rather than adding another
overload.

### Repair

`Enroller.Repair` is convergence, not rescue: it connects **as the management
account** and re-runs the same idempotent bootstrap, so it can push new
expectations onto a drifted or older host. It cannot fix a host where that
account is locked out — no key or no sudo means no way in — and the error says
so, pointing at `hlab repair <host> --as <admin>`.

`--as` is the rescue path: connect with an administrative account and rebuild
the management account from outside. It is just `Enrol` with `ModeProvision`,
which is why the two share everything.

`runPrivileged` uses `sudo -n` when no password is supplied (a key or agent
login, which is how repair connects), rather than feeding a blank line to
`sudo -S` and waiting on a prompt nobody will answer.

`internal/enroll` is the only code that *changes* a machine. Everything about it
is built to be conservative, and those properties are load-bearing:

- **Credentials are never persisted.** The operator's password is used for the
  enrolling handshake and the sudo escalation, then dropped. It travels to sudo
  on **stdin** (`sudo -S` reads the first line, the script follows on the same
  stream), so it never appears in a command line or the remote process list.
  Never add a `--password` flag or put a password in the config.
- **No password without a terminal.** `runEnrollment` refuses when stdin is not
  a TTY, because echo cannot be suppressed otherwise. `-o json/yaml` is refused
  for the same reason — the flow is interactive by nature.
- **Host keys are confirmed, not assumed.** Enrolment sends a password to a
  machine hlab has not met, so `OnUnknownHostKey` shows the fingerprint and
  requires explicit acceptance. A *mismatched* key is refused outright and must
  never reach the prompt (`TestMismatchedHostKeyIsNeverOffered`).
- **The knownhosts verifier snapshots the file at construction**, so
  `tofuVerifier` rebuilds it after recording a key. Without that, a key accepted
  during a run looks unknown again on the next connection and gets appended
  repeatedly. This was a real bug; `TestUnknownHostKeyIsRecordedOnceAccepted`
  guards it.
- **Usernames are validated before interpolation.** `ValidateUsername` is a
  security control, not just usability — the name goes into a shell script.
  Values are also `shellQuote`d.
- **The sudoers fragment is validated with `visudo -c` before being moved into
  place.** A malformed sudoers file can make `sudo` unusable on the machine.
  Never write directly to `/etc/sudoers.d/`.
- **Everything is idempotent.** Re-running reports an existing account and an
  already-authorised key rather than failing or duplicating.
- **Success means verified.** `Enrol` reconnects as the new account with hlab's
  own key and checks `id -un` before reporting success. Don't relax this to
  "the script exited zero".

`--sudo nopasswd` is the default because unattended management needs it, and it
makes hlab's private key equivalent to root on every enrolled machine. The CLI
says so out loud before enrolling. Keep that warning.

Config edits go through `config.AddHost`, which round-trips the document as a
`yaml.Node` so the operator's comments survive, backs up to `.bak`, writes
atomically via rename, and refuses to write anything that would not load back.
This is the one place that edits the user's config — discovery still only emits
drafts to stdout.

## Changing a machine's network

`internal/netcfg` writes configuration in the SSH session but **never brings
the interface up there**. The activation cuts the connection the script is
running over; sshd SIGHUPs the process group and takes `nmcli`/`netplan` with
it, leaving the link half-configured — the address lands and the machine
answers ICMP but never completes a TCP handshake. That reads as "the machine
did not come back", and the dead-man timer then undoes a change that worked.
Whatever touches the link goes in `$BACKUP_DIR/activate.sh` and runs under
`setsid`. `TestActivationIsDetachedFromTheSession` guards it.

For the same reason, a lost connection during an apply is **inconclusive, not
a failure** (`sshx.ConnectionLost`): the reconnect decides. Verification
retries until shortly before the rollback deadline rather than giving up
early, since concluding failure while the machine is still settling gets the
change reverted for nothing.

The NetworkManager backup asks `nmcli` for the profile's own settings rather
than copying `/etc/NetworkManager/system-connections`. Where a profile lives is
a distribution decision — NM built with netplan support keeps it in
`/etc/netplan/90-NM-*.yaml` and generates the keyfile into `/run` — so copying
that directory can yield an empty backup, which is a revert that silently
restores nothing.

`--dns` carries the **search list** with it. Writing resolvers without it
breaks every short name while FQDNs keep working, which is a confusing failure:
the domain had been arriving from DHCP and vanishes the moment the interface
goes static.

## Flat names after the move off dnsmasq

dnsmasq answers a DHCP client's **flat** hostname as well as its FQDN, so
`printer-2` resolved everywhere. Technitium registers leases into a zone, so it
holds `printer-2.example` and nothing at `printer-2`. Ordinary hosts are unaffected —
`search lan` covers it — but Docker gives containers `options ndots:0`, which
makes glibc try a bare name as absolute and never consult the search list.

Anything containerised that addresses another machine by short name therefore
breaks the moment DHCP stops being dnsmasq, and it breaks as a name
resolution error rather than anything pointing at DNS having changed. The fix
is either an FQDN in the config or `dns_opt: [ndots:1]` on the service; the
printfarm stack carries both.

## Updates

`internal/updates` splits into a check that changes nothing that runs and an
apply that is explicit. The check does refresh the package index — asking what
is upgradable against a stale one answers a question nobody asked — but
installs nothing and touches no container.

Two things about the apply are load-bearing:

- **`upgrade`, never `full-upgrade`.** It will not remove a package to satisfy
  a dependency, which is the right default for a machine nobody is standing
  next to. `--force-confold`/`--force-confdef` keep the configuration already
  on the machine; without them dpkg stops on a changed config file and waits
  for an answer that is never coming.
- **`upgrade` alone never installs a kernel.** Bumping `linux-image-generic`
  means installing a new `linux-image-<version>` package, and plain `upgrade`
  refuses to install anything new — so the kernel is held back forever and the
  machine never takes a kernel security fix. `--allow-new` adds
  `--with-new-pkgs`, which permits installs but still never removals. Held-back
  packages are reported for exactly this reason; do not quietly drop them.

Nothing reboots without `--reboot`. When it is given:

- The reboot is **detached** (`setsid`), for the same reason network changes
  are — it takes down the connection the script runs over.
- It is **conditional**: a machine that has not asked for one is left alone, so
  the flag is safe to pass to a whole selector.
- It is **proved by the boot id**, not by the machine answering SSH again. A
  reboot that silently failed passes an "is it back?" test instantly;
  `/proc/sys/kernel/random/boot_id` changes only when the kernel starts.
- It is **refused when the machine is the only recorded resolver** for another
  managed machine. hlab connects by address so it never notices the breakage
  itself, which is exactly why this is checked rather than discovered later.
  A machine resolving through a stub (127.0.0.53) hides its real upstreams, so
  it cannot be judged and is not counted — the check proves what it can.

The work a machine needs is decided per kind, not from the package count: one
with nothing pending but a reboot already outstanding is precisely what
`--reboot` is for, and an earlier version skipped exactly that case.

The container half is deliberately narrow: it refreshes only stacks under
`/opt/hlab/stacks`, because a hand-made project elsewhere has no reviewed
compose file locally and pulling it would act on a definition nobody has read.
The *check* compares each running container against its own local image, which
is a pure local read — it detects "the image moved but the container did not",
not "the registry has something newer".

## Backup

`internal/backup` archives `/opt/hlab/stacks` — the compose files and, far more
importantly, the data beside them: a DNS zone, a DHCP lease database, service
credentials, printer configuration. On this fleet that is a couple of
megabytes sitting on an SD card, and it is the whole network's name resolution.

Bulk data mounted from elsewhere is deliberately excluded. A media library is
large, is not hlab's, and including it would turn a cheap safety net into
something nobody runs.

The load-bearing property is **verification at both ends**:

- The machine checksums the file it wrote; hlab checks the digest *and* the
  byte count after transfer. A truncated archive decodes into a plausible-
  looking tar and fails only when somebody tries to restore from it, which is
  the worst possible moment to find out.
- The digest is stored beside the archive, so a restore proves the file on disk
  is still the one that was taken.
- Base64 rather than raw bytes on the wire: the archive shares stdout with
  whatever else the shell might emit.

`restore` order is also load-bearing: verify the checksum, stop the stacks so
their data does not move underneath them, copy the current state aside under
`/var/lib/hlab`, unpack, start again. A restore from the wrong archive is
therefore reversible. The checksum is interpolated into a root shell script, so
it is validated as a hex digest rather than trusted to be one.

A machine with no `/opt/hlab/stacks` returns `ErrNothing`, not an error — most
machines in a fleet run no stacks, and failing on them would make a healthy run
exit non-zero and be useless to anything scheduled.

**Not covered:** hlab's own home — `config.yaml` and `id_hlab`. Losing the
management key means losing access to every enrolled machine, and it lives on
the operator's workstation, not on the fleet.

## systemd-networkd

The unit hlab writes is named `05-hlab-<iface>.network`, and the prefix is
load-bearing. systemd-networkd applies the **first** `.network` whose `[Match]`
fits, in lexical order across every directory it reads — and a machine can
already have a generated `10-netplan-*.network` in `/run` for the same
interface. A file that does not sort ahead of that one is written, looks
correct, and does nothing.

Note that `/etc/netplan` existing does not mean netplan owns the machine.
Armbian ships a netplan YAML with `renderer: networkd` and no netplan binary
installed, which is why the collector requires `command -v netplan` before
recording netplan as the manager.

Every renderer must emit the search list. Go does not complain about an unused
parameter, so `netplanYAML` and `dhcpcdConf` once accepted `search` and
silently dropped it — indistinguishable from the feature working until a short
name fails to resolve on a machine you did not test.
`TestEveryRendererEmitsTheSearchDomain` covers all three.

## Renaming a stack

Compose derives network and volume names from the project name. Bind-mounted
data travels with the directory, but a **named volume** is prefixed with the
project and does not follow — the renamed stack starts against an empty one and
looks healthy. `stack rename` therefore refuses when any are declared unless
`--force` is passed.

## Health checks

`hlab check` answers a different question to `ping`. Reachable means SSH
answered; this asks whether the machine is doing its job. The gap is not
theoretical — a container ran for two days in "Up" while failing every
request, and nothing in the tool could see it.

Two principles keep it useful:

- **Quiet thresholds.** A check that cries wolf gets ignored, and an ignored
  check is worse than no check. Disk warns at 85% and fails at 95%; a container
  is only crash-looping after several restarts.
- **Only the machine's own disks.** Network filesystems are excluded
  deliberately: a full NAS is a real problem but it is not this machine's, and
  including it means every host mounting the share reports itself sick at once.
  Pseudo-filesystems are excluded because they fill and empty on their own.

`systemctl --failed` reports anything that failed *since boot*, so a unit can
show as failed on a machine that is working now — the failure was at boot and
nothing has cleared it. Worth knowing when reading the output: it is a real
signal, but it is not necessarily a current one.

A stack running fewer services than its compose file declares is a failure
`docker ps` cannot show, because the thing to notice is the one that is absent.

## Security posture

Host keys are verified against `known_hosts` by default; `--insecure-host-key`
is the only bypass and it is documented as bootstrap-only. Auth is identity
file, then ssh-agent — passphrase-protected keys are deliberately *not*
unlocked interactively. Don't add an interactive password prompt or a
trust-on-first-use default without a deliberate decision.

The starter config is written 0600 because an inventory records usernames and
internal addresses. `homelab.yaml` is gitignored for the same reason.

## Testing

`internal/sshx/executor_test.go` runs a real in-process SSH server, so the
executor's happy path, non-zero exits, fan-out ordering, concurrency limiting
and cancellation are covered without a live machine. Extend that harness rather
than mocking the ssh package.
