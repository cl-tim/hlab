package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newKlippeePrinterDiffCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "printer-diff PRINTER",
		Short: "Show the printer's configuration diff",
		Long:  "Run klippee check --diff on the printer through SSH.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			raw, err := a.migrationRunner().KlippeeDiffSSHOutput(
				cmd.Context(), printer, a.timeout(),
			)
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			_, err = fmt.Fprintln(a.out, strings.TrimSpace(string(raw)))
			return err
		},
	}
}
