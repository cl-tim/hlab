// Package probe reads migration state from the native backend.
// An unreachable printer produces a valid report with indeterminate checks,
// allowing migration to resume across a host eMMC swap.
package probe

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
)

// Status is a check's verdict. Indeterminate preserves an unknown result
// for the migration engine to resolve.
type Status string

const (
	Satisfied     Status = "satisfied"
	Unsatisfied   Status = "unsatisfied"
	Indeterminate Status = "indeterminate"
)

type Result struct {
	Probe  string `json:"probe" yaml:"probe"`
	Stage  string `json:"stage" yaml:"stage"`
	Status Status `json:"status" yaml:"status"`
	Detail string `json:"detail" yaml:"detail"`
}

type Report struct {
	Schema    int            `json:"schema" yaml:"schema"`
	Printer   string         `json:"printer" yaml:"printer"`
	Reachable bool           `json:"reachable" yaml:"reachable"`
	Probes    []Result       `json:"probes" yaml:"probes"`
	Summary   map[string]int `json:"summary,omitempty" yaml:"summary,omitempty"`
}

// Statuses indexes the report by probe id for reconciliation.
func (r Report) Statuses() map[string]Status {
	statuses := make(map[string]Status, len(r.Probes))
	for _, result := range r.Probes {
		statuses[result.Probe] = result.Status
	}
	return statuses
}

// Details indexes each probe's human-readable explanation by probe id.
func (r Report) Details() map[string]string {
	details := make(map[string]string, len(r.Probes))
	for _, result := range r.Probes {
		details[result.Probe] = result.Detail
	}
	return details
}

// StageRunner runs a migration stage and returns its stdout. migrate.Runner
// satisfies it; tests substitute a stub.
type StageRunner interface {
	Output(ctx context.Context, args ...string) ([]byte, error)
}

type Client struct {
	Runner StageRunner
}

// Collect probes one printer. A transport failure is reported inside the
// returned document, so a non-nil error here means the tooling itself failed.
func (c Client) Collect(ctx context.Context, printer inventory.Printer) (Report, error) {
	args := append([]string{"probe"}, migrate.ConnectionArgs(printer)...)
	raw, err := c.Runner.Output(ctx, args...)
	if err != nil {
		return Report{}, fmt.Errorf("probe %s: %w", printer.Name, err)
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return Report{}, fmt.Errorf("parse probe report for %s: %w", printer.Name, err)
	}
	if report.Schema != 1 {
		return Report{}, fmt.Errorf(
			"probe report for %s uses unsupported schema %d; update printerlab",
			printer.Name, report.Schema)
	}
	return report, nil
}
