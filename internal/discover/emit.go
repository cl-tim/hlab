package discover

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// EmitInventory writes a YAML fragment describing the given results, ready to
// review and merge into the config. It deliberately writes to a stream rather
// than editing the config in place: the operator decides what to keep, and what
// user each machine should be reached as.
//
// taken is the set of host names already in use, so suggested names never
// collide with the existing inventory.
func EmitInventory(w io.Writer, results []Result, taken map[string]struct{}, command string, now time.Time) error {
	if len(results) == 0 {
		return nil
	}

	claimed := make(map[string]struct{}, len(taken)+len(results))
	for name := range taken {
		claimed[name] = struct{}{}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Discovered by `%s` on %s.\n", command, now.Format("2006-01-02 15:04"))
	b.WriteString("# Review each entry and set the correct user, then merge into your config.\n")
	b.WriteString("hosts:\n")

	for _, r := range results {
		name := suggestName(r, claimed)
		claimed[name] = struct{}{}

		fmt.Fprintf(&b, "  - name: %s\n", name)
		fmt.Fprintf(&b, "    address: %s\n", r.Address)
		if r.Port != 22 {
			fmt.Fprintf(&b, "    port: %d\n", r.Port)
		}
		if tags := suggestTags(r); len(tags) > 0 {
			fmt.Fprintf(&b, "    tags: [%s]\n", strings.Join(tags, ", "))
		}
		if r.Banner != "" {
			fmt.Fprintf(&b, "    description: %s\n", yamlScalar(r.Banner))
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// suggestName derives a stable, readable inventory name for a discovered host,
// preferring its reverse DNS name and falling back to the address.
func suggestName(r Result, claimed map[string]struct{}) string {
	base := ""
	if r.Hostname != "" {
		// Use the first DNS label: "nas-01.lan." -> "nas-01".
		label, _, _ := strings.Cut(r.Hostname, ".")
		base = sanitiseName(label)
	}
	if base == "" {
		base = "host-" + sanitiseName(r.Address)
	}

	name := base
	for i := 2; ; i++ {
		if _, exists := claimed[name]; !exists {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

// sanitiseName reduces a string to lowercase alphanumerics and single dashes.
func sanitiseName(s string) string {
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

// suggestTags seeds the entry with something useful to select on later.
func suggestTags(r Result) []string {
	tags := []string{"discovered"}
	if r.OS != "" {
		tags = append(tags, sanitiseName(r.OS))
	}
	return tags
}

// yamlScalar renders a value as a safe single-line YAML scalar. Banners are
// controlled by whatever is listening on the port, so this must neither trust
// the content nor let it escape onto another line.
func yamlScalar(s string) string {
	// Drop control characters, including the newlines that would otherwise let
	// a crafted banner inject its own YAML keys.
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)

	s = strings.TrimSpace(s)
	if s == "" {
		return `""`
	}

	needsQuote := strings.ContainsAny(s, ":#{}[],&*?|>=!%@`\"'\\\n\t") ||
		strings.HasPrefix(s, "-") ||
		strings.HasPrefix(s, " ") ||
		strings.HasSuffix(s, " ")

	if !needsQuote {
		return s
	}

	// Single-quoted YAML only needs the quote itself escaped, by doubling.
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
