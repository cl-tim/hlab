// Package facts collects a read-only snapshot of what a managed host is
// running: OS, kernel, resources, services, listening ports and containers.
package facts

import (
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// Facts is one host's snapshot.
type Facts struct {
	Host     string `json:"host" yaml:"host"`
	Hostname string `json:"hostname,omitempty" yaml:"hostname,omitempty"`

	OS     OSInfo `json:"os" yaml:"os"`
	Kernel string `json:"kernel,omitempty" yaml:"kernel,omitempty"`
	Arch   string `json:"arch,omitempty" yaml:"arch,omitempty"`
	Init   string `json:"init,omitempty" yaml:"init,omitempty"`
	// Privilege is how much the collector could see: "root", "sudo" or "none".
	// Process names against listening ports, and containers on a host where the
	// account is not in the docker group, need more than "none".
	Privilege string `json:"privilege,omitempty" yaml:"privilege,omitempty"`

	UptimeSeconds int64     `json:"uptimeSeconds,omitempty" yaml:"uptimeSeconds,omitempty"`
	CPUs          int       `json:"cpus,omitempty" yaml:"cpus,omitempty"`
	Memory        MemInfo   `json:"memory" yaml:"memory"`
	LoadAverage   []float64 `json:"loadAverage,omitempty" yaml:"loadAverage,omitempty"`

	Network *inventory.NetworkInfo `json:"network,omitempty" yaml:"network,omitempty"`

	Disks      []Disk      `json:"disks,omitempty" yaml:"disks,omitempty"`
	Services   []Service   `json:"services,omitempty" yaml:"services,omitempty"`
	Ports      []Port      `json:"ports,omitempty" yaml:"ports,omitempty"`
	Containers []Container `json:"containers,omitempty" yaml:"containers,omitempty"`

	// Error is set when the host could not be reached or the snapshot could
	// not be collected. The other fields are then unpopulated.
	Error string `json:"error,omitempty" yaml:"error,omitempty"`
	// Partial is set when the host answered but some sections were missing,
	// which usually means a minimal system or a restricted login.
	Partial bool `json:"partial,omitempty" yaml:"partial,omitempty"`

	DurationMs int64 `json:"durationMs" yaml:"durationMs"`
}

// OK reports whether the snapshot was collected.
func (f Facts) OK() bool { return f.Error == "" }

// OSInfo is the parsed contents of /etc/os-release.
type OSInfo struct {
	Name    string `json:"name,omitempty" yaml:"name,omitempty"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
	Pretty  string `json:"pretty,omitempty" yaml:"pretty,omitempty"`
}

// Short renders the OS compactly for a table cell, e.g. "Ubuntu 24.04".
func (o OSInfo) Short() string {
	switch {
	case o.Name != "" && o.Version != "":
		// VERSION often carries a codename: "24.04.4 LTS (Noble Numbat)".
		version, _, _ := strings.Cut(o.Version, " (")
		return o.Name + " " + version
	case o.Pretty != "":
		return o.Pretty
	case o.Name != "":
		return o.Name
	default:
		return ""
	}
}

// MemInfo is total and available memory, in kibibytes as /proc/meminfo reports.
type MemInfo struct {
	TotalKB     int64 `json:"totalKB,omitempty" yaml:"totalKB,omitempty"`
	AvailableKB int64 `json:"availableKB,omitempty" yaml:"availableKB,omitempty"`
}

// UsedPercent is how much memory is in use, or -1 when unknown.
func (m MemInfo) UsedPercent() int {
	if m.TotalKB <= 0 || m.AvailableKB <= 0 {
		return -1
	}
	return int(float64(m.TotalKB-m.AvailableKB) / float64(m.TotalKB) * 100)
}

// Disk is one real (non-pseudo) filesystem.
type Disk struct {
	Filesystem string `json:"filesystem" yaml:"filesystem"`
	Type       string `json:"type" yaml:"type"`
	MountPoint string `json:"mountPoint" yaml:"mountPoint"`
	SizeKB     int64  `json:"sizeKB" yaml:"sizeKB"`
	UsedKB     int64  `json:"usedKB" yaml:"usedKB"`
	AvailKB    int64  `json:"availKB" yaml:"availKB"`
	UsePercent int    `json:"usePercent" yaml:"usePercent"`
}

// Service is a running init-managed service.
type Service struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// ShortName strips the .service suffix for display.
func (s Service) ShortName() string { return strings.TrimSuffix(s.Name, ".service") }

// Port is a TCP socket in the LISTEN state.
type Port struct {
	// Address is the bind address, normalised to "*" for wildcard binds.
	Address string `json:"address" yaml:"address"`
	Port    int    `json:"port" yaml:"port"`
	// Process is the listening program, empty unless the login could see it
	// (which generally means root).
	Process string `json:"process,omitempty" yaml:"process,omitempty"`
}

// Exposed reports whether the socket is reachable from off the machine, which
// is true for a wildcard bind and for any specific non-loopback address.
func (p Port) Exposed() bool { return ExposureRank(p.Address) > 0 }

// Scope describes the binding in words, for display.
func (p Port) Scope() string {
	switch ExposureRank(p.Address) {
	case 2:
		return "all interfaces"
	case 0:
		return "localhost"
	default:
		return p.Address
	}
}

// ExposureRank orders bind addresses by reachability, so that collapsing the
// several sockets of one service keeps the widest binding: 2 is a wildcard, 1 a
// specific externally reachable address, 0 loopback only.
func ExposureRank(address string) int {
	if address == "*" || address == "0.0.0.0" || address == "::" || address == "" {
		return 2
	}
	if ip := net.ParseIP(address); ip != nil && ip.IsLoopback() {
		return 0
	}
	if address == "localhost" {
		return 0
	}
	return 1
}

func (p Port) String() string {
	s := fmt.Sprintf("%s:%d", p.Address, p.Port)
	if p.Process != "" {
		s += " (" + p.Process + ")"
	}
	return s
}

// Container is a running container. It is an alias because containers are
// recorded on the host record, not in the facts store.
type Container = inventory.Container

// Uptime renders the uptime as a compact human string, e.g. "12d 4h".
func (f Facts) Uptime() string {
	if f.UptimeSeconds <= 0 {
		return ""
	}
	d := time.Duration(f.UptimeSeconds) * time.Second

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// RootDisk returns the filesystem mounted at /, if it was reported.
func (f Facts) RootDisk() (Disk, bool) {
	for _, d := range f.Disks {
		if d.MountPoint == "/" {
			return d, true
		}
	}
	return Disk{}, false
}

// ServiceNames returns the running service names, without .service suffixes.
func (f Facts) ServiceNames() []string {
	out := make([]string, 0, len(f.Services))
	for _, s := range f.Services {
		out = append(out, s.ShortName())
	}
	sort.Strings(out)
	return out
}

// SuggestedTags derives inventory tags from what was found, so discovered
// machines can be selected by role. The OS id and init system come through
// directly; well-known services and container runtimes are recognised by name.
func (f Facts) SuggestedTags() []string {
	set := map[string]struct{}{}

	if f.OS.ID != "" {
		set[sanitiseTag(f.OS.ID)] = struct{}{}
	}
	if f.Arch != "" {
		set[sanitiseTag(f.Arch)] = struct{}{}
	}

	for _, c := range f.Containers {
		set[c.Runtime] = struct{}{}
	}

	for _, s := range f.Services {
		if role, ok := serviceRoles[strings.ToLower(s.ShortName())]; ok {
			set[role] = struct{}{}
		}
	}
	for _, p := range f.Ports {
		if p.Process == "" {
			continue
		}
		if role, ok := serviceRoles[strings.ToLower(p.Process)]; ok {
			set[role] = struct{}{}
		}
	}

	out := make([]string, 0, len(set))
	for tag := range set {
		if tag != "" {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// serviceRoles maps a well-known service or process name to the inventory tag
// it implies. It is intentionally small: a wrong tag is worse than none.
var serviceRoles = map[string]string{
	"docker":          "docker",
	"containerd":      "docker",
	"podman":          "podman",
	"k3s":             "kubernetes",
	"kubelet":         "kubernetes",
	"sshd":            "ssh",
	"ssh":             "ssh",
	"nginx":           "web",
	"apache2":         "web",
	"httpd":           "web",
	"caddy":           "web",
	"traefik":         "web",
	"haproxy":         "proxy",
	"postgresql":      "database",
	"mariadb":         "database",
	"mysqld":          "database",
	"mysql":           "database",
	"mongod":          "database",
	"redis-server":    "cache",
	"redis":           "cache",
	"smbd":            "fileserver",
	"nmbd":            "fileserver",
	"nfs-server":      "fileserver",
	"smb":             "fileserver",
	"zfs-zed":         "zfs",
	"named":           "dns",
	"bind9":           "dns",
	"unbound":         "dns",
	"dnsmasq":         "dns",
	"pihole-FTL":      "dns",
	"pihole-ftl":      "dns",
	"prometheus":      "monitoring",
	"grafana-server":  "monitoring",
	"node_exporter":   "monitoring",
	"influxd":         "monitoring",
	"telegraf":        "monitoring",
	"zabbix-agent":    "monitoring",
	"plexmediaserver": "media",
	"jellyfin":        "media",
	"sonarr":          "media",
	"radarr":          "media",
	"wg-quick":        "vpn",
	"wireguard":       "vpn",
	"openvpn":         "vpn",
	"tailscaled":      "vpn",
	"postfix":         "mail",
	"dovecot":         "mail",
}

// sanitiseTag reduces a value to a safe lowercase tag.
func sanitiseTag(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			// drop
		}
	}
	return b.String()
}
