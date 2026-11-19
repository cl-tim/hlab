package updates

import (
	"context"
	"sort"

	"github.com/cl-tim/hlab/internal/sshx"
)

// Checker asks many hosts at once what is out of date.
type Checker struct {
	exec *sshx.Executor
}

// NewChecker wraps an SSH executor, reusing its fan-out and concurrency
// limiting rather than reimplementing them.
func NewChecker(exec *sshx.Executor) *Checker {
	return &Checker{exec: exec}
}

// Check runs the read-only script on every target and parses the output.
// A host that could not be reached is reported through its own Status.Err
// rather than failing the run — one unreachable machine should not hide what
// the rest of the fleet needs.
func (c *Checker) Check(ctx context.Context, targets []sshx.Target) []Status {
	results := c.exec.Run(ctx, targets, CheckScript)

	out := make([]Status, 0, len(results))
	for _, r := range results {
		out = append(out, statusFromResult(r))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

func statusFromResult(r sshx.Result) Status {
	if r.Err != nil {
		return Status{Host: r.Host, Err: r.Err.Error()}
	}
	s := Parse(r.Host, r.Stdout)
	// A non-zero exit with no parseable output is a failure worth naming; one
	// with output is a host that reported what it could, which is the whole
	// point of probing before use.
	if r.ExitCode != 0 && s.Manager == "" {
		s.Err = firstNonEmpty(r.Stderr, "the check exited non-zero")
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
