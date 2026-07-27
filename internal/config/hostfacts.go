package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// FactsPath is where collected host state is stored, alongside the config.
func FactsPath() string {
	return filepath.Join(Home(), "facts.yaml")
}

// HostAttributes are the durable properties of a machine, learned by
// collection. They live on the host record because they describe the asset
// itself rather than what it happens to be doing.
type HostAttributes struct {
	Name string
	// Hostname is what the machine calls itself. It supersedes anything a scan
	// inferred from reverse DNS, which can be wrong or shared: a DNS server
	// answering PTR queries for its whole subnet will hand the same name to
	// every address on it.
	Hostname string
	Kernel   string
	Arch     string
	CPUs     int
	MemoryMB int64
	Network  *inventory.NetworkInfo
	OS       string
	// Containers is applied wholesale, including when empty, so a container
	// that has been removed disappears from the record rather than lingering.
	Containers []inventory.Container
}

// UpdateHostAttributes writes collected attributes onto the matching host
// records, leaving names, credentials, tags and everything else untouched.
//
// Hosts with no matching update keep whatever they already had.
func UpdateHostAttributes(path string, updates []HostAttributes, now time.Time) (int, error) {
	if path == "" {
		return 0, errors.New("no config path given")
	}
	if len(updates) == 0 {
		return 0, nil
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", path, err)
	}

	file, err := parseBytes(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	hosts := migrateLegacyDiscovered(file)

	byName := make(map[string]int, len(hosts))
	for i, h := range hosts {
		byName[h.Name] = i
	}

	stamp := now.Truncate(time.Second)
	applied := 0

	for _, u := range updates {
		idx, ok := byName[u.Name]
		if !ok {
			continue
		}

		host := hosts[idx]
		// The machine's own answer beats reverse DNS.
		if u.Hostname != "" {
			host.Hostname = u.Hostname
		}
		if u.Kernel != "" {
			host.Kernel = u.Kernel
		}
		if u.Arch != "" {
			host.Arch = u.Arch
		}
		if u.CPUs > 0 {
			host.CPUs = u.CPUs
		}
		if u.MemoryMB > 0 {
			host.MemoryMB = u.MemoryMB
		}
		if u.Network != nil {
			host.Network = u.Network
		}
		// Replaced rather than merged: a container that has gone away must not
		// stay in the record.
		host.Containers = u.Containers
		// Collection sees the OS properly, where a scan could only guess from
		// the SSH banner.
		if u.OS != "" {
			host.OS = u.OS
		}
		host.LastCollected = stamp

		hosts[idx] = host
		applied++
	}

	if applied == 0 {
		return 0, nil
	}

	updated, err := replaceHostsSection(raw, hosts)
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
		return 0, fmt.Errorf("write backup %s.bak: %w", path, err)
	}
	if err := writeAtomic(path, updated); err != nil {
		return 0, err
	}

	return applied, nil
}
