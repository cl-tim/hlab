package facts

import (
	"strings"

	"github.com/cl-tim/homelab-cli/internal/inventory"
	"testing"
)

// realIPOutput is captured verbatim from a live Ubuntu system, including the
// backslash continuation `ip -o` emits.
const realIPOutput = `##HLAB#interfaces#
1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536 qdisc noqueue state UNKNOWN mode DEFAULT group default qlen 1000\    link/loopback 00:00:00:00:00:00 brd 00:00:00:00:00:00
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP mode DEFAULT group default qlen 1000\    link/ether 02:00:00:00:00:21 brd ff:ff:ff:ff:ff:ff
1: lo    inet 127.0.0.1/8 scope host lo\       valid_lft forever preferred_lft forever
1: lo    inet 10.255.255.254/32 brd 10.255.255.254 scope global lo\       valid_lft forever preferred_lft forever
1: lo    inet6 ::1/128 scope host \       valid_lft forever preferred_lft forever
2: eth0    inet 192.168.234.67/20 brd 192.168.239.255 scope global eth0\       valid_lft forever preferred_lft forever
2: eth0    inet6 fe80::215:5dff:feea:54e4/64 scope link \       valid_lft forever preferred_lft forever
##HLAB#gateway#
default via 192.168.224.1 dev eth0 proto kernel
##HLAB#dns#
nameserver 10.255.255.254
search lan
##HLAB#end#
`

func TestParseRealNetworkOutput(t *testing.T) {
	f := Parse("office", realIPOutput)

	if f.Network == nil {
		t.Fatal("Network is nil")
	}

	if f.Network.Gateway != "192.168.224.1" {
		t.Errorf("Gateway = %q, want 192.168.224.1", f.Network.Gateway)
	}
	if strings.Join(f.Network.DNS, ",") != "10.255.255.254" {
		t.Errorf("DNS = %v", f.Network.DNS)
	}
	if strings.Join(f.Network.Search, ",") != "lan" {
		t.Errorf("Search = %v", f.Network.Search)
	}

	// Loopback is dropped: it says nothing about how the machine is attached.
	if len(f.Network.Interfaces) != 1 {
		t.Fatalf("interfaces = %+v, want just eth0", f.Network.Interfaces)
	}

	eth := f.Network.Interfaces[0]
	if eth.Name != "eth0" {
		t.Errorf("Name = %q", eth.Name)
	}
	// The MAC survives the backslash continuation `ip -o` emits.
	if eth.MAC != "02:00:00:00:00:21" {
		t.Errorf("MAC = %q, want 02:00:00:00:00:21", eth.MAC)
	}
	if eth.MTU != 1500 {
		t.Errorf("MTU = %d, want 1500", eth.MTU)
	}
	if !eth.Up {
		t.Error("Up = false, want true")
	}
	if len(eth.Addresses) != 2 {
		t.Errorf("Addresses = %v, want both v4 and v6", eth.Addresses)
	}

	if got := f.Network.PrimaryAddress(); got != "192.168.234.67/20" {
		t.Errorf("PrimaryAddress() = %q", got)
	}
	if got := strings.Join(f.Network.MACs(), ","); got != "02:00:00:00:00:21" {
		t.Errorf("MACs() = %q", got)
	}
}

// realDetailOutput is captured verbatim from a live Ubuntu system.
const realDetailOutput = `##HLAB#interface-detail#
eth0|ethernet|1|up|10000
lo|loopback|1|unknown|
##HLAB#netmanager#
config=dhcpcd
##HLAB#end#
`

func TestParseRealInterfaceDetail(t *testing.T) {
	f := Parse("office", realIPOutput+realDetailOutput)

	if f.Network == nil || len(f.Network.Interfaces) != 1 {
		t.Fatalf("Network = %+v, want one interface", f.Network)
	}
	eth := f.Network.Interfaces[0]

	if eth.Type != inventory.IfaceEthernet {
		t.Errorf("Type = %q, want ethernet", eth.Type)
	}
	if !eth.Carrier || !eth.Up || !eth.Working() {
		t.Errorf("link state = up:%v carrier:%v, want both true", eth.Up, eth.Carrier)
	}
	if eth.SpeedMbps != 10000 {
		t.Errorf("SpeedMbps = %d, want 10000", eth.SpeedMbps)
	}
}

