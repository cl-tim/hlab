package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newKlippeeAgentEventsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "agent-events PRINTER",
		Short: "Show recent Klippee agent messages",
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
			events, err := client.KlippeeEvents(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: read Klippee events: %w", printer.Name, err)
			}
			table := output.Table{Headers: []string{"TIME", "TYPE", "MESSAGE"}}
			for _, event := range events {
				table.AddRow(fmt.Sprintf("%.3f", event.Time), event.Type, event.Message)
			}
			return output.Write(a.out, a.format, events, table)
		},
	}
}
