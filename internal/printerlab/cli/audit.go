package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

type auditResult struct {
	Name     string             `json:"name" yaml:"name"`
	Firmware string             `json:"firmware" yaml:"firmware"`
	Result   string             `json:"result" yaml:"result"`
	Issues   []string           `json:"issues,omitempty" yaml:"issues,omitempty"`
	Snapshot moonraker.Snapshot `json:"snapshot" yaml:"snapshot"`
}

func newAuditCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "audit [selector]",
		Short: "Audit Moonraker reachability and printer readiness",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			snapshots := collectSnapshots(cmd.Context(), printers, a.timeout(), a.parallelism())
			results := make([]auditResult, 0, len(snapshots))
			table := output.Table{Headers: []string{"NAME", "FIRMWARE", "RESULT", "ISSUES"}}
			failed := false
			for _, snapshot := range snapshots {
				result := auditSnapshot(snapshot)
				if result.Result == "fail" {
					failed = true
				}
				results = append(results, result)
				table.AddRow(result.Name, result.Firmware, result.Result, strings.Join(result.Issues, "; "))
			}
			if err := output.Write(a.out, a.format, results, table); err != nil {
				return err
			}
			if failed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
}

func auditSnapshot(snapshot moonraker.Snapshot) auditResult {
	result := auditResult{
		Name: snapshot.Name, Firmware: snapshot.Firmware, Result: "pass", Snapshot: snapshot,
	}
	if !snapshot.Reachable {
		result.Result = "fail"
		result.Issues = append(result.Issues, "Moonraker unreachable")
	}
	if snapshot.Error != "" {
		result.Result = "fail"
		result.Issues = append(result.Issues, snapshot.Error)
	}
	if snapshot.Reachable && snapshot.KlippyState != "ready" {
		result.Result = "fail"
		issue := "Klippy state is " + snapshot.KlippyState
		if snapshot.KlippyStateMessage != "" {
			issue += ": " + snapshot.KlippyStateMessage
		}
		result.Issues = append(result.Issues, issue)
	}
	if snapshot.Reachable && snapshot.PrintState == "" {
		result.Result = "fail"
		result.Issues = append(result.Issues, "print state unavailable")
	}
	if snapshot.Firmware == "auto" {
		if result.Result == "pass" {
			result.Result = "warn"
		}
		result.Issues = append(result.Issues, "firmware mode is not declared")
	}
	return result
}