// TestParseNetworkAddressingAndTypes covers the question this feature exists to
// answer: which interfaces are wired, which are wireless, which are carrying a
// link, and which addresses were leased rather than configured.
func TestParseNetworkAddressingAndTypes(t *testing.T) {
	raw := `##HLAB#interfaces#
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP\    link/ether b8:27:eb:11:22:33 brd ff:ff:ff:ff:ff:ff
3: wlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP\    link/ether b8:27:eb:44:55:66 brd ff:ff:ff:ff:ff:ff
2: eth0    inet 192.0.2.26/24 brd 192.0.2.255 scope global eth0\       valid_lft forever preferred_lft forever
3: wlan0    inet 192.0.2.90/24 brd 192.0.2.255 scope global dynamic wlan0\       valid_lft 86217sec preferred_lft 86217sec
##HLAB#interface-detail#
eth0|ethernet|1|up|1000
wlan0|wifi|1|up|
lo|loopback|1|unknown|
##HLAB#netmanager#
service=NetworkManager
config=netplan
##HLAB#gateway#
default via 192.0.2.1 dev eth0 proto dhcp
##HLAB#end#
`
	f := Parse("pi-dns", raw)
	net := f.Network
	if net == nil {
		t.Fatal("Network is nil")
	}

	byName := map[string]inventory.Interface{}
	for _, i := range net.Interfaces {
		byName[i.Name] = i
	}

	eth, wlan := byName["eth0"], byName["wlan0"]

	if eth.Type != inventory.IfaceEthernet || wlan.Type != inventory.IfaceWiFi {
		t.Errorf("types = eth0:%q wlan0:%q", eth.Type, wlan.Type)
	}
	// eth0 has valid_lft forever and no dynamic flag: statically configured.
	if eth.DHCP {
		t.Error("eth0 reported as DHCP; its address is static")
	}
	// wlan0 is flagged dynamic with a finite lease: DHCP.
	if !wlan.DHCP {
		t.Error("wlan0 not reported as DHCP despite a dynamic lease")
	}
	if eth.SpeedMbps != 1000 {
		t.Errorf("eth0 SpeedMbps = %d, want 1000", eth.SpeedMbps)
	}

	// netplan wins over the service that renders its output, because netplan
	// is what an operator would edit.
	if net.Manager != "netplan" {
		t.Errorf("Manager = %q, want netplan", net.Manager)
	}

	// The signal the whole feature exists for.
	if !net.WiFiRedundant() {
		t.Error("WiFiRedundant() = false; wifi is up while ethernet carries a link")
	}
	if len(net.WorkingEthernet()) != 1 || net.WorkingEthernet()[0].Name != "eth0" {
		t.Errorf("WorkingEthernet() = %+v", net.WorkingEthernet())
	}
	if len(net.ActiveWiFi()) != 1 || net.ActiveWiFi()[0].Name != "wlan0" {
		t.Errorf("ActiveWiFi() = %+v", net.ActiveWiFi())
	}
}

func TestWiFiNotRedundantWhenEthernetHasNoCarrier(t *testing.T) {
	// An ethernet port that is up but unplugged must never be mistaken for a
	// working connection — turning wifi off on that basis would strand the
	// machine.
	raw := `##HLAB#interfaces#
2: eth0: <BROADCAST,MULTICAST,UP> mtu 1500 qdisc mq state DOWN\    link/ether b8:27:eb:11:22:33 brd ff:ff:ff:ff:ff:ff
3: wlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP\    link/ether b8:27:eb:44:55:66 brd ff:ff:ff:ff:ff:ff
3: wlan0    inet 192.0.2.90/24 brd 192.0.2.255 scope global dynamic wlan0\       valid_lft 86217sec
##HLAB#interface-detail#
eth0|ethernet|0|down|
wlan0|wifi|1|up|
##HLAB#end#
`
	net := Parse("pi", raw).Network
	if net == nil {
		t.Fatal("Network is nil")
	}

	if got := net.WorkingEthernet(); len(got) != 0 {
		t.Errorf("WorkingEthernet() = %+v, want none: the cable is unplugged", got)
	}
	if net.WiFiRedundant() {
		t.Error("WiFiRedundant() = true with no working ethernet; wifi is the only link")
	}
}

func TestWorkingEthernetRequiresAnAddress(t *testing.T) {
	// A carrier with no address is a plugged-in cable that never got
	// configured. It is not something to fail over to.
	raw := `##HLAB#interfaces#
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP\    link/ether b8:27:eb:11:22:33 brd ff:ff:ff:ff:ff:ff
3: wlan0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP\    link/ether b8:27:eb:44:55:66 brd ff:ff:ff:ff:ff:ff
3: wlan0    inet 192.0.2.90/24 scope global dynamic wlan0\       valid_lft 86217sec
##HLAB#interface-detail#
eth0|ethernet|1|up|1000
wlan0|wifi|1|up|
##HLAB#end#
`
	net := Parse("pi", raw).Network
	if len(net.WorkingEthernet()) != 0 {
		t.Errorf("WorkingEthernet() = %+v, want none: no address on eth0", net.WorkingEthernet())
	}
	if net.WiFiRedundant() {
		t.Error("WiFiRedundant() = true although ethernet has no address")
	}
}

