# Inventory, selectors and discovery

## Where configuration lives

hlab keeps its inventory, state and management key in one directory under your
home directory: typically `~/.config/hlab` on Linux,
`~/Library/Application Support/hlab` on macOS, and `%AppData%\hlab` on
Windows. `hlab config home` prints the resolved directory.
`$HLAB_HOME` relocates that directory. The current working directory is never
searched or written by default.

Use `hlab config init` to write a starter inventory. The default config is
created automatically when a command needs it and it does not yet exist. Use
`--config PATH` or `$HLAB_CONFIG` only when you intentionally want a different
inventory. The management key stays in hlab's home even when the inventory is
elsewhere. `hlab config path` shows the file in use; `hlab config show` shows
the resolved configuration and paths.

```yaml
defaults:
  user: root
  connectTimeout: 10s

hosts:
  - name: nas-1
    address: 192.0.2.10
    tags: [storage, debian]
  - name: apps-1
    address: 192.0.2.20
    user: operator
    tags: [apps, debian]

groups:
  servers: [nas-1, apps-1]
```

The `hosts:` list contains both discovered and managed machines. A hand-written
record without `managed: false` is treated as managed; hlab marks discoveries
unmanaged. Enrollment promotes a discovered entry in place. Groups and tags
help address a role or subset of the fleet.

## Selectors

Terms are comma-separated and unioned. Prefix groups with `@`, tags with `#`,
and use `!` to subtract matches. Names support globs.

| Selector | Matches |
| --- | --- |
| `all` | every managed host |
| `@servers` | members of the `servers` group |
| `#debian` | managed hosts tagged `debian` |
| `nas-1` | one host by name |
| `apps-*` | host names matching a glob |
| `!#retired` | subtract matching hosts from a selector |

```bash
hlab ping 'all,!#retired'
hlab run '@servers,!apps-2' -- uptime
```

A term that matches nothing is an error. Machine actions resolve selectors to
managed hosts; use `hlab hosts` to see unmanaged discoveries.

Global flags include `--config`, `-o/--output table|json|yaml`, `-c/--concurrency`,
and `--connect-timeout`. `--insecure-host-key` disables SSH host-key checks and
is intended only for bootstrapping machines you control; see
[SSH access and host security](access.md#ssh-identity-and-host-keys).

## Discover machines

`hlab discover` scans local IPv4 subnets by default. You can pass a CIDR, one
address, or a range. By default it probes TCP port 22, reads an SSH
identification banner, and never authenticates. In an interactive terminal it
offers to enroll newly found unmanaged machines after showing the scan.

```bash
hlab discover
hlab discover 192.0.2.0/24 --scan-only
hlab discover 192.0.2.10-50 --new --emit
hlab discover 192.0.2.0/24 --ports 22,2222 --scan-only
```

Scanning records observations in the inventory by default. `--save=false`
leaves the inventory alone. `--scan-only` suppresses the enrollment prompt;
structured output also scans without prompting. `--new` shows only machines
not already recorded, while `--emit` prints a YAML fragment for review rather
than a table. Discovery output is a draft: it cannot choose the right login
account for a machine.

`hlab hosts` lists managed and unmanaged records without connecting to them.
`hlab hosts --managed` and `hlab hosts --unmanaged` filter the list. Related
read and maintenance commands include `hlab hosts groups`,
`hlab hosts history HOST`, `hlab hosts rename OLD NEW`, and
`hlab hosts prune`.

Continue with [SSH enrollment and access](access.md).
