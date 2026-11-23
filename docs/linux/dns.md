# Publish inventory records to Technitium DNS

`hlab dns sync DNS-HOST` compares the managed inventory with a Technitium DNS
zone and DHCP scope on a managed DNS host. By default it plans A records in
the `lan` zone and DHCP reservations for eligible machines. It does not write
anything without `--apply`.

```bash
hlab dns sync dns-1
hlab dns sync dns-1 --zone example.test --apply
hlab dns sync dns-1 --no-reservations
```

The plan includes managed inventory entries only. It creates or updates
inventory A records and reports zone records that do not match without
deleting them; the zone may also hold leases for devices outside hlab's
inventory. Reservations are proposed only for addresses inside the selected
DHCP scope. Use `--no-reservations` to publish names without pinning DHCP
addresses, or `--all` to include records that already match in the report.

DNS sync runs the Technitium API calls on the DNS host against its local
loopback API. It reads the admin password from that host's
`/opt/hlab/stacks/technitium/config/admin-password` file. No DNS admin
credential is stored on the workstation or sent over SSH. The password file
is a stack secret and can be managed with
[`hlab secret`](secrets.md).

See [inventory and discovery](inventory.md) for the managed-host boundary and
[network configuration](network.md) for machine-side resolver settings.
