# Linux fleet management

Manage Linux servers over SSH from one local inventory. hlab keeps its
configuration and management key on the computer where you run it; it does not
search the current directory for configuration or write state into your
checkout.

Use the task pages below for a focused workflow:

- [Inventory, selectors and discovery](inventory.md)
- [SSH access, enrollment and host security](access.md)
- [Remote commands, output and logs](commands.md)
- [Collected facts and health checks](facts-health.md)
- [Package and container updates, and reboots](updates.md)
- [Network configuration and rollback](network.md)
- [DNS publishing to Technitium](dns.md)
- [Private CA, HTTPS and trust stores](tls.md)
- [Compose stacks and adoption](stacks.md)
- [Stack credentials and secrets](secrets.md)
- [Backups and recovery](backups.md)

## How commands target machines

Commands that act on Linux machines use selectors. A selector resolves against
managed inventory records; discovered but unenrolled machines are not contacted
by fleet actions. See [inventory and selectors](inventory.md#selectors).

Apply behavior depends on the command. Updates, reboots, network changes, DNS
publishing, stack deploy/adopt/rename, restores and recovery drills preview
their work and require `--apply` to proceed. `run`, `enroll`, `repair`, and
`secret gen`/`set` perform their operation when invoked. `discover` and `sync`
record observations by default, and `backup` writes local archive files.
Review each command's guide and `--help` before using it.

```bash
hlab hosts
hlab ping @servers
hlab run @servers -- uptime
```

Replace these example host and group names with entries in your own inventory.
Linux fleet examples use documentation-only host names and reserved IP ranges.
Printer workflow examples are documented on the printer pages.
