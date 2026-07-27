package facts

import (
	"sort"
	"strconv"
	"strings"
)

// splitSections breaks the collection script's output into its named sections.
// Anything before the first marker is ignored, which tolerates login banners
// and MOTD text that some hosts print ahead of the command output.
func splitSections(raw string) map[string]string {
	sections := make(map[string]string)

	var current string
	var body []string

	flush := func() {
		if current != "" {
			sections[current] = strings.Join(body, "\n")
		}
		body = body[:0]
	}

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if name, ok := sectionName(trimmed); ok {
			flush()
			current = name
			continue
		}
		if current != "" {
			body = append(body, trimmed)
		}
	}
	flush()

	return sections
}

// sectionName recognises a "##HLAB#name#" marker line.
func sectionName(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, SectionPrefix) || !strings.HasSuffix(line, "#") {
		return "", false
	}
	name := strings.TrimSuffix(strings.TrimPrefix(line, SectionPrefix), "#")
	if name == "" || strings.ContainsAny(name, " \t#") {
		return "", false
	}
	return name, true
}

// Parse turns raw collection output into Facts. It is tolerant by design: a
// host missing a tool simply yields an empty section, and anything that fails
// to parse is skipped rather than failing the whole snapshot.
func Parse(host, raw string) Facts {
	f := Facts{Host: host}
	sections := splitSections(raw)

	// The script emits an "end" marker last, so its absence means the output
	// was truncated — the connection dropped or the command was killed.
	if _, complete := sections["end"]; !complete {
		f.Partial = true
	}

	f.Hostname = firstLine(sections["hostname"])
	f.OS = parseOSRelease(sections["os-release"])
	f.Kernel = firstLine(sections["kernel"])
	f.Arch = firstLine(sections["arch"])
	f.Init = firstLine(sections["init"])
	f.Privilege = firstLine(sections["privileged"])
	f.UptimeSeconds = parseUptime(sections["uptime"])
	f.CPUs = parseInt(firstLine(sections["cpu"]))
	f.Memory = parseMemInfo(sections["memory"])
	f.LoadAverage = parseLoadAvg(sections["load"])
	f.Disks = parseDisks(sections["disk"])
	f.Services = parseServices(sections["services"])
	f.Ports = parsePorts(sections["ports"])
	f.Containers = parseContainers(sections["containers"])
	f.Network = parseNetwork(sections["interfaces"], sections["interface-detail"],
		sections["netmanager"], sections["gateway"], sections["dns"])

	return f
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func parseInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// parseOSRelease reads the KEY=VALUE pairs of /etc/os-release.
func parseOSRelease(s string) OSInfo {
	var os OSInfo

	for _, line := range strings.Split(s, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"'`)

		switch key {
		case "NAME":
			os.Name = value
		case "VERSION":
			os.Version = value
		case "ID":
			os.ID = value
		case "PRETTY_NAME":
			os.Pretty = value
		case "VERSION_ID":
			// Only a fallback: VERSION is more descriptive when present.
			if os.Version == "" {
				os.Version = value
			}
		}
	}

	return os
}

// parseUptime reads the first field of /proc/uptime.
func parseUptime(s string) int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0
	}
	return int64(secs)
}

// parseMemInfo reads the MemTotal and MemAvailable lines of /proc/meminfo.
func parseMemInfo(s string) MemInfo {
	var m MemInfo

	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "MemTotal":
			m.TotalKB = v
		case "MemAvailable":
			m.AvailableKB = v
		}
	}

	return m
}

// parseLoadAvg reads the three load figures from /proc/loadavg.
func parseLoadAvg(s string) []float64 {
	fields := strings.Fields(s)
	if len(fields) < 3 {
		return nil
	}
	out := make([]float64, 0, 3)
	for _, f := range fields[:3] {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return nil
		}
		out = append(out, v)
	}
	return out
}

// pseudoFilesystems never represent real storage, so they are dropped from the
// disk list. Filtering by type rather than by an allowlist means a filesystem
// this code has never heard of still shows up.
var pseudoFilesystems = map[string]struct{}{
	"tmpfs": {}, "devtmpfs": {}, "devpts": {}, "proc": {}, "sysfs": {},
	"cgroup": {}, "cgroup2": {}, "overlay": {}, "squashfs": {}, "ramfs": {},
	"rootfs": {}, "mqueue": {}, "hugetlbfs": {}, "debugfs": {}, "tracefs": {},
	"securityfs": {}, "pstore": {}, "efivarfs": {}, "autofs": {}, "configfs": {},
	"binfmt_misc": {}, "fusectl": {}, "nsfs": {}, "bpf": {}, "9p": {},
	"fuse.snapfuse": {}, "fuse.gvfsd-fuse": {}, "fuse.portal": {},
	"none": {}, "udev": {},
}

