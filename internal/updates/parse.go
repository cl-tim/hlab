package updates

import (
	"sort"
	"strings"
)

// Package is one upgradable package.
type Package struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
	// Security is set when the new version comes from a security archive.
	Security bool `json:"security,omitempty" yaml:"security,omitempty"`
}

// Container is one running container and whether it is behind its own image.
type Container struct {
	Name  string `json:"name" yaml:"name"`
	Image string `json:"image" yaml:"image"`
	Stale bool   `json:"stale,omitempty" yaml:"stale,omitempty"`
}

// Status is what one machine reported.
type Status struct {
	Host    string `json:"host" yaml:"host"`
	Manager string `json:"manager,omitempty" yaml:"manager,omitempty"`

	// Packages is what an upgrade would install.
	Packages []Package `json:"packages,omitempty" yaml:"packages,omitempty"`
	// HeldBack is upgradable but would not be installed by `upgrade`, because
	// doing so would mean removing something. Reported rather than hidden: it
	// is the difference between "up to date" and "as up to date as is safe".
	HeldBack []string `json:"heldBack,omitempty" yaml:"heldBack,omitempty"`

	Containers []Container `json:"containers,omitempty" yaml:"containers,omitempty"`
	Stacks     []string    `json:"stacks,omitempty" yaml:"stacks,omitempty"`

	Reboot     bool     `json:"reboot" yaml:"reboot"`
	RebootPkgs []string `json:"rebootPkgs,omitempty" yaml:"rebootPkgs,omitempty"`
	Kernel     string   `json:"kernel,omitempty" yaml:"kernel,omitempty"`

	// IndexStale is set when the package index could not be refreshed, so the
	// counts below it describe whatever the machine last knew.
	IndexStale bool `json:"indexStale,omitempty" yaml:"indexStale,omitempty"`
	// Partial means the output was cut short and the picture is incomplete.
	Partial bool `json:"partial,omitempty" yaml:"partial,omitempty"`

	Err string `json:"error,omitempty" yaml:"error,omitempty"`
}

// SecurityCount reports how many pending packages come from a security archive.
func (s Status) SecurityCount() int {
	n := 0
	for _, p := range s.Packages {
		if p.Security {
			n++
		}
	}
	return n
}

// StaleContainers reports containers running an image their stack has moved on
// from.
func (s Status) StaleContainers() []Container {
	var out []Container
	for _, c := range s.Containers {
		if c.Stale {
			out = append(out, c)
		}
	}
	return out
}

// UpToDate reports whether there is nothing at all to do.
func (s Status) UpToDate() bool {
	return len(s.Packages) == 0 && len(s.StaleContainers()) == 0 && !s.Reboot
}

// Parse turns raw check output into a Status.
//
// Lenient by design, like fact collection: an unparseable line is skipped
// rather than failing the host. The one hard signal is the trailing end
// marker, whose absence means the output was truncated.
func Parse(host, raw string) Status {
	s := Status{Host: host}
	sections := splitSections(raw)

	if _, ok := sections["end"]; !ok {
		s.Partial = true
	}
	s.Manager = firstLine(sections["manager"])
	s.Kernel = firstLine(sections["kernel"])
	s.IndexStale = firstLine(sections["refresh"]) == "failed"

	// Security archives are only visible in the upgradable listing, so that is
	// read first and used to mark the packages an upgrade would install.
	security := map[string]bool{}
	upgradable := map[string]bool{}
	for _, line := range lines(sections["upgradable"]) {
		name, archive, ok := strings.Cut(line, " ")
		if !ok || name == "" {
			continue
		}
		upgradable[name] = true
		if strings.Contains(archive, "-security") {
			security[name] = true
		}
	}

	installing := map[string]bool{}
	for _, line := range lines(sections["packages"]) {
		name, version, ok := strings.Cut(line, " ")
		if !ok || name == "" {
			continue
		}
		installing[name] = true
		s.Packages = append(s.Packages, Package{
			Name: name, Version: version, Security: security[name],
		})
	}
	sort.Slice(s.Packages, func(i, j int) bool { return s.Packages[i].Name < s.Packages[j].Name })

	for name := range upgradable {
		if !installing[name] {
			s.HeldBack = append(s.HeldBack, name)
		}
	}
	sort.Strings(s.HeldBack)

	if reboot := lines(sections["reboot"]); len(reboot) > 0 && reboot[0] == "required" {
		s.Reboot = true
		s.RebootPkgs = reboot[1:]
		sort.Strings(s.RebootPkgs)
	}

	for _, line := range lines(sections["containers"]) {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		s.Containers = append(s.Containers, Container{
			Name: fields[0], Image: fields[1], Stale: fields[2] == "stale",
		})
	}
	sort.Slice(s.Containers, func(i, j int) bool { return s.Containers[i].Name < s.Containers[j].Name })

	s.Stacks = lines(sections["stacks"])
	sort.Strings(s.Stacks)

	return s
}

// splitSections breaks the output on the "##HLAB#name#" markers.
func splitSections(raw string) map[string]string {
	sections := map[string]string{}

	name := ""
	var body strings.Builder
	flush := func() {
		if name != "" {
			sections[name] = body.String()
		}
		body.Reset()
	}

	for _, line := range strings.Split(raw, "\n") {
		if n, ok := sectionName(line); ok {
			flush()
			name = n
			continue
		}
		if name == "" {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	flush()

	return sections
}

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

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func firstLine(s string) string {
	if l := lines(s); len(l) > 0 {
		return l[0]
	}
	return ""
}
