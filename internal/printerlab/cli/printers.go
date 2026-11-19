package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
)

func newPrintersCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "printers [selector]",
		Short: "List configured printers",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			table := output.Table{Headers: []string{"NAME", "ADDRESS", "MODEL", "FIRMWARE", "TAGS"}}
			for _, printer := range printers {
				table.AddRow(
					printer.Name, printer.Address, printer.Model, printer.Firmware,
					strings.Join(printer.Tags, ","),
				)
			}
			return output.Write(a.out, a.format, printers, table)
		},
	}
}
