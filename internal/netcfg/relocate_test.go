package netcfg

import (
	"net/netip"
	"testing"

	"github.com/cl-tim/hlab/internal/inventory"
)

func mustPrefix(t *testing.T, s string) netip.Prefix {
	t.Helper()
	p, err := netip.ParsePrefix(s)
	if err != nil {
		t.Fatalf("ParsePrefix(%q): %v", s, err)
	}
	return p
}

func TestPoolBounds(t *testing.T) {
	p, err := ParsePool("192.0.2.50-192.0.2.250")
	if err != nil {
		t.Fatalf("ParsePool: %v", err)
	}
	for _, in := range []string{"192.0.2.50", "192.0.2.119", "192.0.2.250"} {
		if !p.Contains(netip.MustParseAddr(in)) {
			t.Errorf("%s should be inside the pool", in)
		}
	}
	for _, out := range []string{"192.0.2.2", "192.0.2.49", "192.0.2.251"} {
		if p.Contains(netip.MustParseAddr(out)) {
			t.Errorf("%s should be outside the pool", out)
		}
	}
	if _, err := ParsePool("192.0.2.250-192.0.2.50"); err == nil {
		t.Error("a reversed range was accepted")
	}
}

// TestAllocatorHandsOutDistinctAddresses is the regression guard for a bug
// that assigned every machine the same address: reservations must accumulate
// across a run, or a fleet-wide change produces a fleet-wide conflict.
func TestAllocatorHandsOutDistinctAddresses(t *testing.T) {
	pool, _ := ParsePool("192.0.2.50-192.0.2.250")
	alloc := NewAllocator(mustPrefix(t, "192.0.2.0/24"), pool, nil, "192.0.2.1")

	// Distinct machines, as in a real fleet-wide run.
	names := []string{"a", "b", "c", "d", "e"}
	seen := map[netip.Addr]bool{}
	for _, name := range names {
		addr, err := alloc.Next(name)
		if err != nil {
			t.Fatalf("Next for %s: %v", name, err)
		}
		if seen[addr] {
			t.Fatalf("Next returned %s twice; every machine would collide", addr)
		}
		seen[addr] = true
		alloc.Reserve(addr, name)
	}
	if len(seen) != 5 {
		t.Errorf("got %d distinct addresses, want 5", len(seen))
	}
	// The gateway is never handed out.
	if seen[netip.MustParseAddr("192.0.2.1")] {
		t.Error("the gateway was allocated")
	}
}

func TestAllocatorAvoidsPoolAndKnownAddresses(t *testing.T) {
	pool, _ := ParsePool("192.0.2.50-192.0.2.250")
	hosts := []inventory.Host{
		{Name: "printer", Address: "192.0.2.2"},
		{Name: "nas", Address: "192.0.2.9", Network: &inventory.NetworkInfo{
			Interfaces: []inventory.Interface{{Name: "eth0", Addresses: []string{"192.0.2.3/24"}}},
		}},
	}
	alloc := NewAllocator(mustPrefix(t, "192.0.2.0/24"), pool, hosts, "192.0.2.1")

	// .2 and .3 are spoken for, .3 only via another host's interface.
	for _, taken := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.9"} {
		if err := alloc.Check(netip.MustParseAddr(taken), "someone-else"); err == nil {
			t.Errorf("Check(%s) = nil, want it refused", taken)
		}
	}
	if err := alloc.Check(netip.MustParseAddr("192.0.2.100"), "x"); err == nil {
		t.Error("an address inside the DHCP pool was accepted")
	}
	if err := alloc.Check(netip.MustParseAddr("198.51.100.5"), "x"); err == nil {
		t.Error("an address outside the subnet was accepted")
	}
	// A machine may keep its own address.
	if err := alloc.Check(netip.MustParseAddr("192.0.2.2"), "printer"); err != nil {
		t.Errorf("a machine could not keep its own address: %v", err)
	}
}

// TestPrefixOfIsMasked guards the second half of the same bug: callers key a
// shared allocator by prefix, so two addresses on one subnet must produce an
// identical prefix.
func TestPrefixOfIsMasked(t *testing.T) {
	a := inventory.Interface{Name: "eth0", Addresses: []string{"192.0.2.236/24"}}
	b := inventory.Interface{Name: "eth0", Addresses: []string{"192.0.2.119/24"}}

	pa, ok := PrefixOf(a, "192.0.2.236")
	if !ok {
		t.Fatal("PrefixOf failed")
	}
	pb, ok := PrefixOf(b, "192.0.2.119")
	if !ok {
		t.Fatal("PrefixOf failed")
	}
	if pa != pb {
		t.Errorf("prefixes differ (%s vs %s); a shared allocator would not be shared", pa, pb)
	}
}

func TestRelocateRewritesAddressAndKeepsPrefix(t *testing.T) {
	h := inventory.Host{
		Name: "pi", Address: "192.0.2.243", Managed: true,
		Network: &inventory.NetworkInfo{
			Manager: "netplan", Gateway: "192.0.2.1", DNS: []string{"192.0.2.119"},
			Interfaces: []inventory.Interface{{
				Name: "eth0", Type: inventory.IfaceEthernet, Up: true, Carrier: true,
				DHCP: true, Addresses: []string{"192.0.2.243/24"},
			}},
		},
	}

	p, err := Relocate(PlanStatic(h), netip.MustParseAddr("192.0.2.10"))
	if err != nil {
		t.Fatalf("Relocate: %v", err)
	}
	if p.Address != "192.0.2.10/24" {
		t.Errorf("Address = %q, want the new address with the original prefix", p.Address)
	}
	if p.NewAddress != "192.0.2.10" {
		t.Errorf("NewAddress = %q; verification would look in the wrong place", p.NewAddress)
	}

	script, err := BuildStaticScript(p)
	if err != nil {
		t.Fatalf("BuildStaticScript: %v", err)
	}
	if !contains(script, "192.0.2.10/24") {
		t.Error("rendered config does not carry the new address")
	}
}

func contains(h, n string) bool {
	return len(h) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(h); i++ {
			if h[i:i+len(n)] == n {
				return true
			}
		}
		return false
	}())
}
