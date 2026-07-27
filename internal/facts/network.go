package facts

import (
	"sort"
	"strconv"
	"strings"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// skipInterface reports whether an interface is noise rather than a real
// attachment to the network.
//
// Container hosts sprout a veth pair per container, which are ephemeral and
// would swamp the record. Bridges (docker0, br-*) are kept: they describe how
// the machine is actually wired.
func skipInterface(name string) bool {
	switch {
	case name == "" || name == "lo":
		return true
	case strings.HasPrefix(name, "veth"):
		return true
	default:
		return false
	}
}

// parseNetwork assembles the network picture from the collected sections.
func parseNetwork(interfaces, detail, manager, gateway, dns string) *inventory.NetworkInfo {
	net := &inventory.NetworkInfo{
		Interfaces: parseInterfaces(interfaces),
		Gateway:    parseGateway(gateway),
		Manager:    parseManager(manager),
	}
	net.DNS, net.Search = parseResolvConf(dns)
	applyInterfaceDetail(net.Interfaces, parseInterfaceDetail(detail))

	if net.Gateway == "" && len(net.Interfaces) == 0 && len(net.DNS) == 0 {
		return nil
	}
	return net
}

// interfaceDetail is what /sys reports about one interface.
type interfaceDetail struct {
	kind      string
	carrier   bool
	operState string
	speedMbps int
}

// parseInterfaceDetail reads the "name|type|carrier|operstate|speed" lines.
func parseInterfaceDetail(s string) map[string]interfaceDetail {
	out := map[string]interfaceDetail{}

	for _, line := range strings.Split(s, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 5 || parts[0] == "" {
			continue
		}

		d := interfaceDetail{
			kind:      parts[1],
			carrier:   parts[2] == "1",
			operState: parts[3],
		}
		if speed, err := strconv.Atoi(parts[4]); err == nil && speed > 0 {
			d.speedMbps = speed
		}
		out[parts[0]] = d
	}
	return out
}

// applyInterfaceDetail folds the /sys view into the addresses already parsed.
func applyInterfaceDetail(ifaces []inventory.Interface, detail map[string]interfaceDetail) {
	for i := range ifaces {
		d, ok := detail[ifaces[i].Name]
		if !ok {
			continue
		}
		ifaces[i].Type = d.kind
		ifaces[i].SpeedMbps = d.speedMbps

		// operstate is more trustworthy than the IFF_UP flag: an interface can
		// be administratively up while the link is down.
		if d.operState != "" {
			ifaces[i].Up = d.operState == "up" || d.operState == "unknown"
		}
		ifaces[i].Carrier = d.carrier
	}
}

// networkManagers maps a detected marker to the name recorded, in the order
// that answers "what would I edit to change this machine's addressing?".
//
// netplan comes first, but only when the collector confirmed the tool is
// installed and owns files of its own. On Ubuntu netplan renders for
// systemd-networkd or NetworkManager and editing those directly is undone on
// the next `netplan apply`, so it is the source of truth there. On Raspberry Pi
// OS the direction reverses: NetworkManager owns the configuration and writes
// /etc/netplan/90-NM-*.yaml as output. Treating those as netplan input names
// the wrong owner, and writing a netplan file on a machine with no netplan
// binary displaces NM.s profiles for nothing.
var networkManagers = []struct{ marker, name string }{
	{"config=netplan", "netplan"},
	{"service=NetworkManager", "NetworkManager"},
	{"service=systemd-networkd", "systemd-networkd"},
	{"service=dhcpcd", "dhcpcd"},
	{"service=networking", "ifupdown"},
	{"config=dhcpcd", "dhcpcd"},
	{"config=systemd-networkd", "systemd-networkd"},
	{"config=ifupdown", "ifupdown"},
}

// parseManager decides which tool owns the network configuration.
func parseManager(s string) string {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(s, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			seen[l] = struct{}{}
		}
	}

	for _, m := range networkManagers {
		if _, ok := seen[m.marker]; ok {
			return m.name
		}
	}
	return ""
}

