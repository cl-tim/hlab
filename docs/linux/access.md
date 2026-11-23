# SSH access, enrollment and host security

hlab uses SSH keys to manage enrolled hosts. `hlab ping SELECTOR` checks
reachability, host-key trust and authentication by opening and closing an SSH
connection. It does not enroll a host or run a command.

## SSH identity and host keys

Authentication tries the host's `identityFile` (or `defaults.identityFile`)
before keys in `ssh-agent`. Passphrase-protected key files are not unlocked
interactively; load them into the agent with `ssh-add`.

Host keys are checked against `~/.ssh/known_hosts`, or
`defaults.knownHostsFile` when set. Unknown host keys are refused for normal
connections. During interactive enrollment, hlab shows an unknown key's
fingerprint and asks you to accept it; a key that conflicts with a recorded
key is refused. `--insecure-host-key` disables that verification and its
protection against connecting to an impostor. Use it only while bootstrapping
a machine you physically control.

## Enroll a machine

`hlab enroll` walks unmanaged records from earlier scans. You may also provide
a machine name or address. `hlab discover` offers this flow interactively for
new findings. Enrollment verifies SSH access before recording a machine as
managed.

The prompt can provision a dedicated `hlab` account or adopt credentials that
already work. Provisioning creates the account, installs hlab's public key and
configures sudo. Adoption records the existing account and key without
changing the machine. `--provision` selects account creation without asking.

```bash
hlab enroll apps-1
hlab enroll 192.0.2.20 --provision --sync
```

The password entered during enrollment is used interactively, never stored,
and passed to sudo on standard input. Enrollment requires a terminal. The
default `--sudo nopasswd` supports unattended administration; anyone with
hlab's private key can then obtain root privileges on enrolled machines.
Choose `--sudo password` or `--sudo none` when that access is not appropriate.
The `none` mode is enough for read-only `sync`, but limits other privileged
operations.

## Repair or unenroll

`hlab repair SELECTOR` reasserts the management account, key and sudo rule on
hosts the management account can still reach. It is idempotent. If that
account is locked out, `--as ADMIN` connects with an administrative account
and rebuilds the management account from outside.

`hlab unenroll HOST` previews revocation. `--apply` removes hlab's key and sudo
rule from the host and proves the key is refused before changing the local
inventory. It retains the record as unmanaged; add `--forget` to remove the
record. `--remove-account` also deletes the management account, and is refused
unless hlab created it. `--offline` updates the inventory only: it does not
revoke the key on the machine.

```bash
hlab repair apps-1
hlab repair apps-1 --as admin
hlab unenroll apps-1
hlab unenroll apps-1 --apply
```

See [inventory and discovery](inventory.md) for selectors and management
records, and [remote commands](commands.md) for running commands after access
is established.
