// Package inventory models the set of machines hlab manages and the rules for
// selecting subsets of them.
package inventory

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Host is one machine hlab knows about, whether or not it manages it.
//
// A single record accumulates everything learned about a machine: what a scan
// observed, and — once enrolled — how to reach it. Managed reports which of
// those two states it is in.
type Host struct {
	Name    string `yaml:"name" json:"name"`
	Address string `yaml:"address" json:"address"`
	Port    int    `yaml:"port,omitempty" json:"port"`

	// Managed reports whether hlab has credentials for this machine. Only
	// managed machines are reachable, so only they can be selected by commands
	// that connect.
	//
	// It has no yaml tag because the on-disk form needs to tell an absent key
	// from an explicit false: see config.hostEntry.
	Managed bool `yaml:"-" json:"managed"`

	// Access details, meaningful only when Managed.
	User         string `yaml:"user,omitempty" json:"user,omitempty"`
	IdentityFile string `yaml:"identityFile,omitempty" json:"identityFile,omitempty"`

	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`

	// Observed fields, refreshed by each scan.
	Hostname  string    `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	OS        string    `yaml:"os,omitempty" json:"os,omitempty"`
	Banner    string    `yaml:"banner,omitempty" json:"banner,omitempty"`
	FirstSeen time.Time `yaml:"firstSeen,omitempty" json:"firstSeen,omitempty"`
	LastSeen  time.Time `yaml:"lastSeen,omitempty" json:"lastSeen,omitempty"`

	// Durable attributes of the machine, filled in by `hlab discover services`.
	// Volatile state — running services, listening ports, containers — is
	// deliberately not here: it belongs in the facts store, so this file stays
	// something a person can read and edit.
	Kernel   string       `yaml:"kernel,omitempty" json:"kernel,omitempty"`
	Arch     string       `yaml:"arch,omitempty" json:"arch,omitempty"`
	CPUs     int          `yaml:"cpus,omitempty" json:"cpus,omitempty"`
	MemoryMB int64        `yaml:"memoryMB,omitempty" json:"memoryMB,omitempty"`
	Network  *NetworkInfo `yaml:"network,omitempty" json:"network,omitempty"`

	// Containers are recorded here rather than in the facts store because they
	// describe what a machine is *for*, not what it happens to be doing: a
	// homelab's containers change about as often as its installed services.
	Containers []Container `yaml:"containers,omitempty" json:"containers,omitempty"`

	LastCollected time.Time `yaml:"lastCollected,omitempty" json:"lastCollected,omitempty"`
}

// Container is a running container on a managed machine.
type Container struct {
	Runtime string `yaml:"runtime" json:"runtime"`
	Name    string `yaml:"name" json:"name"`
	Image   string `yaml:"image,omitempty" json:"image,omitempty"`
	Status  string `yaml:"status,omitempty" json:"status,omitempty"`
}

// NetworkInfo is how a machine is attached to the network.
type NetworkInfo struct {
	Gateway string   `yaml:"gateway,omitempty" json:"gateway,omitempty"`
	DNS     []string `yaml:"dns,omitempty,flow" json:"dns,omitempty"`
	Search  []string `yaml:"search,omitempty,flow" json:"search,omitempty"`
	// Manager is what owns the network configuration on this machine —
	// NetworkManager, netplan, dhcpcd and so on. It decides how the
	// configuration can later be changed.
	Manager    string      `yaml:"manager,omitempty" json:"manager,omitempty"`
	Interfaces []Interface `yaml:"interfaces,omitempty" json:"interfaces,omitempty"`
}

// Interface kinds, as reported by the collector.
const (
	IfaceEthernet = "ethernet"
	IfaceWiFi     = "wifi"
	IfaceBridge   = "bridge"
	IfaceVirtual  = "virtual"
	IfaceLoopback = "loopback"
)

// Interface is one network interface on a machine.
//
// The MAC address matters beyond diagnostics: it identifies the hardware across
// DHCP changes, so a machine that moves address can still be recognised.
type Interface struct {
	Name string `yaml:"name" json:"name"`
	// Type is ethernet, wifi, bridge, virtual or loopback, determined from
	// /sys rather than guessed from the name.
	Type string `yaml:"type,omitempty" json:"type,omitempty"`
	MAC  string `yaml:"mac,omitempty" json:"mac,omitempty"`
	MTU  int    `yaml:"mtu,omitempty" json:"mtu,omitempty"`
	// Up is the administrative state; Carrier is whether a link is actually
	// present — a cable plugged in, or a wireless association. An interface can
	// be up with no carrier, which is exactly the case that must not be
	// mistaken for a working connection.
	Up      bool `yaml:"up" json:"up"`
	Carrier bool `yaml:"carrier" json:"carrier"`
	// SpeedMbps is the negotiated link speed, where the driver reports one.
	SpeedMbps int `yaml:"speedMbps,omitempty" json:"speedMbps,omitempty"`
	// DHCP reports whether any address on this interface was leased rather
	// than configured statically.
	DHCP      bool     `yaml:"dhcp" json:"dhcp"`
	Addresses []string `yaml:"addresses,omitempty,flow" json:"addresses,omitempty"`
}

// Working reports whether the interface is both up and carrying a link.
func (i Interface) Working() bool { return i.Up && i.Carrier }

