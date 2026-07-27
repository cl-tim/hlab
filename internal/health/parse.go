package health

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Severity is how much a finding matters.
type Severity string

const (
	// Fail is something that is broken now.
	Fail Severity = "fail"
	// Warn is something that will break if left alone.
	Warn Severity = "warn"
)

// Finding is one thing wrong with a machine.
type Finding struct {
	Check    string   `json:"check" yaml:"check"`
	Severity Severity `json:"severity" yaml:"severity"`
	Detail   string   `json:"detail" yaml:"detail"`
}

// Filesystem is one mounted filesystem's usage.
type Filesystem struct {
	Mount   string `json:"mount" yaml:"mount"`
	Percent int    `json:"percent" yaml:"percent"`
	Avail   string `json:"avail" yaml:"avail"`
}

// Container is one container's state.
type Container struct {
	Name     string `json:"name" yaml:"name"`
	State    string `json:"state" yaml:"state"`
	Health   string `json:"health,omitempty" yaml:"health,omitempty"`
	Restarts int    `json:"restarts" yaml:"restarts"`
}

// Stack is one managed stack's service count.
type Stack struct {
	Name    string `json:"name" yaml:"name"`
	Want    int    `json:"want" yaml:"want"`
	Running int    `json:"running" yaml:"running"`
}

// Report is one machine's health.
type Report struct {
	Host        string       `json:"host" yaml:"host"`
	Findings    []Finding    `json:"findings,omitempty" yaml:"findings,omitempty"`
	Filesystems []Filesystem `json:"filesystems,omitempty" yaml:"filesystems,omitempty"`
	Containers  []Container  `json:"containers,omitempty" yaml:"containers,omitempty"`
	Stacks      []Stack      `json:"stacks,omitempty" yaml:"stacks,omitempty"`
	FailedUnits []string     `json:"failedUnits,omitempty" yaml:"failedUnits,omitempty"`
	Reboot      bool         `json:"reboot" yaml:"reboot"`
	ClockSynced *bool        `json:"clockSynced,omitempty" yaml:"clockSynced,omitempty"`
	UptimeSec   int          `json:"uptimeSec,omitempty" yaml:"uptimeSec,omitempty"`
	Partial     bool         `json:"partial,omitempty" yaml:"partial,omitempty"`
	Err         string       `json:"error,omitempty" yaml:"error,omitempty"`
}

// Healthy reports whether nothing is wrong.
func (r Report) Healthy() bool { return r.Err == "" && len(r.Findings) == 0 }

// Worst returns the most severe finding level present.
func (r Report) Worst() Severity {
	worst := Severity("")
	for _, f := range r.Findings {
		if f.Severity == Fail {
			return Fail
		}
		worst = Warn
	}
	return worst
}

// Thresholds tune what counts as a problem.
type Thresholds struct {
	// DiskPercent is the usage above which a filesystem is a problem.
	DiskPercent int
	// Restarts is the restart count above which a container is treated as
	// crash-looping rather than merely having been restarted.
	Restarts int
	// YoungUptimeSec flags a machine that rebooted recently, since an
	// unexplained restart is worth noticing.
	YoungUptimeSec int
}

// DefaultThresholds are deliberately quiet: a check that cries wolf gets
// ignored, and an ignored check is worse than none.
func DefaultThresholds() Thresholds {
	return Thresholds{DiskPercent: 85, Restarts: 5, YoungUptimeSec: 600}
}

