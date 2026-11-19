package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

type idleTimeoutReport struct {
	Printer string  `json:"printer" yaml:"printer"`
	Seconds float64 `json:"seconds" yaml:"seconds"`
}

func newIdleTimeoutCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "idle-timeout PRINTER",
		Short: "Read the resolved Klipper idle timeout",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			seconds, err := client.IdleTimeout(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: read idle timeout: %w", printer.Name, err)
			}
			report := idleTimeoutReport{Printer: printer.Name, Seconds: seconds}
			table := output.Table{Headers: []string{"PRINTER", "SECONDS"}}
			table.AddRow(report.Printer, fmt.Sprintf("%.0f", report.Seconds))
			return output.Write(a.out, a.format, report, table)
		},
	}
}
