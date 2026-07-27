package cli

import (
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

func hostWithDNS(name, address string, dns []string, ifaceAddrs ...string) inventory.Host {
	var ifaces []inventory.Interface
	if len(ifaceAddrs) > 0 {
		ifaces = []inventory.Interface{{Name: "eth0", Addresses: ifaceAddrs}}
	}
	return inventory.Host{
		Name: name, Address: address, Managed: true,
		Network: &inventory.NetworkInfo{DNS: dns, Interfaces: ifaces},
	}
}

// Rebooting the machine another one depends on for name resolution takes that
// machine's DNS with it. hlab connects by address so it never notices, which
// is exactly why this has to be checked rather than discovered later.
func TestRebootRefusedWhenItIsSomebodysOnlyResolver(t *testing.T) {
	dnsServer := hostWithDNS("dns-1", "198.51.100.4", []string{"198.51.100.4"}, "198.51.100.4/24")
	client := hostWithDNS("printer-1", "198.51.100.58", []string{"198.51.100.4"})

	got := resolverDependents([]inventory.Host{dnsServer, client}, dnsServer)
	if len(got) != 1 || got[0] != "printer-1" {
		t.Errorf("resolverDependents = %v, want [printer-1]", got)
	}
}

// A machine with a second resolver is not stranded, which is the whole reason
// for running two DNS servers.
func TestRebootAllowedWhenASecondResolverExists(t *testing.T) {
	dnsServer := hostWithDNS("dns-1", "198.51.100.4", []string{"198.51.100.4", "198.51.100.3"}, "198.51.100.4/24")
	client := hostWithDNS("cam-1", "198.51.100.239", []string{"198.51.100.4", "198.51.100.3"})

	if got := resolverDependents([]inventory.Host{dnsServer, client}, dnsServer); len(got) != 0 {
		t.Errorf("resolverDependents = %v, want none", got)
	}
}

// Every address the machine holds counts, not just the one hlab reaches it on:
// a second interface is exactly how a resolver ends up being depended on under
// an address nobody thought about.
func TestResolverDependentsConsidersEveryAddress(t *testing.T) {
	dnsServer := hostWithDNS("dns-1", "198.51.100.4", nil, "198.51.100.4/24", "198.51.100.26/24")
	client := hostWithDNS("box", "198.51.100.99", []string{"198.51.100.26"})

	got := resolverDependents([]inventory.Host{dnsServer, client}, dnsServer)
	if len(got) != 1 || got[0] != "box" {
		t.Errorf("resolverDependents = %v, want [box] via the second address", got)
	}
}

// A stub resolver hides where queries actually go, so it cannot be judged from
// the inventory. Counting it as a dependency would refuse reboots on a guess.
func TestResolverDependentsIgnoresStubResolvers(t *testing.T) {
	dnsServer := hostWithDNS("dns-1", "198.51.100.4", nil, "198.51.100.4/24")
	client := hostWithDNS("box", "198.51.100.99", []string{"127.0.0.53"})

	if got := resolverDependents([]inventory.Host{dnsServer, client}, dnsServer); len(got) != 0 {
		t.Errorf("resolverDependents = %v, want none for a stub resolver", got)
	}
}

// A machine pointing at itself is not stranded by its own reboot.
func TestResolverDependentsIgnoresTheMachineItself(t *testing.T) {
	dnsServer := hostWithDNS("dns-1", "198.51.100.4", []string{"198.51.100.4"}, "198.51.100.4/24")

	if got := resolverDependents([]inventory.Host{dnsServer}, dnsServer); len(got) != 0 {
		t.Errorf("resolverDependents = %v, want none", got)
	}
}

// An unmanaged record has no credentials and is not something hlab keeps
// working, so it must not veto a reboot.
func TestResolverDependentsIgnoresUnmanagedMachines(t *testing.T) {
	dnsServer := hostWithDNS("dns-1", "198.51.100.4", nil, "198.51.100.4/24")
	stranger := hostWithDNS("unknown", "198.51.100.77", []string{"198.51.100.4"})
	stranger.Managed = false

	if got := resolverDependents([]inventory.Host{dnsServer, stranger}, dnsServer); len(got) != 0 {
		t.Errorf("resolverDependents = %v, want none for an unmanaged record", got)
	}
}

func TestRefusalNamesWhatWouldBreak(t *testing.T) {
	dnsServer := hostWithDNS("dns-1", "198.51.100.4", nil, "198.51.100.4/24")
	client := hostWithDNS("printer-1", "198.51.100.58", []string{"198.51.100.4"})

	got := resolverDependents([]inventory.Host{dnsServer, client}, dnsServer)
	if !strings.Contains(strings.Join(got, ","), "printer-1") {
		t.Errorf("the refusal does not name the affected machine: %v", got)
	}
}
