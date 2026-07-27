package netcfg

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// Pool is a range of addresses a DHCP server hands out.
//
// Static assignments have to avoid it, or the DHCP server will eventually
// lease an address to something else that a machine is already holding.
type Pool struct {
	First netip.Addr
	Last  netip.Addr
}

// ParsePool reads a "first-last" range, e.g. "192.0.2.50-192.0.2.250".
func ParsePool(s string) (Pool, error) {
	first, last, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return Pool{}, fmt.Errorf("expected a range like 192.0.2.50-192.0.2.250, got %q", s)
	}

	f, err := netip.ParseAddr(strings.TrimSpace(first))
	if err != nil {
		return Pool{}, fmt.Errorf("invalid range start: %w", err)
	}
	l, err := netip.ParseAddr(strings.TrimSpace(last))
	if err != nil {
		return Pool{}, fmt.Errorf("invalid range end: %w", err)
	}
	if l.Less(f) {
		return Pool{}, fmt.Errorf("range %q ends before it starts", s)
	}
	return Pool{First: f, Last: l}, nil
}

// Contains reports whether an address falls inside the pool.
func (p Pool) Contains(a netip.Addr) bool {
	if !p.First.IsValid() || !p.Last.IsValid() {
		return false
	}
	return !a.Less(p.First) && !p.Last.Less(a)
}

func (p Pool) String() string { return p.First.String() + "-" + p.Last.String() }

// Allocator hands out addresses outside the DHCP pool, avoiding anything
// already spoken for.
type Allocator struct {
	prefix netip.Prefix
	pool   Pool
	taken  map[netip.Addr]string
}

// NewAllocator builds an allocator for the subnet a machine sits on.
//
// Everything hlab has ever seen at an address counts as taken, managed or not:
// a printer that answered a scan once will answer again, and handing its
// address to a server would break both.
func NewAllocator(prefix netip.Prefix, pool Pool, hosts []inventory.Host, gateway string) *Allocator {
	a := &Allocator{prefix: prefix.Masked(), pool: pool, taken: map[netip.Addr]string{}}

	if gw, err := netip.ParseAddr(gateway); err == nil {
		a.taken[gw] = "the gateway"
	}
	for _, h := range hosts {
		if addr, err := netip.ParseAddr(h.Address); err == nil {
			a.taken[addr] = h.Name
		}
		if h.Network == nil {
			continue
		}
		for _, iface := range h.Network.Interfaces {
			for _, cidr := range iface.Addresses {
				ip, _, _ := strings.Cut(cidr, "/")
				if addr, err := netip.ParseAddr(ip); err == nil {
					a.taken[addr] = h.Name
				}
			}
		}
	}
	return a
}

// Reserve marks an address as used, so successive allocations do not collide
// within a single run.
func (a *Allocator) Reserve(addr netip.Addr, by string) { a.taken[addr] = by }

// Check reports why an address cannot be used, or nil if it can.
func (a *Allocator) Check(addr netip.Addr, forHost string) error {
	if !a.prefix.Contains(addr) {
		return fmt.Errorf("%s is not in %s", addr, a.prefix)
	}
	if a.pool.Contains(addr) {
		return fmt.Errorf("%s is inside the DHCP pool %s", addr, a.pool)
	}
	if addr == a.prefix.Addr() {
		return fmt.Errorf("%s is the network address", addr)
	}
	if addr == lastInPrefix(a.prefix) {
		return fmt.Errorf("%s is the broadcast address", addr)
	}
	if who, ok := a.taken[addr]; ok && who != forHost {
		return fmt.Errorf("%s is already used by %s", addr, who)
	}
	return nil
}

// Next returns the lowest usable address outside the pool.
func (a *Allocator) Next(forHost string) (netip.Addr, error) {
	for addr := a.prefix.Addr().Next(); a.prefix.Contains(addr); addr = addr.Next() {
		if a.Check(addr, forHost) == nil {
			return addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free address outside %s in %s", a.pool, a.prefix)
}

// lastInPrefix returns the broadcast address of an IPv4 prefix.
func lastInPrefix(p netip.Prefix) netip.Addr {
	if !p.Addr().Is4() {
		return netip.Addr{}
	}
	b := p.Masked().Addr().As4()
	v := uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
	v |= (uint64(1) << (32 - p.Bits())) - 1
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// PrefixOf returns the subnet an interface's address sits on.
func PrefixOf(iface inventory.Interface, address string) (netip.Prefix, bool) {
	for _, cidr := range iface.Addresses {
		ip, _, found := strings.Cut(cidr, "/")
		if ip != address || !found {
			continue
		}
		if p, err := netip.ParsePrefix(cidr); err == nil {
			// Masked so every address on the same subnet yields the same
			// prefix, which callers rely on to share one allocator.
			return p.Masked(), true
		}
	}
	return netip.Prefix{}, false
}

// Relocate turns a static plan into one that also moves the address.
//
// This is the dangerous variant: the verification connection has to find the
// machine at an address it has never used. It is only survivable because the
// change is applied behind the dead-man timer — if the new address does not
// come up, the machine restores DHCP and reappears where it was.
func Relocate(p Plan, to netip.Addr) (Plan, error) {
	if p.Skipped {
		return p, fmt.Errorf("cannot relocate: %s", p.Reason)
	}
	prefix, ok := PrefixOf(interfaceOf(p), strings.Split(p.Address, "/")[0])
	if !ok {
		return p, fmt.Errorf("could not determine the subnet of %s", p.Address)
	}

	p.NewAddress = to.String()
	p.Address = fmt.Sprintf("%s/%d", to, prefix.Bits())
	p.Reason = fmt.Sprintf("move %s from %s to %s statically (via %s)",
		p.Interface, p.Host.Address, to, p.Manager)
	return p, nil
}

// interfaceOf rebuilds the interface a plan targets, for prefix lookup.
func interfaceOf(p Plan) inventory.Interface {
	if p.Host.Network != nil {
		for _, iface := range p.Host.Network.Interfaces {
			if iface.Name == p.Interface {
				return iface
			}
		}
	}
	return inventory.Interface{}
}
