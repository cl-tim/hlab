package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

type cancelPrintReport struct {
	Printer string `json:"printer" yaml:"printer"`
	Before  string `json:"before" yaml:"before"`
	After   string `json:"after" yaml:"after"`
	Result  string `json:"result" yaml:"result"`
}

func newCancelPrintCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel-print PRINTER",
		Short: "Cancel an active print through Moonraker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("active print cancellation"); err != nil {
				return err
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			before, err := client.PrintState(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: cannot verify print state: %w", printer.Name, err)
			}
			if before != "printing" && before != "paused" {
				return fmt.Errorf("%s: no active print to cancel (state %q)", printer.Name, before)
			}
			if err := client.RunGcode(cmd.Context(), "CANCEL_PRINT"); err != nil {
				return fmt.Errorf("%s: cancel print: %w", printer.Name, err)
			}
			after, err := client.PrintState(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: cannot verify cancellation: %w", printer.Name, err)
			}
			if after == "printing" || after == "paused" {
				return fmt.Errorf("%s: print remains %q after CANCEL_PRINT", printer.Name, after)
			}
			report := cancelPrintReport{
				Printer: printer.Name, Before: before, After: after, Result: "cancelled",
			}
			table := output.Table{Headers: []string{"PRINTER", "BEFORE", "AFTER", "RESULT"}}
			table.AddRow(report.Printer, report.Before, report.After, report.Result)
			return output.Write(a.out, a.format, report, table)
		},
	}
}
