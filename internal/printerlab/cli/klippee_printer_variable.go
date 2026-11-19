package cli

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newKlippeePrinterVariableSetCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:   "printer-variable-set PRINTER NAME VALUE",
		Short: "Set a Klippee rendering variable on the printer",
		Long: "Set and validate a value in the printer's klippee_variables.cfg through SSH.\n" +
			"Requires an idle printer and Klippee agent.",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("physical Klippee variable update"); err != nil {
				return err
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if account := plrPrinterAccount(printer); account != migrate.BoxClockGuardAccount {
				return fmt.Errorf("%s: physical Klippee variable requires printer account mks", printer.Name)
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: cannot verify variable-set preconditions: %w", printer.Name, err)
			}
			if err := client.EnsureIdle(cmd.Context()); err != nil {
				return fmt.Errorf("%s: physical Klippee variable-set blocked: %w", printer.Name, err)
			}
			status, err := requestKlippeeOperationStatus(cmd.Context(), client, "")
			if err != nil {
				return fmt.Errorf("%s: physical variable-set requires current Klippee operation status: %w", printer.Name, err)
			}
			if err := requireKlippeeOperationQuiescent(status); err != nil {
				return fmt.Errorf("%s: physical Klippee variable-set blocked: %w", printer.Name, err)
			}
			stageArgs := append(
				[]string{"set-klippee-variable"},
				migrate.ConnectionArgs(printer)...,
			)
			stageArgs = append(
				stageArgs,
				"--name", args[1],
				"--value", args[2],
				"--confirm", migrate.KlippeeVariableSetAction,
			)
			raw, runErr := a.migrationRunner().Output(cmd.Context(), stageArgs...)
			if len(bytes.TrimSpace(raw)) == 0 {
				if runErr != nil {
					return fmt.Errorf("%s: physical Klippee variable-set returned no JSON: %w", printer.Name, runErr)
				}
				return fmt.Errorf("%s: physical Klippee variable-set returned no JSON", printer.Name)
			}
			result, parseErr := migrate.ParseKlippeeVariableSet(raw)
			if parseErr != nil {
				return fmt.Errorf("%s: invalid physical Klippee variable-set proof: %w", printer.Name, parseErr)
			}
			if result.Name != args[1] || result.ValueSHA256 != migrate.KlippeeVariableValueSHA256(args[2]) {
				return fmt.Errorf("%s: physical Klippee variable-set proof did not match the request", printer.Name)
			}
			table := output.Table{Headers: []string{
				"PRINTER", "STATUS", "NAME", "VALUE SHA256", "ACCOUNT", "KLIPPER ACTION", "CONFIG DEPLOYED",
			}}
			statusLabel := "failed"
			if result.Succeeded() {
				statusLabel = "set and verified"
			}
			table.AddRow(
				printer.Name, statusLabel, result.Name, result.ValueSHA256,
				result.Account, result.KlipperAction,
				fmt.Sprint(result.ConfigDeployed != nil && *result.ConfigDeployed),
			)
			if err := output.Write(a.out, a.format, result, table); err != nil {
				return err
			}
			if runErr != nil {
				return fmt.Errorf("%s: physical Klippee variable-set failed: %w", printer.Name, runErr)
			}
			if !result.Succeeded() {
				return fmt.Errorf("%s: physical Klippee variable-set failed: %s", printer.Name, result.Error)
			}
			return nil
		},
	}
	return command
}
