package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

type klippeeBoxRuntimeReport struct {
	Printer string                     `json:"printer" yaml:"printer"`
	State   moonraker.BoxRuntimeStatus `json:"state" yaml:"state"`
}

func newKlippeeBoxRuntimeStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "box-state PRINTER",
		Short: "Read the active QIDI Box slot and sensor state",
		Long:  "Show QIDI Box runtime state and saved variables through Moonraker.",
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
			state, err := client.BoxRuntimeStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: read QIDI Box state: %w", printer.Name, err)
			}
			report := klippeeBoxRuntimeReport{Printer: printer.Name, State: state}
			table := output.Table{Headers: []string{
				"PRINTER", "LOAD MACRO", "UNLOAD READY", "UNLOAD SLOT", "SLOT SOURCE", "ISSUE", "ENABLED", "LIVE SLOT", "LAST LOAD", "E ENDSTOP", "FILAMENT WIDTH", "PRINT STATE",
			}}
			table.AddRow(
				report.Printer, state.LoadMacroLoaded, state.UnloadReady, state.UnloadSlot, state.UnloadSlotSource,
				state.UnloadIssue, state.Enabled, state.SlotSync, state.LastLoadSlot,
				state.EEndstop, fmt.Sprintf("%.3f", state.FilamentWidth), state.PrintState,
			)
			return output.Write(a.out, a.format, report, table)
		},
	}
}