// parseDisks reads `df -PT` output, skipping pseudo filesystems.
func parseDisks(s string) []Disk {
	var disks []Disk

	for i, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// Skip the header row.
		if i == 0 && strings.HasPrefix(line, "Filesystem") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}

		fsType := fields[1]
		if _, pseudo := pseudoFilesystems[fsType]; pseudo {
			continue
		}

		// A mount point may contain spaces, so take the rest of the line.
		mount := strings.Join(fields[6:], " ")
		if strings.HasPrefix(mount, "/snap/") {
			continue
		}

		size, err1 := strconv.ParseInt(fields[2], 10, 64)
		used, err2 := strconv.ParseInt(fields[3], 10, 64)
		avail, err3 := strconv.ParseInt(fields[4], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		disks = append(disks, Disk{
			Filesystem: fields[0],
			Type:       fsType,
			MountPoint: mount,
			SizeKB:     size,
			UsedKB:     used,
			AvailKB:    avail,
			UsePercent: parseInt(strings.TrimSuffix(fields[5], "%")),
		})
	}

	sort.Slice(disks, func(i, j int) bool { return disks[i].MountPoint < disks[j].MountPoint })
	return disks
}

// parseServices reads `systemctl list-units --plain --no-legend` output, or
// `rc-status -s` on OpenRC systems.
func parseServices(s string) []Service {
	var services []Service

	for _, line := range strings.Split(s, "\n") {
		// systemd marks some units with a leading bullet.
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "●*→"))
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		// OpenRC: "sshd  [  started  ]".
		if strings.Contains(line, "[") && strings.Contains(line, "]") {
			if !strings.Contains(strings.ToLower(line), "started") {
				continue
			}
			services = append(services, Service{Name: fields[0]})
			continue
		}

		// systemd: UNIT LOAD ACTIVE SUB DESCRIPTION.
		if len(fields) < 4 {
			continue
		}
		svc := Service{Name: fields[0]}
		if len(fields) > 4 {
			svc.Description = strings.Join(fields[4:], " ")
		}
		services = append(services, svc)
	}

	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	return services
}

// parsePorts reads either `ss -H -tln[p]` or `netstat -tln[p]` output.
//
// The same service usually appears twice, bound to both 0.0.0.0 and [::].
// Those are collapsed into one entry per port and process, keeping the widest
// binding seen, so the count reflects services rather than sockets and the
// address still shows whether the port is actually exposed.
func parsePorts(s string) []Port {
	type key struct {
		port    int
		process string
	}
	merged := make(map[key]Port)

	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		var local, process string
		switch {
		case strings.HasPrefix(strings.ToLower(fields[0]), "tcp"):
			// netstat: Proto Recv-Q Send-Q Local Foreign State [PID/Program]
			if len(fields) < 6 || !strings.EqualFold(fields[5], "LISTEN") {
				continue
			}
			local = fields[3]
			if len(fields) >= 7 {
				process = netstatProcess(fields[6])
			}
		case strings.EqualFold(fields[0], "LISTEN"):
			// ss: State Recv-Q Send-Q Local Peer [users:(("name",pid=..,fd=..))]
			local = fields[3]
			process = ssProcess(line)
		default:
			continue
		}

		address, port, ok := splitHostPort(local)
		if !ok {
			continue
		}

		k := key{port: port, process: process}
		existing, seen := merged[k]
		if !seen {
			merged[k] = Port{Address: address, Port: port, Process: process}
			continue
		}
		// Keep the widest binding, so collapsing several sockets never
		// understates how reachable the service actually is.
		if ExposureRank(address) > ExposureRank(existing.Address) {
			existing.Address = address
			merged[k] = existing
		}
	}

	out := make([]Port, 0, len(merged))
	for _, p := range merged {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Process < out[j].Process
	})
	return out
}

// splitHostPort splits an ss/netstat local address such as "0.0.0.0:22",
// "[::]:22", "*:80" or "127.0.0.53%lo:53".
func splitHostPort(s string) (address string, port int, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, false
	}

	host := s[:i]
	port, err := strconv.Atoi(s[i+1:])
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, false
	}

	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	// Drop a scope suffix: "127.0.0.53%lo" -> "127.0.0.53".
	if j := strings.Index(host, "%"); j >= 0 {
		host = host[:j]
	}
	if host == "" || host == "*" || host == "0.0.0.0" || host == "::" {
		host = "*"
	}

	return host, port, true
}

// ssProcess pulls the first program name out of an ss users:(...) field.
func ssProcess(line string) string {
	i := strings.Index(line, `users:(("`)
	if i < 0 {
		return ""
	}
	rest := line[i+len(`users:(("`):]
	name, _, ok := strings.Cut(rest, `"`)
	if !ok {
		return ""
	}
	return name
}

// netstatProcess turns netstat's "800/sshd" field into "sshd".
func netstatProcess(field string) string {
	_, name, ok := strings.Cut(field, "/")
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}

// parseContainers reads the "runtime|name|image|status" lines emitted by the
// script. Anything that is not four fields is ignored, which discards the
// stray messages some docker shims print on stdout.
func parseContainers(s string) []Container {
	var containers []Container
	// The script may ask both unprivileged and via sudo, so the same container
	// can be reported twice.
	seen := make(map[string]struct{})

	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Split(line, "|")
		if len(parts) != 4 {
			continue
		}
		runtime := strings.TrimSpace(parts[0])
		if runtime != "docker" && runtime != "podman" {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if name == "" {
			continue
		}

		key := runtime + "|" + name
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		containers = append(containers, Container{
			Runtime: runtime,
			Name:    name,
			Image:   strings.TrimSpace(parts[2]),
			Status:  strings.TrimSpace(parts[3]),
		})
	}

	sort.Slice(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers
}
