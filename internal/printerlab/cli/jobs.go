package cli

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

type jobHistoryReport struct {
	Printer string          `json:"printer" yaml:"printer"`
	Jobs    []moonraker.Job `json:"jobs" yaml:"jobs"`
	Error   string          `json:"error,omitempty" yaml:"error,omitempty"`
}

func newJobsCmd(a *app) *cobra.Command {
	command := &cobra.Command{Use: "jobs", Short: "Inspect printer job history"}
	var sinceFlag string
	var limit int
	history := &cobra.Command{
		Use:   "history [selector]",
		Short: "Show recent print jobs and recorded Spoolman spool IDs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 1 {
				return fmt.Errorf("--limit must be positive")
			}
			var since time.Time
			if cmd.Flags().Changed("since") {
				duration, err := historyDuration(sinceFlag)
				if err != nil || duration <= 0 {
					return fmt.Errorf("--since must be a positive duration such as 24h or 7d")
				}
				since = time.Now().Add(-duration)
			}
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			reports := readFleet(ctx, a, printers, func(p inventory.Printer, client *moonraker.Client, err error) jobHistoryReport {
				report := jobHistoryReport{Printer: p.Name}
				if err == nil {
					report.Jobs, err = client.JobHistory(ctx, since, limit)
				}
				if err != nil {
					report.Error = err.Error()
				}
				return report
			})
			return writeJobHistory(a, reports)
		},
	}
	history.Flags().StringVar(&sinceFlag, "since", "", "show jobs started within this duration, e.g. 24h or 7d")
	history.Flags().IntVar(&limit, "limit", 50, "maximum number of jobs per printer")
	command.AddCommand(history)
	return command
}

func historyDuration(value string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(value, "d"); ok {
		const day = 24 * time.Hour
		count, err := strconv.ParseUint(days, 10, 64)
		if err != nil || count > uint64(math.MaxInt64/day) {
			return 0, fmt.Errorf("invalid number of days %q", days)
		}
		return time.Duration(count) * day, nil
	}
	return time.ParseDuration(value)
}

func writeJobHistory(a *app, reports []jobHistoryReport) error {
	table := output.Table{Headers: []string{"PRINTER", "STARTED (UTC)", "JOB", "STATUS", "PRINT TIME (S)", "FILAMENT (MM)", "SPOOL IDS"}}
	failed := false
	for _, report := range reports {
		if report.Error != "" {
			failed = true
			table.AddRow(report.Printer, "-", report.Error, "error", "-", "-", "-")
		} else if len(report.Jobs) == 0 {
			table.AddRow(report.Printer, "-", "No matching jobs", "-", "-", "-", "-")
		}
		for _, job := range report.Jobs {
			ids := make([]string, 0, len(job.SpoolIDs))
			for _, id := range job.SpoolIDs {
				ids = append(ids, strconv.Itoa(id))
			}
			table.AddRow(report.Printer, job.StartedAt.Format(time.RFC3339), job.Filename, job.Status,
				fmt.Sprintf("%.1f", job.PrintDurationSeconds), fmt.Sprintf("%.1f", job.FilamentUsedMM), strings.Join(ids, ", "))
		}
	}
	if err := output.Write(a.out, a.format, reports, table); err != nil {
		return err
	}
	if failed {
		return &ExitError{Code: 1}
	}
	return nil
}
