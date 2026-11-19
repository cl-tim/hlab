package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/cl-tim/hlab/internal/inventory"
)

// Observation is what a scan saw at one address.
type Observation struct {
	Address  string
	Port     int
	Hostname string
	OS       string
	Banner   string

	// MatchName, when set, names the existing record this observation belongs
	// to. The caller supplies it when a host is recorded under a DNS name that
	// resolves to this address, which address matching alone would miss.
	MatchName string
}

// ChangeKind classifies what a scan did to a record.
type ChangeKind string

const (
	ChangeAdded     ChangeKind = "new"
	ChangeUpdated   ChangeKind = "changed"
	ChangeUnchanged ChangeKind = "unchanged"
)

// AssetChange describes what one scan observation did to the stored record.
type AssetChange struct {
	Name    string     `json:"name" yaml:"name"`
	Address string     `json:"address" yaml:"address"`
	Kind    ChangeKind `json:"kind" yaml:"kind"`
	// Changes describes each differing field, e.g. "ssh: OpenSSH_9.6p1 -> OpenSSH_10.0p2".
	Changes []string `json:"changes,omitempty" yaml:"changes,omitempty"`
}

// ScanDiff summarises what a scan changed.
type ScanDiff struct {
	Added     int
	Updated   int
	Unchanged int
	// ByAddress indexes the per-machine outcome so results can be annotated.
	ByAddress map[string]AssetChange
}

// MergeObservations folds scan results into the config's asset list.
//
// Machines already recorded are enriched in place: observed fields are
// refreshed and LastSeen advanced, while everything that makes a machine
// manageable — its name, credentials and tags — is left alone. A scan can never
// turn a managed machine back into an unmanaged one.
//
// Machines not seen before are appended as unmanaged records.
func MergeObservations(path string, observed []Observation, now time.Time) (ScanDiff, error) {
	diff := ScanDiff{ByAddress: map[string]AssetChange{}}

	if path == "" {
		return diff, errors.New("no config path given")
	}
	if len(observed) == 0 {
		return diff, nil
	}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		raw = nil
	default:
		return diff, fmt.Errorf("read %s: %w", path, err)
	}

	file, err := parseBytes(raw)
	if err != nil {
		return diff, fmt.Errorf("parse %s: %w", path, err)
	}

	hosts := migrateLegacyDiscovered(file)
	hosts, diff = mergeObservations(hosts, observed, now.Truncate(time.Second))

	updated, err := replaceHostsSection(raw, hosts)
	if err != nil {
		return ScanDiff{}, err
	}

	if len(raw) > 0 {
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			return ScanDiff{}, fmt.Errorf("write backup %s.bak: %w", path, err)
		}
	}
	if err := writeAtomic(path, updated); err != nil {
		return ScanDiff{}, err
	}

	return diff, nil
}

// mergeObservations applies observations to the recorded hosts.
func mergeObservations(hosts []inventory.Host, observed []Observation, now time.Time) ([]inventory.Host, ScanDiff) {
	diff := ScanDiff{ByAddress: map[string]AssetChange{}}

	byKey := make(map[string]int, len(hosts))
	byName := make(map[string]int, len(hosts))
	taken := make(map[string]struct{}, len(hosts))

	for i, h := range hosts {
		byKey[addressKey(h.Address, h.Port)] = i
		byName[h.Name] = i
		taken[h.Name] = struct{}{}
	}

	for _, obs := range observed {
		if obs.Port == 0 {
			obs.Port = 22
		}

		idx := -1
		if obs.MatchName != "" {
			if i, ok := byName[obs.MatchName]; ok {
				idx = i
			}
		}
		if idx < 0 {
			if i, ok := byKey[addressKey(obs.Address, obs.Port)]; ok {
				idx = i
			}
		}

		if idx < 0 {
			host := inventory.Host{
				Name:      uniqueName(suggestAssetName(obs), taken),
				Address:   obs.Address,
				Port:      obs.Port,
				Managed:   false,
				Hostname:  obs.Hostname,
				OS:        obs.OS,
				Banner:    obs.Banner,
				FirstSeen: now,
				LastSeen:  now,
			}
			taken[host.Name] = struct{}{}
			byKey[addressKey(host.Address, host.Port)] = len(hosts)
			byName[host.Name] = len(hosts)
			hosts = append(hosts, host)

			diff.Added++
			diff.ByAddress[obs.Address] = AssetChange{
				Name: host.Name, Address: host.Address, Kind: ChangeAdded,
			}
			continue
		}

		host := hosts[idx]
		changes := describeChanges(host, obs)

		host.Hostname = obs.Hostname
		host.OS = obs.OS
		host.Banner = obs.Banner
		host.LastSeen = now
		if host.FirstSeen.IsZero() {
			host.FirstSeen = now
		}
		hosts[idx] = host

		change := AssetChange{Name: host.Name, Address: host.Address, Kind: ChangeUnchanged}
		if len(changes) > 0 {
			change.Kind = ChangeUpdated
			change.Changes = changes
			diff.Updated++
		} else {
			diff.Unchanged++
		}
		diff.ByAddress[obs.Address] = change
	}

	sort.Slice(hosts, func(i, j int) bool { return lessByAddress(hosts[i], hosts[j]) })
	return hosts, diff
}