func TestParseManagerPrefersWhatYouWouldEdit(t *testing.T) {
	tests := []struct {
		name    string
		markers string
		want    string
	}{
		{"netplan over its renderer", "service=systemd-networkd\nconfig=netplan", "netplan"},
		{"netplan over NetworkManager", "service=NetworkManager\nconfig=netplan", "netplan"},
		{"NetworkManager alone", "service=NetworkManager", "NetworkManager"},
		{"dhcpcd running", "service=dhcpcd\nconfig=dhcpcd", "dhcpcd"},
		{"config only", "config=dhcpcd", "dhcpcd"},
		{"stale ifupdown file loses to a running service", "service=NetworkManager\nconfig=ifupdown", "NetworkManager"},
		{"nothing detected", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseManager(tc.markers); got != tc.want {
				t.Errorf("parseManager = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseNetworkSkipsContainerNoise(t *testing.T) {
	// A container host sprouts a veth pair per container. Those are ephemeral
	// and would swamp the record; bridges are kept because they describe how
	// the machine is actually wired.
	raw := `##HLAB#interfaces#
2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc mq state UP\    link/ether aa:bb:cc:11:22:33 brd ff:ff:ff:ff:ff:ff
3: docker0: <BROADCAST,MULTICAST,UP> mtu 1500 qdisc noqueue state UP\    link/ether 02:42:9c:11:22:33 brd ff:ff:ff:ff:ff:ff
7: veth9a1b2c3@if6: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue state UP\    link/ether 5e:aa:bb:cc:dd:ee brd ff:ff:ff:ff:ff:ff
2: eth0    inet 192.0.2.236/24 brd 192.0.2.255 scope global eth0\       valid_lft forever
3: docker0    inet 172.17.0.1/16 brd 172.17.255.255 scope global docker0\       valid_lft forever
7: veth9a1b2c3    inet6 fe80::5caa:bbff:fecc:ddee/64 scope link \       valid_lft forever
##HLAB#end#
`
	f := Parse("docker", raw)

	if f.Network == nil {
		t.Fatal("Network is nil")
	}

	var names []string
	for _, iface := range f.Network.Interfaces {
		names = append(names, iface.Name)
	}
	if strings.Join(names, ",") != "docker0,eth0" {
		t.Errorf("interfaces = %v, want docker0 and eth0 with veth dropped", names)
	}

	if got := f.Network.PrimaryAddress(); got != "172.17.0.1/16" && got != "192.0.2.236/24" {
		t.Errorf("PrimaryAddress() = %q, want a real address", got)
	}
}

func TestParseNetworkIfconfigAndRouteFallback(t *testing.T) {
	// `route -n` output, for a machine without iproute2.
	raw := `##HLAB#gateway#
Kernel IP routing table
Destination     Gateway         Genmask         Flags Metric Ref    Use Iface
0.0.0.0         198.51.100.1        0.0.0.0         UG    100    0        0 eth0
198.51.100.0        0.0.0.0         255.255.255.0   U     100    0        0 eth0
##HLAB#dns#
nameserver 198.51.100.1
nameserver 1.1.1.1
domain lan
##HLAB#end#
`
	f := Parse("box", raw)

	if f.Network == nil {
		t.Fatal("Network is nil")
	}
	if f.Network.Gateway != "198.51.100.1" {
		t.Errorf("Gateway = %q, want 198.51.100.1 from route -n", f.Network.Gateway)
	}
	if strings.Join(f.Network.DNS, ",") != "198.51.100.1,1.1.1.1" {
		t.Errorf("DNS = %v, want both nameservers in order", f.Network.DNS)
	}
}

func TestParseNetworkAbsentIsNil(t *testing.T) {
	// A host with no networking tools reports nothing rather than an empty
	// shell of a record.
	f := Parse("tiny", "##HLAB#interfaces#\n##HLAB#gateway#\n##HLAB#dns#\n##HLAB#end#\n")
	if f.Network != nil {
		t.Errorf("Network = %+v, want nil when nothing was reported", f.Network)
	}
}

func TestNetworkHelpersOnNil(t *testing.T) {
	f := Parse("tiny", "##HLAB#end#\n")
	if got := f.Network.PrimaryAddress(); got != "" {
		t.Errorf("PrimaryAddress() on nil = %q", got)
	}
	if got := f.Network.MACs(); got != nil {
		t.Errorf("MACs() on nil = %v", got)
	}
}
