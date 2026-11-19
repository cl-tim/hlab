package netcfg

import (
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/inventory"
)

func iface(name, kind, addr string, dhcp, carrier bool) inventory.Interface {
	return inventory.Interface{
		Name: name, Type: kind, Up: true, Carrier: carrier, DHCP: dhcp,
		Addresses: []string{addr},
	}
}

func host(name, address, manager string, ifaces ...inventory.Interface) inventory.Host {
	return inventory.Host{
		Name: name, Address: address, Managed: true, User: "hlab",
		Network: &inventory.NetworkInfo{
			Manager: manager, Gateway: "192.0.2.1", DNS: []string{"192.0.2.119"},
			Interfaces: ifaces,
		},
	}
}

func TestPlanStaticKeepsTheAddressTheMachineHolds(t *testing.T) {
	h := host("pi", "192.0.2.243", "netplan",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.243/24", true, true))

	p := PlanStatic(h)
	if p.Skipped {
		t.Fatalf("skipped: %s", p.Reason)
	}
	if p.Address != "192.0.2.243/24" {
		t.Errorf("Address = %q, want the lease it already holds", p.Address)
	}
	if p.Interface != "eth0" || p.Manager != "netplan" {
		t.Errorf("plan = %+v", p)
	}
}

func TestPlanStaticSkipsWhenAlreadyStatic(t *testing.T) {
	h := host("printer-1", "192.0.2.58", "netplan",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.58/24", false, true))

	if p := PlanStatic(h); !p.Skipped || !strings.Contains(p.Reason, "already") {
		t.Errorf("plan = %+v, want a skip", p)
	}
}

// A static address with no gateway or no resolvers is worse than the DHCP
// lease it replaces: the machine keeps its address and loses everything else.
func TestPlanStaticRefusesWithoutGatewayOrDNS(t *testing.T) {
	base := host("pi", "198.51.100.5", "netplan",
		iface("eth0", inventory.IfaceEthernet, "198.51.100.5/24", true, true))

	noGW := base
	noGW.Network = &inventory.NetworkInfo{
		Manager: "netplan", DNS: []string{"198.51.100.1"}, Interfaces: base.Network.Interfaces,
	}
	if p := PlanStatic(noGW); !p.Skipped || !strings.Contains(p.Reason, "gateway") {
		t.Errorf("plan without a gateway = %+v, want a skip", p)
	}

	noDNS := base
	noDNS.Network = &inventory.NetworkInfo{
		Manager: "netplan", Gateway: "198.51.100.1", Interfaces: base.Network.Interfaces,
	}
	if p := PlanStatic(noDNS); !p.Skipped || !strings.Contains(p.Reason, "DNS") {
		t.Errorf("plan without DNS = %+v, want a skip", p)
	}
}

func TestPlanStaticRefusesUnknownManager(t *testing.T) {
	h := host("box", "198.51.100.5", "ifupdown",
		iface("eth0", inventory.IfaceEthernet, "198.51.100.5/24", true, true))
	if p := PlanStatic(h); !p.Skipped {
		t.Errorf("plan = %+v, want a skip for an unsupported manager", p)
	}
}

// TestPlanDisableWiFiRefusesWhenWirelessIsTheOnlyWayIn is the check that keeps
// a machine reachable. Three of the fleet have no ethernet at all; blocking
// wireless there would strand them with no route back.
func TestPlanDisableWiFiRefusesWhenWirelessIsTheOnlyWayIn(t *testing.T) {
	wifiOnly := host("cam-1", "192.0.2.239", "netplan",
		iface("wlan0", inventory.IfaceWiFi, "192.0.2.239/24", true, true))

	p := PlanDisableWiFi(wifiOnly)
	if !p.Skipped {
		t.Fatal("PlanDisableWiFi would strand a machine with no ethernet")
	}
	if !strings.Contains(p.Reason, "only way in") {
		t.Errorf("Reason = %q", p.Reason)
	}
}

func TestPlanDisableWiFiRefusesWhenReachedOverWireless(t *testing.T) {
	// Ethernet exists and carries a link, but hlab is connected over wifi.
	// Blocking wireless would drop the session mid-change.
	h := host("pi", "192.0.2.244", "netplan",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.243/24", true, true),
		iface("wlan0", inventory.IfaceWiFi, "192.0.2.244/24", true, true))

	p := PlanDisableWiFi(h)
	if !p.Skipped {
		t.Fatal("PlanDisableWiFi would cut the connection it is running over")
	}
	if !strings.Contains(p.Reason, "repoint") {
		t.Errorf("Reason = %q, want it to say what to do first", p.Reason)
	}
}

func TestPlanDisableWiFiRefusesWhenEthernetHasNoCarrier(t *testing.T) {
	// An ethernet port that is up but unplugged is not a fallback.
	h := host("pi", "192.0.2.244", "netplan",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.243/24", true, false),
		iface("wlan0", inventory.IfaceWiFi, "192.0.2.244/24", true, true))

	if p := PlanDisableWiFi(h); !p.Skipped {
		t.Error("PlanDisableWiFi accepted an unplugged ethernet port as a fallback")
	}
}

func TestPlanDisableWiFiAcceptsWiredMachine(t *testing.T) {
	h := host("apps-1", "192.0.2.243", "netplan",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.243/24", true, true),
		iface("wlan0", inventory.IfaceWiFi, "192.0.2.244/24", true, true))

	p := PlanDisableWiFi(h)
	if p.Skipped {
		t.Fatalf("skipped: %s", p.Reason)
	}
	if p.Interface != "wlan0" {
		t.Errorf("Interface = %q, want wlan0", p.Interface)
	}
}

func TestScriptsArmTheRevertBeforeApplying(t *testing.T) {
	// If the change cuts the connection, the timer must already be running —
	// otherwise there is nothing to put the old configuration back.
	p := PlanStatic(host("pi", "198.51.100.5", "netplan",
		iface("eth0", inventory.IfaceEthernet, "198.51.100.5/24", true, true)))

	script, err := BuildStaticScript(p)
	if err != nil {
		t.Fatalf("BuildStaticScript: %v", err)
	}

	armed := strings.Index(script, "revert.sh")
	applied := strings.Index(script, "netplan apply")
	if armed < 0 || applied < 0 {
		t.Fatal("script does not both arm a revert and apply")
	}
	if armed > applied {
		t.Error("the revert is armed after the change is applied; a lost connection would never be undone")
	}
	if !strings.Contains(script, "198.51.100.5/24") || !strings.Contains(script, "192.0.2.1") {
		t.Error("rendered config is missing the address or gateway")
	}
}

func TestWiFiScriptIsIdempotentAndReversible(t *testing.T) {
	p := PlanDisableWiFi(host("pi", "198.51.100.5", "netplan",
		iface("eth0", inventory.IfaceEthernet, "198.51.100.5/24", true, true),
		iface("wlan0", inventory.IfaceWiFi, "198.51.100.6/24", true, true)))

	script, err := BuildWiFiScript(p)
	if err != nil {
		t.Fatalf("BuildWiFiScript: %v", err)
	}
	if !strings.Contains(script, "Soft blocked: yes") {
		t.Error("script does not check whether wireless is already blocked")
	}
	if !strings.Contains(script, "rfkill unblock wifi") {
		t.Error("the revert does not unblock wireless")
	}
	armed := strings.Index(script, "revert-wifi.sh")
	blocked := strings.Index(script, "\nrfkill block wifi")
	if armed < 0 || blocked < 0 || armed > blocked {
		t.Error("wireless is blocked before the revert is armed")
	}
}

func TestNetplanRenderIsValidShape(t *testing.T) {
	got := netplanYAML("eth0", "192.0.2.243/24", "192.0.2.1", []string{"192.0.2.119", "1.1.1.1"}, []string{"lan"})
	for _, want := range []string{
		"dhcp4: false",
		"addresses: [192.0.2.243/24]",
		"- to: default",
		"via: 192.0.2.1",
		"addresses: [192.0.2.119, 1.1.1.1]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("netplan output missing %q:\n%s", want, got)
		}
	}
}

func TestDhcpcdRenderIsDelimited(t *testing.T) {
	// The stanza is replaced wholesale on each change, so it has to be findable.
	got := dhcpcdConf("eth0", "192.0.2.58/24", "192.0.2.1", []string{"192.0.2.119"}, []string{"lan"})
	if !strings.Contains(got, "# --- hlab static configuration") ||
		!strings.Contains(got, "# --- end hlab static configuration ---") {
		t.Errorf("dhcpcd stanza is not delimited:\n%s", got)
	}
	if !strings.Contains(got, "static ip_address=192.0.2.58/24") {
		t.Errorf("dhcpcd stanza missing the address:\n%s", got)
	}
}

// TestStaticScriptPreflightsTheManager guards the mistake that stopped the
// first real apply: the manager is a fact collected earlier, and acting on it
// without checking displaces a machine's configuration for nothing when it is
// wrong.
func TestStaticScriptPreflightsTheManager(t *testing.T) {
	for _, tc := range []struct{ manager, probe string }{
		{"netplan", "command -v netplan"},
		{"NetworkManager", "command -v nmcli"},
		{"dhcpcd", "/etc/dhcpcd.conf"},
	} {
		p := PlanStatic(host("h", "198.51.100.5", tc.manager,
			iface("eth0", inventory.IfaceEthernet, "198.51.100.5/24", true, true)))
		script, err := BuildStaticScript(p)
		if err != nil {
			t.Fatalf("%s: %v", tc.manager, err)
		}

		probe := strings.Index(script, tc.probe)
		backup := strings.Index(script, "BACKUP_DIR/netplan")
		if probe < 0 {
			t.Errorf("%s: script does not check the tooling is present", tc.manager)
			continue
		}
		if backup >= 0 && probe > backup {
			t.Errorf("%s: the check runs after files are touched", tc.manager)
		}
	}
}

func TestNetworkManagerScriptUsesNmcli(t *testing.T) {
	p := PlanStatic(host("pi", "192.0.2.243", "NetworkManager",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.243/24", true, true)))

	script, err := BuildStaticScript(p)
	if err != nil {
		t.Fatalf("BuildStaticScript: %v", err)
	}
	for _, want := range []string{
		"nmcli connection modify",
		"ipv4.method manual",
		"192.0.2.243/24",
		"nmcli connection up",
		"BACKUP_DIR/nm",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("NetworkManager script missing %q", want)
		}
	}
	// It must not write netplan files on a NetworkManager machine.
	if strings.Contains(script, "/etc/netplan/60-hlab") {
		t.Error("the NetworkManager path writes a netplan file")
	}
}

// TestNetworkManagerBackupAsksNmcliRatherThanCopyingFiles guards the defect
// that left apps-1 with no way back: the backup copied
// /etc/NetworkManager/system-connections, which is empty on a distribution
// where NM stores profiles as /etc/netplan/90-NM-*.yaml and generates the
// keyfile into /run. The revert fired on schedule and restored nothing.
func TestNetworkManagerBackupAsksNmcliRatherThanCopyingFiles(t *testing.T) {
	p := PlanStatic(host("pi", "192.0.2.243", "NetworkManager",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.243/24", true, true)))

	script, err := BuildStaticScript(p)
	if err != nil {
		t.Fatalf("BuildStaticScript: %v", err)
	}

	if strings.Contains(script, "cp -a /etc/NetworkManager/system-connections") {
		t.Error("the backup still copies a directory NM does not necessarily use")
	}
	for _, want := range []string{
		"nmcli -g \"ipv4.$k\" connection show",
		"BACKUP_DIR/nm/con",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("backup does not record %q", want)
		}
	}

	// The settings have to be read before anything is changed, or the backup
	// records the new configuration and the revert is a no-op.
	backup := strings.Index(script, "BACKUP_DIR/nm/$k")
	modify := strings.Index(script, "nmcli connection modify")
	if backup < 0 || modify < 0 || backup > modify {
		t.Error("NM settings are captured after the change is made")
	}

	// And the revert has to put them back through NM, not through files.
	revert := script[strings.Index(script, "REVERT"):]
	if !strings.Contains(revert, "ipv4.method \"$(cat \"$BACKUP_DIR/nm/method\")\"") {
		t.Error("the revert does not restore the recorded NM settings")
	}
}

// Repointing resolvers has to reach machines that are already static — that is
// the whole point of it. The address they hold is left exactly as it is.
func TestPlanStaticWithDNSUpdatesAnAlreadyStaticMachine(t *testing.T) {
	h := host("apps-1", "192.0.2.4", "NetworkManager",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.4/24", false, true))

	p := PlanStaticWithDNS(h, []string{"192.0.2.119", "192.0.2.4"}, nil)
	if p.Skipped {
		t.Fatalf("skipped: %s", p.Reason)
	}
	if p.Address != "192.0.2.4/24" {
		t.Errorf("Address = %q, want the address it already holds", p.Address)
	}
	if strings.Join(p.DNS, ",") != "192.0.2.119,192.0.2.4" {
		t.Errorf("DNS = %v", p.DNS)
	}
	if !strings.Contains(p.Reason, "point eth0 at") {
		t.Errorf("Reason = %q, want it to describe a resolver change", p.Reason)
	}
}

// Without --dns the old behaviour stands: an already-static machine is left
// alone rather than being rewritten for no reason.
func TestPlanStaticStillSkipsStaticMachinesWithoutDNS(t *testing.T) {
	h := host("printer-1", "192.0.2.58", "netplan",
		iface("eth0", inventory.IfaceEthernet, "192.0.2.58/24", false, true))
	if p := PlanStatic(h); !p.Skipped {
		t.Errorf("plan = %+v, want a skip", p)
	}
}

// Order is part of the setting — the first resolver that answers is the one
// used — so a reordering is a real change, and an identical list is not.
func TestPlanStaticWithDNSSkipsWhenNothingWouldChange(t *testing.T) {
	h := host("pi", "198.51.100.5", "NetworkManager",
		iface("eth0", inventory.IfaceEthernet, "198.51.100.5/24", false, true))
	h.Network.DNS = []string{"198.51.100.1", "198.51.100.2"}

	if p := PlanStaticWithDNS(h, []string{"198.51.100.1", "198.51.100.2"}, nil); !p.Skipped {
		t.Error("an identical resolver list was treated as a change")
	}
	if p := PlanStaticWithDNS(h, []string{"198.51.100.2", "198.51.100.1"}, nil); p.Skipped {
		t.Error("a reordered resolver list was treated as no change")
	}
}

// TestActivationIsDetachedFromTheSession guards the failure that stopped
// dns-2 moving to a static address three times: the reactivation cuts
// the connection the script is running over, the shell is SIGHUPed, and nmcli
// dies part-way. The address lands, ICMP answers, and TCP never completes — so
// hlab concludes the machine never came back and the rollback undoes a change
// that had all but worked.
func TestActivationIsDetachedFromTheSession(t *testing.T) {
	for _, manager := range []string{"netplan", "NetworkManager", "dhcpcd"} {
		p := PlanStatic(host("h", "198.51.100.5", manager,
			iface("eth0", inventory.IfaceEthernet, "198.51.100.5/24", true, true)))
		script, err := BuildStaticScript(p)
		if err != nil {
			t.Fatalf("%s: %v", manager, err)
		}

		if !strings.Contains(script, "setsid \"$BACKUP_DIR/activate.sh\"") {
			t.Errorf("%s: the activation is not detached", manager)
		}

		// Only the apply section is examined. The armed revert contains the same
		// commands and is meant to: it runs later, from its own detached shell.
		apply := script[strings.Index(script, "# --- write the new configuration"):]
		detach := strings.Index(apply, "cat > \"$BACKUP_DIR/activate.sh\"")
		if detach < 0 {
			t.Fatalf("%s: no detached activation block", manager)
		}
		for _, disruptive := range []string{"netplan apply", "nmcli connection up", "restart dhcpcd"} {
			at := strings.Index(apply, disruptive)
			if at >= 0 && at < detach {
				t.Errorf("%s: %q runs in-session, before the detached block", manager, disruptive)
			}
		}
	}
}

// Every renderer must emit the search list. Go does not complain about an
// unused parameter, so two of these accepted `search` and silently dropped it
// — which looks exactly like the feature working until a short name fails to
// resolve on a machine that is not the one you tested.
func TestEveryRendererEmitsTheSearchDomain(t *testing.T) {
	dns := []string{"198.51.100.4"}
	search := []string{"lan"}

	for name, got := range map[string]string{
		"netplan":          netplanYAML("eth0", "198.51.100.5/24", "198.51.100.1", dns, search),
		"dhcpcd":           dhcpcdConf("eth0", "198.51.100.5/24", "198.51.100.1", dns, search),
		"systemd-networkd": networkdUnit("eth0", "198.51.100.5/24", "198.51.100.1", dns, search),
	} {
		if !strings.Contains(got, "lan") {
			t.Errorf("%s renderer dropped the search domain:\n%s", name, got)
		}
		if !strings.Contains(got, "198.51.100.4") {
			t.Errorf("%s renderer dropped the resolver:\n%s", name, got)
		}
	}
}

// systemd-networkd applies the first matching .network in lexical order across
// every directory it reads. This machine already has a generated
// 10-netplan-*.network in /run for the same interface, so a file that does not
// sort ahead of it is written, looks right, and does nothing.
func TestNetworkdUnitSortsAheadOfGeneratedFiles(t *testing.T) {
	p := PlanStatic(host("printer-2", "198.51.100.225", "systemd-networkd",
		iface("eth0", inventory.IfaceEthernet, "198.51.100.225/24", true, true)))
	if p.Skipped {
		t.Fatalf("systemd-networkd is not supported: %s", p.Reason)
	}

	script, err := BuildStaticScript(p)
	if err != nil {
		t.Fatalf("BuildStaticScript: %v", err)
	}
	if !strings.Contains(script, "/etc/systemd/network/05-hlab-eth0.network") {
		t.Error("the unit is not named to sort ahead of the generated ones")
	}
	for _, want := range []string{"networkctl reload", "BACKUP_DIR/networkd", "command -v networkctl"} {
		if !strings.Contains(script, want) {
			t.Errorf("systemd-networkd script missing %q", want)
		}
	}
	// Every manager's branch is present in the template behind a conditional,
	// so what distinguishes them is the target that gets interpolated. That is
	// the file this run will actually write.
	if strings.Contains(script, "/etc/netplan/60-hlab") || strings.Contains(script, "TARGET='/etc/dhcpcd.conf'") {
		t.Error("the systemd-networkd path targets another manager's file")
	}
}

func TestNetworkdUnitShape(t *testing.T) {
	got := networkdUnit("eth0", "198.51.100.225/24", "198.51.100.1", []string{"198.51.100.4", "198.51.100.3"}, []string{"lan"})
	for _, want := range []string{
		"[Match]", "Name=eth0", "[Network]",
		"Address=198.51.100.225/24", "Gateway=198.51.100.1",
		"DNS=198.51.100.4", "DNS=198.51.100.3", "Domains=lan",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("networkd unit missing %q:\n%s", want, got)
		}
	}
}
