# Package and container updates, and reboots

## Review pending updates

`hlab update [SELECTOR]` reports pending operating-system package updates,
whether a reboot is requested, and containers running an image that their
managed Compose definition has moved past. The check refreshes the package
index, but it does not install packages, pull images or restart containers.
It compares running containers with their local stack image definitions; it
does not check a registry for newer images.

```bash
hlab update @servers
hlab update apps-1 --detail
hlab update apps-1 --packages
hlab update apps-1 --images
```

`--detail` lists individual packages. `--packages` or `--images` narrows the
report and apply to one update type; passing neither checks both.

## Apply updates

Add `--apply` to install package and/or container updates. Package upgrades
use `apt upgrade`, which does not remove packages. Updates that would require
removing a package are held back and reported. `--allow-new` permits apt to
install new packages needed for a held-back update, such as a new kernel
package, but still does not permit removals. Existing package configuration
files are kept.

```bash
hlab update apps-1 --apply
hlab update @servers --packages --allow-new --apply
```

Nothing reboots unless you add `--reboot`. Then hlab reboots only machines
that request it after upgrading, waits for them, and verifies a changed boot
ID. A host that is the only recorded resolver for another managed machine is
refused. Without `--rolling`, updates can proceed across the selected hosts
without a health gate between them.

With `--rolling`, each host is updated one at a time; hlab checks its health
and configured endpoints before advancing. A failed check or update stops the
rollout. `--rolling` requires `--apply`.

## Reboot separately

`hlab reboot [SELECTOR]` previews a sequential reboot plan. `--apply` performs
it; `--if-needed` limits it to machines that report a pending reboot.
`--if-needed` is useful with a broad selector after package updates. Hlab
verifies that the kernel boot ID changed, waits for each host before starting
the next, and stops on failure. It refuses to reboot the only recorded
resolver for another managed machine.

```bash
hlab reboot @servers --if-needed
hlab reboot @servers --if-needed --apply
```

See [collected facts and health checks](facts-health.md) for the checks used
by rolling updates, and [networking](network.md) for DNS and resolver
configuration.
