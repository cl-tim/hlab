package cli

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

type spoolmanReport struct {
	Printer  string                    `json:"printer" yaml:"printer"`
	State    string                    `json:"state" yaml:"state"`
	Spoolman *moonraker.SpoolmanStatus `json:"spoolman,omitempty" yaml:"spoolman,omitempty"`
	Error    string                    `json:"error,omitempty" yaml:"error,omitempty"`
}

func newSpoolmanCmd(a *app) *cobra.Command {
	command := &cobra.Command{Use: "spoolman", Short: "Inspect Moonraker's Spoolman integration"}
	command.AddCommand(&cobra.Command{
		Use:   "status [selector]",
		Short: "Show the active spool, connection, and pending usage reports",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			reports := readFleet(ctx, a, printers, func(p inventory.Printer, client *moonraker.Client, err error) spoolmanReport {
				report := spoolmanReport{Printer: p.Name}
				var status moonraker.SpoolmanStatus
				if err == nil {
					status, err = client.SpoolmanStatus(ctx)
				}
				var httpErr *moonraker.HTTPError
				switch {
				case errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound:
					report.State = "unavailable"
				case err != nil:
					report.State, report.Error = "error", err.Error()
				default:
					report.Spoolman = &status
					report.State = "disconnected"
					if status.Connected {
						report.State = "connected"
					}
				}
				return report
			})
			table := output.Table{Headers: []string{"PRINTER", "SPOOLMAN", "ACTIVE SPOOL", "PENDING REPORTS", "ISSUE"}}
			failed := false
			for _, report := range reports {
				spool, pending := "-", "-"
				if state := report.Spoolman; state != nil {
					spool = "none"
					if state.ActiveSpoolID != nil {
						spool = strconv.Itoa(*state.ActiveSpoolID)
					}
					pending = strconv.Itoa(len(state.PendingReports))
				}
				failed = failed || report.Error != ""
				table.AddRow(report.Printer, report.State, spool, pending, report.Error)
			}
			if err := output.Write(a.out, a.format, reports, table); err != nil {
				return err
			}
			if failed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	})
	return command
}
