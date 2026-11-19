package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/inventory"
)

// dualHomed builds the case this feature exists for: one machine with both
// NICs online, recorded twice because a scan saw two addresses.
func dualHomed(name, address string, managed bool) inventory.Host {
	return inventory.Host{
		Name: name, Address: address, Managed: managed,
		Network: &inventory.NetworkInfo{
			Interfaces: []inventory.Interface{
				{
					Name: "eth0", Type: inventory.IfaceEthernet, MAC: "02:00:00:00:00:11",
					Up: true, Carrier: true, Addresses: []string{"192.0.2.119/24"},
				},
				{
					Name: "wlan0", Type: inventory.IfaceWiFi, MAC: "02:00:00:00:00:12",
					Up: true, Carrier: true, Addresses: []string{"192.0.2.26/24"},
				},
			},
		},
	}
}

func TestFindDuplicatesBySharedMAC(t *testing.T) {
	hosts := []inventory.Host{
		dualHomed("dns-2", "192.0.2.26", true),
		dualHomed("pi-2", "192.0.2.119", true),
		{Name: "unrelated", Address: "192.0.2.50", Managed: true},
	}

	groups := FindDuplicates(hosts)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1: %+v", len(groups), groups)
	}
	g := groups[0]

	// The wired record survives: wireless is the path likely to be switched off.
	if g.Keep.Name != "pi-2" {
		t.Errorf("Keep = %q, want the record on the ethernet address", g.Keep.Name)
	}
	if len(g.Remove) != 1 || g.Remove[0].Name != "dns-2" {
		t.Errorf("Remove = %+v", g.Remove)
	}
	if !strings.Contains(g.Reason, "02:00:00:00:00:12") {
		t.Errorf("Reason = %q, want it to name the shared MAC", g.Reason)
	}
}

func TestFindDuplicatesKeepsTheManagedRecord(t *testing.T) {
	// Credentials outrank everything: discarding the managed record would mean
	// re-enrolling a machine hlab can already reach.
	hosts := []inventory.Host{
		dualHomed("wired-but-unmanaged", "192.0.2.119", false),
		dualHomed("wireless-but-managed", "192.0.2.26", true),
	}

	groups := FindDuplicates(hosts)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	if groups[0].Keep.Name != "wireless-but-managed" {
		t.Errorf("Keep = %q, want the managed record even though it is wireless", groups[0].Keep.Name)
	}
}

func TestFindDuplicatesByAddressOnAnotherHostsInterface(t *testing.T) {
	hosts := []inventory.Host{
		dualHomed("pi", "192.0.2.119", true),
		// A bare scan record for the same machine's other NIC: no interface
		// data of its own, so only the cross-reference can catch it.
		{Name: "host-192-168-1-26", Address: "192.0.2.26"},
	}

	groups := FindDuplicates(hosts)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1: %+v", len(groups), groups)
	}
	if groups[0].Keep.Name != "pi" || groups[0].Remove[0].Name != "host-192-168-1-26" {
		t.Errorf("group = %+v", groups[0])
	}
	if !strings.Contains(groups[0].Reason, "interface of") {
		t.Errorf("Reason = %q", groups[0].Reason)
	}
}

func TestFindDuplicatesLeavesDistinctMachinesAlone(t *testing.T) {
	// A shared hostname is not proof — two machines can be misconfigured the
	// same way. Only hardware or address overlap counts.
	hosts := []inventory.Host{
		{
			Name: "a", Address: "198.51.100.1", Hostname: "raspberrypi", Managed: true,
			Network: &inventory.NetworkInfo{Interfaces: []inventory.Interface{
				{Name: "eth0", MAC: "aa:aa:aa:aa:aa:aa", Addresses: []string{"198.51.100.1/24"}},
			}},
		},
		{
			Name: "b", Address: "198.51.100.2", Hostname: "raspberrypi", Managed: true,
			Network: &inventory.NetworkInfo{Interfaces: []inventory.Interface{
				{Name: "eth0", MAC: "bb:bb:bb:bb:bb:bb", Addresses: []string{"198.51.100.2/24"}},
			}},
		},
	}

	if groups := FindDuplicates(hosts); len(groups) != 0 {
		t.Errorf("FindDuplicates merged two distinct machines: %+v", groups)
	}
}

func TestFindStaleSparesManagedMachines(t *testing.T) {
	long := time.Now().Add(-90 * 24 * time.Hour)
	recent := time.Now().Add(-time.Hour)
	cutoff := time.Now().Add(-30 * 24 * time.Hour)

	hosts := []inventory.Host{
		{Name: "old-unmanaged", Address: "198.51.100.1", LastSeen: long},
		{Name: "old-managed", Address: "198.51.100.2", Managed: true, LastSeen: long},
		{Name: "recent-unmanaged", Address: "198.51.100.3", LastSeen: recent},
		{Name: "never-seen", Address: "198.51.100.4"},
	}

	stale := FindStale(hosts, cutoff)
	if len(stale) != 1 || stale[0].Name != "old-unmanaged" {
		t.Errorf("FindStale = %+v, want only old-unmanaged", stale)
	}
}

func TestRemoveHostsDropsGroupMembership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := `# lab
hosts:
  - name: a
    address: 198.51.100.1
    managed: true
  - name: b
    address: 198.51.100.2
    managed: true
groups:
  core: [a, b]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := RemoveHosts(path, []string{"b"}); err != nil {
		t.Fatalf("RemoveHosts: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Inventory.Hosts()) != 1 {
		t.Errorf("hosts = %+v, want just a", cfg.Inventory.Hosts())
	}
	// A group left pointing at a removed machine would fail to load at all.
	if got := strings.Join(cfg.Inventory.Groups()["core"], ","); got != "a" {
		t.Errorf("group core = %q, want just a", got)
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# lab") {
		t.Errorf("comments lost:\n%s", raw)
	}
}

func TestRemoveHostsRejectsUnknownNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.1", Managed: true}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	if err := RemoveHosts(path, []string{"nope"}); err == nil {
		t.Error("removing an unknown machine succeeded")
	}
	if cfg, _ := Load(path); len(cfg.Inventory.Hosts()) != 1 {
		t.Error("a rejected removal changed the config")
	}
}

func TestWiredAddressOf(t *testing.T) {
	h := dualHomed("pi", "192.0.2.26", true)
	if got := WiredAddressOf(h); got != "192.0.2.119" {
		t.Errorf("WiredAddressOf = %q, want the ethernet address", got)
	}
	if got := InterfaceKindFor(h, "192.0.2.26"); got != inventory.IfaceWiFi {
		t.Errorf("InterfaceKindFor(wireless address) = %q", got)
	}
}