// WorkingEthernet returns the ethernet interfaces that are up, carrying a link
// and hold an address — the ones a machine is genuinely reachable over.
func (n *NetworkInfo) WorkingEthernet() []Interface {
	if n == nil {
		return nil
	}
	var out []Interface
	for _, iface := range n.Interfaces {
		if iface.Type == IfaceEthernet && iface.Working() && len(iface.Addresses) > 0 {
			out = append(out, iface)
		}
	}
	return out
}

// ActiveWiFi returns the wireless interfaces that are currently associated.
func (n *NetworkInfo) ActiveWiFi() []Interface {
	if n == nil {
		return nil
	}
	var out []Interface
	for _, iface := range n.Interfaces {
		if iface.Type == IfaceWiFi && iface.Working() {
			out = append(out, iface)
		}
	}
	return out
}

// WiFiRedundant reports machines where wireless is active while a working
// ethernet link is also present — the candidates for turning WiFi off.
func (n *NetworkInfo) WiFiRedundant() bool {
	return len(n.ActiveWiFi()) > 0 && len(n.WorkingEthernet()) > 0
}

// PrimaryAddress returns the first non-loopback IPv4 address, which is the one
// worth showing in a table.
func (n *NetworkInfo) PrimaryAddress() string {
	if n == nil {
		return ""
	}
	for _, iface := range n.Interfaces {
		for _, addr := range iface.Addresses {
			if strings.Contains(addr, ":") {
				continue // IPv6
			}
			if !strings.HasPrefix(addr, "127.") {
				return addr
			}
		}
	}
	return ""
}

// MACs returns every MAC address observed on the machine.
func (n *NetworkInfo) MACs() []string {
	if n == nil {
		return nil
	}
	out := make([]string, 0, len(n.Interfaces))
	for _, iface := range n.Interfaces {
		if iface.MAC != "" {
			out = append(out, iface.MAC)
		}
	}
	return out
}

// Addr is the dial target, host:port.
func (h Host) Addr() string {
	return fmt.Sprintf("%s:%d", h.Address, h.Port)
}

// HasTag reports whether the host carries the given tag (case insensitive).
func (h Host) HasTag(tag string) bool {
	for _, t := range h.Tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

// Inventory is the resolved set of hosts plus named groups.
type Inventory struct {
	hosts  []Host
	byName map[string]Host
	groups map[string][]string
}

// New builds an inventory, validating that host names are unique and that
// every group member refers to a real host.
func New(hosts []Host, groups map[string][]string) (*Inventory, error) {
	inv := &Inventory{
		hosts:  make([]Host, 0, len(hosts)),
		byName: make(map[string]Host, len(hosts)),
		groups: make(map[string][]string, len(groups)),
	}

	for _, h := range hosts {
		if h.Name == "" {
			return nil, fmt.Errorf("host with address %q has no name", h.Address)
		}
		if h.Address == "" {
			return nil, fmt.Errorf("host %q has no address", h.Name)
		}
		if _, dup := inv.byName[h.Name]; dup {
			return nil, fmt.Errorf("duplicate host name %q", h.Name)
		}
		inv.byName[h.Name] = h
		inv.hosts = append(inv.hosts, h)
	}

	for name, members := range groups {
		if name == "all" {
			return nil, fmt.Errorf("group name %q is reserved", name)
		}
		for _, m := range members {
			if _, ok := inv.byName[m]; !ok {
				return nil, fmt.Errorf("group %q refers to unknown host %q", name, m)
			}
		}
		sorted := append([]string(nil), members...)
		sort.Strings(sorted)
		inv.groups[name] = sorted
	}

	sort.Slice(inv.hosts, func(i, j int) bool { return inv.hosts[i].Name < inv.hosts[j].Name })
	return inv, nil
}

// Hosts returns every known machine, managed or not, sorted by name.
func (i *Inventory) Hosts() []Host {
	return append([]Host(nil), i.hosts...)
}

// Managed returns only the machines hlab has credentials for.
func (i *Inventory) Managed() []Host {
	out := make([]Host, 0, len(i.hosts))
	for _, h := range i.hosts {
		if h.Managed {
			out = append(out, h)
		}
	}
	return out
}

// Unmanaged returns machines that have been seen but not enrolled.
func (i *Inventory) Unmanaged() []Host {
	out := make([]Host, 0, len(i.hosts))
	for _, h := range i.hosts {
		if !h.Managed {
			out = append(out, h)
		}
	}
	return out
}

// ByAddress looks a machine up by address and port, which is how scan results
// are matched to existing records.
func (i *Inventory) ByAddress(address string, port int) (Host, bool) {
	if port == 0 {
		port = 22
	}
	for _, h := range i.hosts {
		hp := h.Port
		if hp == 0 {
			hp = 22
		}
		if h.Address == address && hp == port {
			return h, true
		}
	}
	return Host{}, false
}

// Host looks a host up by exact name.
func (i *Inventory) Host(name string) (Host, bool) {
	h, ok := i.byName[name]
	return h, ok
}

// Groups returns the group name -> member names map.
func (i *Inventory) Groups() map[string][]string {
	out := make(map[string][]string, len(i.groups))
	for k, v := range i.groups {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// GroupNames returns the sorted list of group names.
func (i *Inventory) GroupNames() []string {
	names := make([]string, 0, len(i.groups))
	for k := range i.groups {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
