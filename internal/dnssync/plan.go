// Package dnssync keeps a Technitium zone in step with hlab's inventory.
//
// The inventory already knows every machine's name, address and hardware
// address. Keeping the DNS server's idea of the fleet in step with it by hand
// is exactly the sort of task that is done once, correctly, and then quietly
// rots as machines are added and addresses change.
package dnssync

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/cl-tim/hlab/internal/inventory"
)

// Record is one name the zone should hold.
type Record struct {
	Name    string `json:"name" yaml:"name"`
	Address string `json:"address" yaml:"address"`
}

// Reservation pins a machine's address in DHCP.
type Reservation struct {
	Name    string `json:"name" yaml:"name"`
	MAC     string `json:"mac" yaml:"mac"`
	Address string `json:"address" yaml:"address"`
}

// Action is what should happen to one record.
type Action string

const (
	// Create is a name the zone does not have.
	Create Action = "create"
	// Update is a name whose address disagrees with the inventory.
	Update Action = "update"
	// Unchanged needs nothing.
	Unchanged Action = "unchanged"
	// Extra is a name in the zone that hlab does not know about. It is
	// reported and never removed.
	Extra Action = "extra"
)

// Change is one difference between the inventory and the zone.
type Change struct {
	Action  Action `json:"action" yaml:"action"`
	Name    string `json:"name" yaml:"name"`
	Address string `json:"address,omitempty" yaml:"address,omitempty"`
	// Was is the address the zone currently holds, when they disagree.
	Was string `json:"was,omitempty" yaml:"was,omitempty"`
}

// Plan is the whole difference.
type Plan struct {
	Zone         string        `json:"zone" yaml:"zone"`
	Changes      []Change      `json:"changes,omitempty" yaml:"changes,omitempty"`
	Reservations []Reservation `json:"reservations,omitempty" yaml:"reservations,omitempty"`
	Skipped      []string      `json:"skipped,omitempty" yaml:"skipped,omitempty"`
}

// Actionable reports whether anything would change.
func (p Plan) Actionable() bool {
	for _, c := range p.Changes {
		if c.Action == Create || c.Action == Update {
			return true
		}
	}
	return len(p.Reservations) > 0
}

// DesiredRecords derives the names a zone should hold from the inventory.
//
// Only managed machines are included. An unmanaged record is something a scan
// saw once; publishing it as authoritative DNS would give a name to a machine
// nobody has verified, and hlab has no way to notice when it goes away.
func DesiredRecords(hosts []inventory.Host, zone string) ([]Record, []string) {
	var records []Record
	var skipped []string

	for _, h := range hosts {
		if !h.Managed {
			continue
		}
		if h.Address == "" {
			skipped = append(skipped, fmt.Sprintf("%s has no address", h.Name))
			continue
		}
		if !validLabel(h.Name) {
			skipped = append(skipped, fmt.Sprintf("%q is not usable as a DNS label", h.Name))
			continue
		}
		records = append(records, Record{
			Name: strings.ToLower(h.Name) + "." + zone, Address: h.Address,
		})
	}

	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
	return records, skipped
}

// DesiredReservations pins each machine's address to the interface that holds
// it, so DHCP cannot hand that address to something else.
//
// The MAC is taken from the interface actually carrying the machine's address
// rather than the first one found: a machine with both wired and wireless
// interfaces would otherwise have its address reserved against the wrong one,
// which is worse than no reservation because it looks done.
func DesiredReservations(hosts []inventory.Host) []Reservation {
	var out []Reservation

	for _, h := range hosts {
		if !h.Managed || h.Address == "" || h.Network == nil {
			continue
		}
		mac := macHolding(h, h.Address)
		if mac == "" {
			continue
		}
		out = append(out, Reservation{Name: h.Name, MAC: mac, Address: h.Address})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// macHolding finds the hardware address of the interface carrying an address.
func macHolding(h inventory.Host, address string) string {
	for _, iface := range h.Network.Interfaces {
		for _, addr := range iface.Addresses {
			if ip, _, _ := strings.Cut(addr, "/"); ip == address {
				return iface.MAC
			}
		}
	}
	return ""
}

// MissingReservations drops the ones the scope already holds correctly, and
// the ones it could never hold at all.
//
// An address outside the scope's range is not reservable: the server will
// never offer it, so pinning it is noise. Statically configured machines are
// deliberately outside the pool, which is exactly what this excludes — and
// they never ask DHCP for an address anyway.
func MissingReservations(want []Reservation, have map[string]string, start, end string) []Reservation {
	lo, okLo := toUint(start)
	hi, okHi := toUint(end)

	var out []Reservation
	for _, r := range want {
		if okLo && okHi {
			if n, ok := toUint(r.Address); !ok || n < lo || n > hi {
				continue
			}
		}
		key := strings.ToLower(strings.ReplaceAll(r.MAC, "-", ":"))
		if have[key] == r.Address {
			continue
		}
		out = append(out, r)
	}
	return out
}

// toUint turns a dotted address into a comparable number.
func toUint(addr string) (uint32, bool) {
	parts := strings.Split(addr, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var n uint32
	for _, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || v > 255 {
			return 0, false
		}
		n = n<<8 | uint32(v)
	}
	return n, true
}

// Diff compares what the inventory wants with what the zone holds.
func Diff(zone string, want []Record, have map[string]string) Plan {
	p := Plan{Zone: zone}

	seen := map[string]bool{}
	for _, r := range want {
		seen[r.Name] = true
		switch current, ok := have[r.Name]; {
		case !ok:
			p.Changes = append(p.Changes, Change{Action: Create, Name: r.Name, Address: r.Address})
		case current != r.Address:
			p.Changes = append(p.Changes, Change{
				Action: Update, Name: r.Name, Address: r.Address, Was: current,
			})
		default:
			p.Changes = append(p.Changes, Change{Action: Unchanged, Name: r.Name, Address: r.Address})
		}
	}

	// Names the zone holds that the inventory does not are reported, never
	// removed. The zone is shared: DHCP registers every device on the network
	// into it, and most of them are not hlab's to manage.
	for name, addr := range have {
		if !seen[name] {
			p.Changes = append(p.Changes, Change{Action: Extra, Name: name, Address: addr})
		}
	}

	sort.Slice(p.Changes, func(i, j int) bool {
		if p.Changes[i].Action != p.Changes[j].Action {
			return actionRank(p.Changes[i].Action) < actionRank(p.Changes[j].Action)
		}
		return p.Changes[i].Name < p.Changes[j].Name
	})
	return p
}

func actionRank(a Action) int {
	switch a {
	case Update:
		return 0
	case Create:
		return 1
	case Extra:
		return 2
	default:
		return 3
	}
}

// validLabel reports whether a name can be a DNS label.
func validLabel(name string) bool {
	if name == "" || len(name) > 63 {
		return false
	}
	if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
		return false
	}
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
