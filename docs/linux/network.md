# Network configuration and rollback

Run `hlab sync` first so hlab has a recent view of interfaces, addresses,
gateway, DNS and the network manager. `hlab net static [SELECTOR]` proposes
pinning the interface hlab reaches a machine through to a static address.
Without options, it keeps the address already in use. It supports netplan,
NetworkManager, systemd-networkd and dhcpcd; machines with no usable network
facts or another configuration manager are skipped.

The command is plan-only unless `--apply` is supplied. A static change takes
the connection used to apply it offline, so hlab backs up the existing
settings, starts a detached 90-second rollback timer, applies the change,
reconnects and confirms. If it cannot confirm, the old settings are restored.
Changes are applied one machine at a time; a failure stops the rest of the
plan.

```bash
hlab net static @servers
hlab net static apps-1 --apply
```

## Move out of a DHCP pool

Use `--pool FIRST-LAST` to describe the DHCP allocation range. With no
`--to`, hlab only relocates a machine whose current address falls inside that
range and picks a candidate address outside it, on the same subnet. Hlab
checks inventory addresses, collected interface addresses and the gateway
before choosing. These checks cannot prove that a host unknown to the inventory
is not already using that address.

Use `--to ADDRESS` to choose a specific destination; it takes exactly one
machine. The address must be on that machine's subnet and absent from the
inventory and collected interface facts. If you provide `--pool`, it must also
be outside that range. A relocation is still protected by the
same dead-man timer: hlab verifies the host at its new address before it
updates the inventory and cancels rollback. If the new address does not
answer, the machine restores its previous configuration.

```bash
hlab net static apps-1 --pool 192.0.2.100-192.0.2.200
hlab net static apps-1 --pool 192.0.2.100-192.0.2.200 --apply
hlab net static apps-1 --to 192.0.2.30 --pool 192.0.2.100-192.0.2.200 --apply
```

## Set DNS or disable Wi-Fi

`--dns` replaces the resolver list. `--dns-search` sets the domain search
list; if omitted, hlab carries over the machine's current search list. When
updating an already-static interface, pass `--dns`; `--dns-search` is applied
with that resolver update or while pinning a DHCP interface. Hlab connects by
address, so a DNS change cannot sever the SSH connection used to apply it.

```bash
hlab net static @servers --dns 192.0.2.53 --dns-search example.test
```

`hlab net wifi-off [SELECTOR]` previews blocking wireless with `rfkill`.
It only proceeds when the machine has working Ethernet and hlab already
reaches it over that Ethernet interface; it refuses if the SSH connection
depends on Wi-Fi. Apply with `--apply`. Wireless also has a 90-second
dead-man revert, canceled only after hlab confirms it can reconnect.

```bash
hlab net wifi-off apps-1
hlab net wifi-off apps-1 --apply
```
