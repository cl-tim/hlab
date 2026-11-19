package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/inventory"
)

// DuplicateGroup is a set of records that turned out to be one machine.
type DuplicateGroup struct {
	// Keep is the record worth keeping; Remove are the others.
	Keep   inventory.Host
	Remove []inventory.Host
	// Reason says what proved they are the same machine.
	Reason string
}

// FindDuplicates groups records that describe the same physical machine.
//
// A machine with both ethernet and wireless online has two addresses, so a
// network scan sees it twice and records it twice. The MAC addresses on each
// record's interfaces settle it: the same hardware cannot appear under two
// identities, whatever addresses it currently holds.
//
// Reverse DNS and hostnames are deliberately not used on their own — two
// machines can share a badly configured name, and that is how the wrong records
// would get merged.
func FindDuplicates(hosts []inventory.Host) []DuplicateGroup {
	// Union-find over records, joined by any shared MAC or by one record's
	// address appearing among another's interfaces.
	parent := make([]int, len(hosts))
	for i := range parent {
		parent[i] = i
	}

	var find func(int) int
	find = func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	reasons := map[int]string{}

	byMAC := map[string]int{}
	byIfaceAddr := map[string]int{}

	for i, h := range hosts {
		for _, mac := range h.Network.MACs() {
			mac = strings.ToLower(mac)
			if j, seen := byMAC[mac]; seen {
				union(j, i)
				reasons[find(i)] = "shared MAC " + mac
			} else {
				byMAC[mac] = i
			}
		}
		if h.Network != nil {
			for _, iface := range h.Network.Interfaces {
				for _, addr := range iface.Addresses {
					ip, _, _ := strings.Cut(addr, "/")
					byIfaceAddr[ip] = i
				}
			}
		}
	}

	// A record whose own address is an interface of another record is that
	// same machine reached by its other NIC.
	for i, h := range hosts {
		if j, ok := byIfaceAddr[h.Address]; ok && j != i {
			union(j, i)
			if _, has := reasons[find(i)]; !has {
				reasons[find(i)] = fmt.Sprintf("%s is an interface of %s", h.Address, hosts[j].Name)
			}
		}
	}

	members := map[int][]inventory.Host{}
	for i, h := range hosts {
		root := find(i)
		members[root] = append(members[root], h)
	}

	var groups []DuplicateGroup
	for root, set := range members {
		if len(set) < 2 {
			continue
		}
		sort.Slice(set, func(a, b int) bool { return betterRecord(set[a], set[b]) })

		reason := reasons[root]
		if reason == "" {
			reason = "same machine"
		}
		groups = append(groups, DuplicateGroup{
			Keep:   set[0],
			Remove: append([]inventory.Host(nil), set[1:]...),
			Reason: reason,
		})
	}

	sort.Slice(groups, func(i, j int) bool { return groups[i].Keep.Name < groups[j].Keep.Name })
	return groups
}

// betterRecord reports whether a is the record worth keeping.
//
// A managed record wins outright: it carries credentials, and discarding it
// would mean re-enrolling a machine hlab can already reach.
func betterRecord(a, b inventory.Host) bool {
	if a.Managed != b.Managed {
		return a.Managed
	}
	// Prefer the record reached over the wire. A wireless path is the one
	// likely to be switched off, and losing it would cut hlab off from the
	// machine entirely.
	aWired, bWired := reachedOverEthernet(a), reachedOverEthernet(b)
	if aWired != bWired {
		return aWired
	}
	if !a.LastCollected.Equal(b.LastCollected) {
		return a.LastCollected.After(b.LastCollected)
	}
	if !a.LastSeen.Equal(b.LastSeen) {
		return a.LastSeen.After(b.LastSeen)
	}
	return a.Name < b.Name
}

// reachedOverEthernet reports whether a record's address sits on a wired
// interface of that machine.
func reachedOverEthernet(h inventory.Host) bool {
	return InterfaceKindFor(h, h.Address) == inventory.IfaceEthernet
}

// InterfaceKindFor reports the kind of interface a given address sits on, or
// "" if the machine has no interface holding it.
func InterfaceKindFor(h inventory.Host, address string) string {
	if h.Network == nil {
		return ""
	}
	for _, iface := range h.Network.Interfaces {
		for _, addr := range iface.Addresses {
			if ip, _, _ := strings.Cut(addr, "/"); ip == address {
				return iface.Type
			}
		}
	}
	return ""
}

// WiredAddressOf returns an IPv4 address on a working ethernet interface, for
// suggesting where a record should point instead of a wireless address.
func WiredAddressOf(h inventory.Host) string {
	for _, iface := range h.Network.WorkingEthernet() {
		for _, addr := range iface.Addresses {
			ip, _, _ := strings.Cut(addr, "/")
			if ip != "" && !strings.Contains(ip, ":") {
				return ip
			}
		}
	}
	return ""
}

// FindStale returns unmanaged records not seen since the given time.
//
// Managed machines are never stale: hlab holds credentials for them, and a
// machine being switched off is not a reason to forget how to reach it.
func FindStale(hosts []inventory.Host, before time.Time) []inventory.Host {
	var out []inventory.Host
	for _, h := range hosts {
		if h.Managed || h.LastSeen.IsZero() {
			continue
		}
		if h.LastSeen.Before(before) {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RemoveHosts deletes records by name, along with any group membership.
func RemoveHosts(path string, names []string) error {
	if path == "" {
		return errors.New("no config path given")
	}
	if len(names) == 0 {
		return nil
	}

	doomed := make(map[string]struct{}, len(names))
	for _, n := range names {
		doomed[n] = struct{}{}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	file, err := parseBytes(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	hosts := migrateLegacyDiscovered(file)
	kept := make([]inventory.Host, 0, len(hosts))
	removed := 0
	for _, h := range hosts {
		if _, drop := doomed[h.Name]; drop {
			removed++
			continue
		}
		kept = append(kept, h)
	}
	if removed == 0 {
		return fmt.Errorf("none of those machines are in the config")
	}

	updated, err := replaceHostsSection(raw, kept)
	if err != nil {
		return err
	}
	if updated, err = dropFromGroups(updated, doomed); err != nil {
		return err
	}

	if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
		return fmt.Errorf("write backup %s.bak: %w", path, err)
	}
	return writeAtomic(path, updated)
}

// dropFromGroups removes deleted machines from every group, so no group is
// left referring to a record that no longer exists.
func dropFromGroups(raw []byte, doomed map[string]struct{}) ([]byte, error) {
	file, err := parseBytes(raw)
	if err != nil {
		return nil, err
	}
	if len(file.Groups) == 0 {
		return raw, nil
	}

	trimmed := make(map[string][]string, len(file.Groups))
	changed := false
	for name, members := range file.Groups {
		kept := make([]string, 0, len(members))
		for _, m := range members {
			if _, drop := doomed[m]; drop {
				changed = true
				continue
			}
			kept = append(kept, m)
		}
		trimmed[name] = kept
	}
	if !changed {
		return raw, nil
	}

	return rewriteGroups(raw, trimmed)
}
