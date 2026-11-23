# Collected facts and health checks

## Collect a machine snapshot

`hlab sync [SELECTOR]` connects to each selected managed host and collects a
read-only snapshot of its operating system, kernel, CPU and memory, network,
disks, services, listening ports and containers. Sudo is used only where it
adds visibility and is never required; a host with limited privileges reports
what it could read.

```bash
hlab sync
hlab sync @servers --emit-tags
```

Slow-changing host attributes, including kernel, hardware, network and
containers, are stored on the host record in `config.yaml`. Volatile facts,
such as services, ports, disks, uptime and load, go to `facts.yaml`. Both files
are in hlab's home directory, not the current checkout. `--emit-tags` suggests
tags from detected services; review the suggestions before using them.

A host whose DHCP address moved can be followed during `sync`: hlab verifies
the old recorded SSH host key at the new address before updating the record.
See [network changes](network.md) for deliberate static-address changes.

## Check health

`hlab check [SELECTOR]` asks whether a machine is doing its job, not just
whether SSH answered. It checks disk use, failed systemd units, stopped,
unhealthy or repeatedly restarting containers, whether a managed stack has
fewer running services than declared, pending reboot state, clock sync and
recent unexpected restarts. It is read-only and exits nonzero when findings
are failures.

Defaults are intentionally quiet: disk use is reported from 85% and fails at
95%; a container is treated as crash-looping at five or more restarts.
`--disk` and `--restarts` change those thresholds, and `--quiet` shows only hosts with
findings.

Configured endpoints add service probes. Probes run from the computer running
hlab. HTTP expects a 2xx response, TCP expects a connection, and DNS expects a
successful lookup. Each probe has a five-second timeout.

```yaml
endpoints:
  apps-1:
    - {name: web, kind: http, target: "https://apps.example.test/health"}
    - {name: database, kind: tcp, target: "apps.example.test:5432"}
    - {name: dns, kind: dns, target: "apps.example.test"}
```

`hlab update --rolling` uses host health and configured endpoints as a gate
before moving to the next host. See [updates and reboots](updates.md).
