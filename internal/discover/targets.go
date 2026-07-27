// Package discover finds machines on the network that are not yet under
// management, and renders them as inventory entries.
package discover

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// DefaultMaxTargets caps an accidental scan of an enormous range. A /16 fits
// under it; anything larger has to be asked for explicitly.
const DefaultMaxTargets = 65536

// ExpandTargets turns scan specs into a de-duplicated address list. Each spec
// is one of:
//
//	192.0.2.0/24              a CIDR block
//	192.0.2.10                a single address
//	192.0.2.10-192.0.2.50   an inclusive range
//	192.0.2.10-50             an inclusive range, last octet shorthand
//
// For IPv4 blocks of /30 or larger the network and broadcast addresses are
// skipped, since nothing listens on them.
func ExpandTargets(specs []string, max int) ([]netip.Addr, error) {
	if max <= 0 {
		max = DefaultMaxTargets
	}

	seen := make(map[netip.Addr]struct{})
	var out []netip.Addr

	add := func(a netip.Addr) error {
		a = a.Unmap()
		if _, dup := seen[a]; dup {
			return nil
		}
		if len(out) >= max {
			return fmt.Errorf(
				"scan would cover more than %d addresses; narrow the range or raise --max-targets", max)
		}
		seen[a] = struct{}{}
		out = append(out, a)
		return nil
	}

	for _, raw := range specs {
		spec := strings.TrimSpace(raw)
		if spec == "" {
			continue
		}

		var err error
		switch {
		case strings.Contains(spec, "/"):
			err = expandPrefix(spec, add)
		case strings.Contains(spec, "-"):
			err = expandRange(spec, add)
		default:
			var a netip.Addr
			a, err = netip.ParseAddr(spec)
			if err != nil {
				err = fmt.Errorf("invalid address %q: %w", spec, err)
				break
			}
			err = add(a)
		}
		if err != nil {
			return nil, err
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no addresses to scan")
	}
	return out, nil
}

func expandPrefix(spec string, add func(netip.Addr) error) error {
	p, err := netip.ParsePrefix(spec)
	if err != nil {
		return fmt.Errorf("invalid CIDR %q: %w", spec, err)
	}
	p = p.Masked()

	first := p.Addr()
	// A /31 is a point-to-point link and a /32 is a single host: both ends are
	// usable. Wider IPv4 blocks reserve the network and broadcast addresses.
	// IPv6 has no broadcast address, so nothing is reserved there.
	skipEdges := first.Is4() && p.Bits() <= 30
	last := netip.Addr{}
	if skipEdges {
		last = lastAddr4(p)
	}

	for a := first; a.IsValid() && p.Contains(a); a = a.Next() {
		if skipEdges && (a == first || a == last) {
			continue
		}
		if err := add(a); err != nil {
			return err
		}
	}
	return nil
}

// lastAddr4 returns the broadcast address of an IPv4 prefix.
func lastAddr4(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().As4()
	v := uint64(b[0])<<24 | uint64(b[1])<<16 | uint64(b[2])<<8 | uint64(b[3])
	// uint64 keeps the shift well-defined for a /0, where host bits are 32.
	v |= (uint64(1) << (32 - p.Bits())) - 1
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func expandRange(spec string, add func(netip.Addr) error) error {
	lhs, rhs, _ := strings.Cut(spec, "-")
	lhs, rhs = strings.TrimSpace(lhs), strings.TrimSpace(rhs)

	start, err := netip.ParseAddr(lhs)
	if err != nil {
		return fmt.Errorf("invalid range start %q: %w", lhs, err)
	}

	var end netip.Addr
	if strings.ContainsAny(rhs, ".:") {
		end, err = netip.ParseAddr(rhs)
		if err != nil {
			return fmt.Errorf("invalid range end %q: %w", rhs, err)
		}
		if start.Is4() != end.Is4() {
			return fmt.Errorf("range %q mixes IPv4 and IPv6", spec)
		}
	} else {
		// Last-octet shorthand: 192.0.2.10-50.
		if !start.Is4() {
			return fmt.Errorf("range %q: last-octet shorthand only works for IPv4", spec)
		}
		n, err := strconv.Atoi(rhs)
		if err != nil || n < 0 || n > 255 {
			return fmt.Errorf("range %q: %q is not a valid last octet", spec, rhs)
		}
		b := start.As4()
		b[3] = byte(n)
		end = netip.AddrFrom4(b)
	}

	if end.Less(start) {
		return fmt.Errorf("range %q ends before it starts", spec)
	}

	for a := start; a.IsValid(); a = a.Next() {
		if err := add(a); err != nil {
			return err
		}
		if a == end {
			break
		}
	}
	return nil
}

// LocalSubnets returns the IPv4 prefixes attached to this machine's up,
// non-loopback interfaces. It is what `hlab discover scan` uses when given no
// explicit range.
func LocalSubnets() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("list network interfaces: %w", err)
	}

	seen := make(map[string]struct{})
	var out []string

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			prefix := (&net.IPNet{IP: ip4.Mask(ipnet.Mask), Mask: ipnet.Mask}).String()
			if _, dup := seen[prefix]; dup {
				continue
			}
			seen[prefix] = struct{}{}
			out = append(out, prefix)
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no usable IPv4 networks found on this machine; pass a range explicitly")
	}
	return out, nil
}