// Parse turns raw check output into a Report and decides what is wrong.
func Parse(host, raw string, t Thresholds) Report {
	r := Report{Host: host}
	sections := splitSections(raw)

	if _, ok := sections["end"]; !ok {
		r.Partial = true
		r.Findings = append(r.Findings, Finding{
			Check: "check", Severity: Warn,
			Detail: "the check was cut short, so this picture is incomplete",
		})
	}

	r.Filesystems = parseFilesystems(sections["disk"])
	for _, fs := range r.Filesystems {
		if fs.Percent >= t.DiskPercent {
			sev := Warn
			if fs.Percent >= 95 {
				sev = Fail
			}
			r.Findings = append(r.Findings, Finding{
				Check: "disk", Severity: sev,
				Detail: fmt.Sprintf("%s is %d%% full (%s free)", fs.Mount, fs.Percent, fs.Avail),
			})
		}
	}

	r.FailedUnits = lines(sections["units"])
	if len(r.FailedUnits) > 0 {
		r.Findings = append(r.Findings, Finding{
			Check: "units", Severity: Fail,
			Detail: fmt.Sprintf("%s failed", strings.Join(r.FailedUnits, ", ")),
		})
	}

	r.Containers = parseContainers(sections["containers"])
	for _, c := range r.Containers {
		switch {
		case c.Health == "unhealthy":
			// The container itself says it is not working, which is a stronger
			// signal than anything inferred from outside it.
			r.Findings = append(r.Findings, Finding{
				Check: "containers", Severity: Fail,
				Detail: fmt.Sprintf("%s reports itself unhealthy", c.Name),
			})
		case c.State == "restarting":
			r.Findings = append(r.Findings, Finding{
				Check: "containers", Severity: Fail,
				Detail: fmt.Sprintf("%s is restarting", c.Name),
			})
		case c.State == "exited" || c.State == "dead":
			r.Findings = append(r.Findings, Finding{
				Check: "containers", Severity: Fail,
				Detail: fmt.Sprintf("%s is %s", c.Name, c.State),
			})
		case c.Restarts >= t.Restarts:
			r.Findings = append(r.Findings, Finding{
				Check: "containers", Severity: Warn,
				Detail: fmt.Sprintf("%s has restarted %d times", c.Name, c.Restarts),
			})
		}
	}

	r.Stacks = parseStacks(sections["stacks"])
	for _, st := range r.Stacks {
		if st.Running < st.Want {
			r.Findings = append(r.Findings, Finding{
				Check: "stacks", Severity: Fail,
				Detail: fmt.Sprintf("%s has %d of %d services running", st.Name, st.Running, st.Want),
			})
		}
	}

	if firstLine(sections["reboot"]) == "required" {
		r.Reboot = true
		r.Findings = append(r.Findings, Finding{
			Check: "reboot", Severity: Warn, Detail: "a reboot is pending",
		})
	}

	if v := firstLine(sections["clock"]); v != "" {
		synced := v == "yes"
		r.ClockSynced = &synced
		if !synced {
			r.Findings = append(r.Findings, Finding{
				Check: "clock", Severity: Warn,
				Detail: "the clock is not synchronised, which breaks TLS in confusing ways",
			})
		}
	}

	if secs := firstLine(sections["uptime"]); secs != "" {
		if f, err := strconv.ParseFloat(secs, 64); err == nil {
			r.UptimeSec = int(f)
			if r.UptimeSec < t.YoungUptimeSec {
				r.Findings = append(r.Findings, Finding{
					Check: "uptime", Severity: Warn,
					Detail: fmt.Sprintf("restarted %s ago", shortDuration(r.UptimeSec)),
				})
			}
		}
	}

	sort.SliceStable(r.Findings, func(i, j int) bool {
		return r.Findings[i].Severity == Fail && r.Findings[j].Severity != Fail
	})
	return r
}

func parseFilesystems(s string) []Filesystem {
	var out []Filesystem
	for _, line := range lines(s) {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pct, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		fs := Filesystem{Mount: f[0], Percent: pct}
		if len(f) >= 3 {
			fs.Avail = f[2]
		}
		out = append(out, fs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Mount < out[j].Mount })
	return out
}

func parseContainers(s string) []Container {
	var out []Container
	for _, line := range lines(s) {
		parts := strings.Split(line, "|")
		if len(parts) != 4 {
			continue
		}
		restarts, _ := strconv.Atoi(parts[3])
		health := parts[2]
		if health == "-" {
			health = ""
		}
		out = append(out, Container{
			Name: parts[0], State: parts[1], Health: health, Restarts: restarts,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func parseStacks(s string) []Stack {
	var out []Stack
	for _, line := range lines(s) {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		want, err1 := strconv.Atoi(f[1])
		have, err2 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, Stack{Name: f[0], Want: want, Running: have})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func shortDuration(secs int) string {
	switch {
	case secs >= 3600:
		return fmt.Sprintf("%dh", secs/3600)
	case secs >= 60:
		return fmt.Sprintf("%dm", secs/60)
	default:
		return fmt.Sprintf("%ds", secs)
	}
}

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
