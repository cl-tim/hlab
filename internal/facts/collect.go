package facts

import (
	"context"
	"sort"
	"strings"

	"github.com/cl-tim/hlab/internal/sshx"
)

// Collector gathers snapshots from many hosts at once.
type Collector struct {
	exec *sshx.Executor
}

// NewCollector wraps an SSH executor, reusing its fan-out and concurrency
// limiting rather than reimplementing them.
func NewCollector(exec *sshx.Executor) *Collector {
	return &Collector{exec: exec}
}

// Collect runs the collection script on every target and parses the output.
// Individual host failures are reported in the corresponding Facts.Error, not
// as a returned error.
func (c *Collector) Collect(ctx context.Context, targets []sshx.Target) []Facts {
	results := c.exec.Run(ctx, targets, CollectScript)

	out := make([]Facts, 0, len(results))
	for _, r := range results {
		out = append(out, factsFromResult(r))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// factsFromResult converts one command result into a snapshot.
func factsFromResult(r sshx.Result) Facts {
	if r.Err != nil {
		return Facts{Host: r.Host, Error: r.Err.Error(), DurationMs: r.DurationMs}
	}

	// The script probes for every tool it uses, so a non-zero exit with no
	// usable output means something more fundamental went wrong — a login
	// shell that rejected the command, for instance.
	if !strings.Contains(r.Stdout, SectionPrefix) {
		msg := strings.TrimSpace(r.Stderr)
		if msg == "" {
			msg = "host returned no recognisable output; is the login shell a POSIX shell?"
		}
		return Facts{Host: r.Host, Error: msg, DurationMs: r.DurationMs}
	}

	f := Parse(r.Host, r.Stdout)
	f.DurationMs = r.DurationMs
	return f
}
