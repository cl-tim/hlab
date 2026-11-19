package cli

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newKlippeeRefreshAgentCmd(a *app) *cobra.Command {
	var recoverUnresponsive bool
	command := &cobra.Command{
		Use:   "refresh-agent PRINTER",
		Short: "Restart and verify the printer's Klippee agent",
		Long: "Restart the printer's Klippee agent service through SSH and verify it is active.\n" +
			"Requires an idle agent. Use --recover-unresponsive when operation status is\n" +
			"unavailable and Moonraker reports the printer idle.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("Klippee agent refresh"); err != nil {
				return err
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: cannot prove Klippee agent quiescent: %w", printer.Name, err)
			}
			status, err := requestKlippeeOperationStatus(cmd.Context(), client, "")
			if err != nil {
				if !recoverUnresponsive {
					return fmt.Errorf(
						"%s: refresh-agent requires current klippee_operation_status support: %w",
						printer.Name,
						err,
					)
				}
				if idleErr := client.EnsureIdle(cmd.Context()); idleErr != nil {
					return fmt.Errorf(
						"%s: unresponsive-agent recovery requires an idle printer: %w",
						printer.Name, idleErr,
					)
				}
			} else if err := requireKlippeeOperationQuiescent(status); err != nil {
				return fmt.Errorf(
					"%s: refresh-agent blocked because agent quiescence was not proven: %w",
					printer.Name,
					err,
				)
			}
			stageArgs := append(
				[]string{"refresh-klippee-agent"}, migrate.ConnectionArgs(printer)...,
			)
			stageArgs = append(
				stageArgs, "--confirm", migrate.KlippeeAgentRefreshAction,
			)
			raw, runErr := a.migrationRunner().Output(cmd.Context(), stageArgs...)
			if len(bytes.TrimSpace(raw)) == 0 {
				if runErr != nil {
					return runErr
				}
				return fmt.Errorf("firmware operation returned no Klippee agent refresh result")
			}
			result, parseErr := migrate.ParseKlippeeAgentRefresh(raw)
			if parseErr != nil {
				if runErr != nil {
					return fmt.Errorf("%w (result could not be validated: %v)", runErr, parseErr)
				}
				return parseErr
			}
			expectedAccount := printer.User
			if printer.PrinterUser != "" {
				expectedAccount = printer.PrinterUser
			}
			if result.Account != expectedAccount {
				return fmt.Errorf(
					"Klippee agent refresh targeted account %q; expected %q",
					result.Account, expectedAccount,
				)
			}
			table := output.Table{Headers: []string{
				"PRINTER", "SERVICE", "ACCOUNT", "SCOPE", "STATE", "KLIPPER ACTIONS", "CONFIG DEPLOYS",
			}}
			table.AddRow(
				printer.Name, result.Service, result.Account, result.Scope,
				result.ObservedState, "none", "none",
			)
			if err := output.Write(a.out, a.format, result, table); err != nil {
				return err
			}
			if runErr != nil {
				if result.Error != "" {
					return fmt.Errorf("%s: %w", result.Error, runErr)
				}
				return runErr
			}
			if !result.OK {
				if result.Error != "" {
					return fmt.Errorf("Klippee agent refresh failed: %s", result.Error)
				}
				return fmt.Errorf("Klippee agent refresh reported failure")
			}
			return nil
		},
	}
	command.Flags().BoolVar(
		&recoverUnresponsive,
		"recover-unresponsive",
		false,
		"restart an unresponsive agent when Moonraker reports the printer idle",
	)
	return command
}
