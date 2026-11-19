package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/discover"
	"github.com/cl-tim/hlab/internal/inventory"
)

// candidate is a machine offered for enrolment. It comes either from a live
// scan or from a record an earlier scan stored, so the interactive walk does
// not care which.
type candidate struct {
	// Name is what the machine is, or would be, recorded as.
	Name        string
	Address     string
	Port        int
	Hostname    string
	OS          string
	Software    string
	Banner      string
	Description string
	LastSeen    time.Time
	// Stored reports whether this came from the config rather than a fresh scan.
	Stored bool
}

// candidateFromScan builds a candidate from a live scan result.
func candidateFromScan(r discover.Result) candidate {
	return candidate{
		Address:  r.Address,
		Port:     r.Port,
		Hostname: r.Hostname,
		OS:       r.OS,
		Software: r.Software,
		Banner:   r.Banner,
	}
}

// candidateFromHost builds a candidate from a stored, unmanaged record.
func candidateFromHost(h inventory.Host) candidate {
	software, _ := discover.ParseBanner(h.Banner)
	return candidate{
		Name:        h.Name,
		Address:     h.Address,
		Port:        h.Port,
		Hostname:    h.Hostname,
		OS:          h.OS,
		Software:    software,
		Banner:      h.Banner,
		Description: h.Description,
		LastSeen:    h.LastSeen,
		Stored:      true,
	}
}

// describe renders the one-line header shown above each machine's questions.
func (c candidate) describe() string {
	target := c.Address
	if c.Port != 0 && c.Port != 22 {
		target = fmt.Sprintf("%s:%d", c.Address, c.Port)
	}

	parts := []string{target}
	if name := shortHostname(c.Hostname); name != "" {
		parts = append(parts, name)
	}
	if c.OS != "" {
		parts = append(parts, c.OS)
	}
	if c.Software != "" {
		parts = append(parts, c.Software)
	}
	if seen := relativeTime(c.LastSeen); seen != "" {
		parts = append(parts, "last seen "+seen)
	}
	return strings.Join(parts, "  ")
}

// suggestedName proposes an inventory name that does not clash with an
// existing one. A stored record already has a name, so it keeps it.
func (c candidate) suggestedName(inv *inventory.Inventory) string {
	if c.Name != "" {
		return c.Name
	}

	base := ""
	if c.Hostname != "" {
		label, _, _ := strings.Cut(c.Hostname, ".")
		base = sanitiseInventoryName(label)
	}
	if base == "" {
		base = "host-" + sanitiseInventoryName(c.Address)
	}

	if inv == nil {
		return base
	}

	name := base
	for i := 2; ; i++ {
		if _, taken := inv.Host(name); !taken {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func sanitiseInventoryName(s string) string {
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