// describeChanges reports the observable differences between what is recorded
// and what a scan just saw. This is what makes a rescan able to say "the SSH
// version on this machine changed".
func describeChanges(host inventory.Host, obs Observation) []string {
	var changes []string

	add := func(label, was, now string) {
		if was == now {
			return
		}
		switch {
		case was == "":
			changes = append(changes, fmt.Sprintf("%s: %s", label, now))
		case now == "":
			changes = append(changes, fmt.Sprintf("%s: %s -> (gone)", label, was))
		default:
			changes = append(changes, fmt.Sprintf("%s: %s -> %s", label, was, now))
		}
	}

	add("hostname", host.Hostname, obs.Hostname)
	add("os", host.OS, obs.OS)
	add("ssh", host.Banner, obs.Banner)

	return changes
}

// suggestAssetName derives a readable record name from what was observed.
func suggestAssetName(obs Observation) string {
	if obs.Hostname != "" {
		label, _, _ := strings.Cut(obs.Hostname, ".")
		if name := sanitiseAssetName(label); name != "" {
			return name
		}
	}
	return "host-" + sanitiseAssetName(obs.Address)
}

func sanitiseAssetName(s string) string {
	var b strings.Builder
	lastDash := false

	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// uniqueName suffixes a name until it does not collide.
func uniqueName(base string, taken map[string]struct{}) string {
	if base == "" {
		base = "host"
	}
	name := base
	for i := 2; ; i++ {
		if _, exists := taken[name]; !exists {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func addressKey(address string, port int) string {
	if port == 0 {
		port = 22
	}
	return fmt.Sprintf("%s|%d", address, port)
}

// lessByAddress orders records numerically by IP so the file reads in the order
// a person expects. Sorting the address as a string puts .119 before .1.
func lessByAddress(a, b inventory.Host) bool {
	ipA, errA := netip.ParseAddr(a.Address)
	ipB, errB := netip.ParseAddr(b.Address)

	switch {
	case errA == nil && errB == nil:
		if ipA != ipB {
			return ipA.Less(ipB)
		}
	default:
		if a.Address != b.Address {
			return a.Address < b.Address
		}
	}
	return a.Port < b.Port
}

// migrateLegacyDiscovered folds a pre-unification `discovered:` section into
// the host list, so a config written by an older build keeps working.
func migrateLegacyDiscovered(file File) []inventory.Host {
	hosts := make([]inventory.Host, 0, len(file.Hosts))
	for _, e := range file.Hosts {
		hosts = append(hosts, e.resolve())
	}
	if len(file.Discovered) == 0 {
		return hosts
	}

	seen := make(map[string]struct{}, len(hosts))
	taken := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		seen[addressKey(h.Address, h.Port)] = struct{}{}
		taken[h.Name] = struct{}{}
	}

	for _, d := range file.Discovered {
		if _, dup := seen[addressKey(d.Address, d.Port)]; dup {
			continue
		}
		obs := Observation{Address: d.Address, Hostname: d.Hostname}
		host := inventory.Host{
			Name:      uniqueName(suggestAssetName(obs), taken),
			Address:   d.Address,
			Port:      d.Port,
			Hostname:  d.Hostname,
			OS:        d.OS,
			Banner:    d.Banner,
			FirstSeen: d.FirstSeen,
			LastSeen:  d.LastSeen,
		}
		taken[host.Name] = struct{}{}
		seen[addressKey(host.Address, host.Port)] = struct{}{}
		hosts = append(hosts, host)
	}
	return hosts
}

// replaceHostsSection rewrites the `hosts:` key, dropping any legacy
// `discovered:` section now that its contents have been folded in.
func replaceHostsSection(raw []byte, hosts []inventory.Host) ([]byte, error) {
	doc, root, err := documentRoot(raw)
	if err != nil {
		return nil, err
	}

	seq := findOrCreateSequence(root, "hosts")
	seq.Content = nil
	seq.Style = 0

	for _, h := range hosts {
		node, err := hostNode(h)
		if err != nil {
			return nil, err
		}
		seq.Content = append(seq.Content, node)
	}

	removeKey(root, "discovered")

	out, err := marshalDocument(doc)
	if err != nil {
		return nil, err
	}
	if _, err := parseBytes(out); err != nil {
		return nil, fmt.Errorf("refusing to write a config that would not load back: %w", err)
	}
	return out, nil
}

// removeKey deletes a top-level key from the document.
func removeKey(root *yaml.Node, key string) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			return
		}
	}
}
