// Package netcfg changes the network configuration of managed machines.
//
// Every operation here can strand a machine: the connection hlab is using to
// make the change is itself carried by the thing being changed. The design
// follows from that — refuse anything that would remove the only path in,
// prefer the address a machine already holds, and apply behind a dead-man
// timer that puts the old configuration back if the change is not confirmed.
package netcfg

import (
	"fmt"
	"strings"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// Action is what a plan would do to one machine.
type Action string

const (
	// ActionStatic converts an interface from a DHCP lease to a static
	// address, keeping the address it already holds.
	ActionStatic Action = "static"
	// ActionDisableWiFi blocks wireless on a machine reached over ethernet.
	ActionDisableWiFi Action = "disable-wifi"
)

// Plan is a change proposed for one machine, or the reason there is none.
type Plan struct {
	Host    inventory.Host
	Action  Action
	Skipped bool
	// Reason explains what will be done, or why nothing will be.
	Reason string

	// Interface is the interface being changed.
	Interface string
	// Address, Gateway and DNS are the settings a static plan would write.
	Address string
	Gateway string
	DNS     []string
	// Search is the domain search list. It is carried across unchanged rather
	// than dropped: without it a bare "printer-2" stops resolving even though
	// "printer-2.example" still does, and everything configured by short name breaks
	// quietly at the moment DHCP stops supplying the domain.
	Search []string
	// Manager is the tool that owns this machine's configuration.
	Manager string
	// NewAddress is set when the plan moves the machine to a different
	// address, which means verification must find it somewhere new.
	NewAddress string
}

// PlanStatic proposes converting the interface a machine is reached over from
// DHCP to a static address.
//
// The address written is the one the machine already holds. Changing it at the
// same time would mean the verification connection has to find the machine at a
// new address, turning a reversible change into a gamble — take the lease as it
// stands, and move the address separately if you want to.
func PlanStatic(h inventory.Host) Plan { return PlanStaticWithDNS(h, nil, nil) }

// PlanStaticWithDNS is PlanStatic with the machine's resolvers replaced.
//
// Repointing DNS is the same write to the same file as pinning an address, so
// it is the same plan rather than a second one. It is also the one network
// change that cannot strand a machine — hlab connects by address, not by name —
// which is why a machine that is already static is updated here instead of
// being skipped.
func PlanStaticWithDNS(h inventory.Host, dns, search []string) Plan {
	p := Plan{Host: h, Action: ActionStatic}

	if h.Network == nil || len(h.Network.Interfaces) == 0 {
		return skip(p, "no network information collected; run `hlab sync` first")
	}
	p.Manager = h.Network.Manager
	if p.Manager == "" {
		return skip(p, "could not tell what manages this machine's network configuration")
	}
	if !supportedManager(p.Manager) {
		return skip(p, fmt.Sprintf("%s configuration is not supported yet", p.Manager))
	}

	iface, ok := interfaceHolding(h, h.Address)
	if !ok {
		return skip(p, fmt.Sprintf("no interface holds %s; the record may be out of date", h.Address))
	}
	p.Interface = iface.Name

	if !iface.DHCP && len(dns) == 0 {
		return skip(p, fmt.Sprintf("%s already has a static address", iface.Name))
	}

	cidr, ok := addressWithPrefix(iface, h.Address)
	if !ok {
		return skip(p, fmt.Sprintf("could not determine the prefix length for %s", h.Address))
	}
	p.Address = cidr

	p.Gateway = h.Network.Gateway
	if p.Gateway == "" {
		return skip(p, "no default gateway recorded; a static address without one would cut the machine off")
	}
	p.DNS = h.Network.DNS
	if len(dns) > 0 {
		p.DNS = dns
	}
	p.Search = h.Network.Search
	if len(search) > 0 {
		p.Search = search
	}
	if len(p.DNS) == 0 {
		return skip(p, "no DNS servers recorded; a static address without them would leave the machine unable to resolve")
	}

	// An already-static machine is only here to have its resolvers changed, so
	// say that rather than claiming to do something it has already had done.
	if !iface.DHCP {
		if sameServers(h.Network.DNS, p.DNS) {
			return skip(p, fmt.Sprintf("%s already resolves via %s", iface.Name, strings.Join(p.DNS, ", ")))
		}
		p.Reason = fmt.Sprintf("point %s at %s (via %s)",
			p.Interface, strings.Join(p.DNS, ", "), p.Manager)
		return p
	}

	p.Reason = fmt.Sprintf("hold %s on %s statically (via %s)", p.Address, p.Interface, p.Manager)
	return p
}

// sameServers reports whether two resolver lists are identical, order included.
// Order is part of the setting: the first server that answers is the one used.
func sameServers(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// PlanDisableWiFi proposes blocking wireless on a machine.
//
// It refuses unless the machine has a working ethernet link that is not the
// one being removed, and unless hlab itself reaches the machine over that
// ethernet link. Disabling the interface hlab is connected through would end
// the session and leave no way back in.
func PlanDisableWiFi(h inventory.Host) Plan {
	p := Plan{Host: h, Action: ActionDisableWiFi}

	if h.Network == nil || len(h.Network.Interfaces) == 0 {
		return skip(p, "no network information collected; run `hlab sync` first")
	}
	p.Manager = h.Network.Manager

	wifi := h.Network.ActiveWiFi()
	if len(wifi) == 0 {
		return skip(p, "no active wireless interface")
	}
	p.Interface = wifi[0].Name

	wired := h.Network.WorkingEthernet()
	if len(wired) == 0 {
		return skip(p, "no working ethernet link; wireless is the only way in")
	}

	// The decisive check: hlab must already be reaching this machine over the
	// wire, not over the interface about to be switched off.
	reached, ok := interfaceHolding(h, h.Address)
	if !ok {
		return skip(p, fmt.Sprintf("no interface holds %s; the record may be out of date", h.Address))
	}
	if reached.Type != inventory.IfaceEthernet {
		return skip(p, fmt.Sprintf(
			"hlab reaches this machine at %s over %s (%s); repoint the record at its ethernet address first",
			h.Address, reached.Name, reached.Type))
	}

	names := make([]string, 0, len(wired))
	for _, w := range wired {
		names = append(names, w.Name)
	}
	p.Reason = fmt.Sprintf("block wireless (%s); reached over %s", p.Interface, strings.Join(names, ", "))
	return p
}

func skip(p Plan, reason string) Plan {
	p.Skipped = true
	p.Reason = reason
	return p
}

// supportedManager reports whether netcfg can write this manager's config.
func supportedManager(name string) bool {
	switch name {
	case "netplan", "NetworkManager", "dhcpcd":
		return true
	default:
		return false
	}
}

// interfaceHolding finds the interface carrying a given address.
func interfaceHolding(h inventory.Host, address string) (inventory.Interface, bool) {
	if h.Network == nil {
		return inventory.Interface{}, false
	}
	for _, iface := range h.Network.Interfaces {
		for _, addr := range iface.Addresses {
			if ip, _, _ := strings.Cut(addr, "/"); ip == address {
				return iface, true
			}
		}
	}
	return inventory.Interface{}, false
}

// addressWithPrefix returns the address in CIDR form as the machine holds it.
func addressWithPrefix(iface inventory.Interface, address string) (string, bool) {
	for _, addr := range iface.Addresses {
		ip, _, found := strings.Cut(addr, "/")
		if ip == address && found {
			return addr, true
		}
	}
	return "", false
}

// Skipped marks a plan as not actionable, with a reason.
func Skipped(p Plan, reason string) Plan { return skip(p, reason) }