// parseInterfaces reads `ip -o link show` followed by `ip -o addr show`, or an
// `ifconfig -a` fallback.
func parseInterfaces(s string) []inventory.Interface {
	byName := map[string]*inventory.Interface{}
	var order []string

	get := func(name string) *inventory.Interface {
		if iface, ok := byName[name]; ok {
			return iface
		}
		iface := &inventory.Interface{Name: name}
		byName[name] = iface
		order = append(order, name)
		return iface
	}

	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if name, ok := ipLinkName(line); ok && !skipInterface(name) {
			iface := get(name)
			applyLinkLine(iface, line)
			continue
		}
		if name, addr, dynamic, ok := ipAddrLine(line); ok && !skipInterface(name) {
			iface := get(name)
			iface.Addresses = append(iface.Addresses, addr)
			// A leased address is flagged dynamic by the kernel, whichever
			// tool requested it. That makes this the one DHCP signal that
			// holds across NetworkManager, netplan, dhcpcd and the rest.
			if dynamic {
				iface.DHCP = true
			}
		}
	}

	out := make([]inventory.Interface, 0, len(order))
	for _, name := range order {
		iface := byName[name]
		// An interface with neither a MAC nor an address tells us nothing.
		if iface.MAC == "" && len(iface.Addresses) == 0 {
			continue
		}
		sort.Strings(iface.Addresses)
		out = append(out, *iface)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ipLinkName recognises an `ip -o link` line and extracts the interface name.
//
//	2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc ... link/ether b8:27:eb:.. brd ..
func ipLinkName(line string) (string, bool) {
	if !strings.Contains(line, "<") || !strings.Contains(line, "mtu") {
		return "", false
	}
	fields := strings.Fields(line)
	if len(fields) < 2 || !strings.HasSuffix(fields[0], ":") {
		return "", false
	}
	name := strings.TrimSuffix(fields[1], ":")
	// A virtual interface is reported as "eth0@if12"; keep the local half.
	name, _, _ = strings.Cut(name, "@")
	return name, name != ""
}

// applyLinkLine fills in MTU, link state and MAC from an `ip -o link` line.
func applyLinkLine(iface *inventory.Interface, line string) {
	fields := strings.Fields(line)

	for i, f := range fields {
		switch {
		case f == "mtu" && i+1 < len(fields):
			if mtu, err := strconv.Atoi(fields[i+1]); err == nil {
				iface.MTU = mtu
			}
		case strings.HasPrefix(f, "link/") && i+1 < len(fields):
			if mac := fields[i+1]; strings.Count(mac, ":") == 5 {
				iface.MAC = strings.ToLower(mac)
			}
		}
	}

	// The angle-bracket flags carry the operational state.
	if start := strings.Index(line, "<"); start >= 0 {
		if end := strings.Index(line[start:], ">"); end > 0 {
			iface.Up = strings.Contains(line[start:start+end], "UP")
		}
	}
}

// ipAddrLine recognises an `ip -o addr` line.
//
//	2: eth0    inet 192.0.2.26/24 brd 192.0.2.255 scope global dynamic eth0\  valid_lft ...
func ipAddrLine(line string) (name, addr string, dynamic, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 4 || !strings.HasSuffix(fields[0], ":") {
		return "", "", false, false
	}

	name = strings.TrimSuffix(fields[1], ":")
	name, _, _ = strings.Cut(name, "@")

	// "dynamic" appears as its own word for a leased address. A statically
	// configured one has no such flag and reports valid_lft forever.
	for _, f := range fields {
		if f == "dynamic" {
			dynamic = true
			break
		}
	}

	for i, f := range fields {
		if (f == "inet" || f == "inet6") && i+1 < len(fields) {
			return name, fields[i+1], dynamic, true
		}
	}
	return "", "", false, false
}

// parseGateway reads the default route from `ip route show default`, or from
// `route -n` output.
func parseGateway(s string) string {
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}

		// ip: "default via 192.0.2.1 dev eth0 ..."
		if fields[0] == "default" {
			for i, f := range fields {
				if f == "via" && i+1 < len(fields) {
					return fields[i+1]
				}
			}
			continue
		}

		// route -n: "0.0.0.0  192.0.2.1  0.0.0.0  UG  ..."
		if fields[0] == "0.0.0.0" && len(fields) >= 2 {
			return fields[1]
		}
	}
	return ""
}

// parseResolvConf reads nameservers and search domains.
func parseResolvConf(s string) (nameservers, search []string) {
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "nameserver":
			nameservers = append(nameservers, fields[1])
		case "search", "domain":
			search = append(search, fields[1:]...)
		}
	}
	return nameservers, search
}
