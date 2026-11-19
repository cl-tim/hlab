package cli

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newKlippeeUpgradeAgentCmd(a *app) *cobra.Command {
	var recoverUnresponsive bool
	command := &cobra.Command{
		Use:   "upgrade-agent PRINTER",
		Short: "Upgrade the printer's Klippee agent",
		Long: "Upgrade the printer's Klippee CLI and agent from the official source through SSH.\n" +
			"Restart the agent service and verify its version and process. Requires an idle agent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("Klippee agent upgrade"); err != nil {
				return err
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			expectedAccount := plrPrinterAccount(printer)
			if expectedAccount != migrate.KlippeeAgentAccount {
				return fmt.Errorf(
					"%s: upgrade-agent requires inventory printer account %q; got %q",
					printer.Name, migrate.KlippeeAgentAccount, expectedAccount,
				)
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: cannot prove Klippee agent quiescent: %w", printer.Name, err)
			}
			status, err := requestKlippeeOperationStatus(cmd.Context(), client, "")
			if err != nil {
				if !recoverUnresponsive {
					return fmt.Errorf(
						"%s: upgrade-agent requires current klippee_operation_status support: %w",
						printer.Name, err,
					)
				}
				if idleErr := client.EnsureIdle(cmd.Context()); idleErr != nil {
					return fmt.Errorf(
						"%s: unresponsive-agent upgrade requires an idle printer: %w",
						printer.Name, idleErr,
					)
				}
			} else if err := requireKlippeeOperationQuiescent(status); err != nil {
				return fmt.Errorf(
					"%s: upgrade-agent blocked because agent quiescence was not proven: %w",
					printer.Name, err,
				)
			}

			stageArgs := append(
				[]string{"upgrade-klippee-agent"}, migrate.ConnectionArgs(printer)...,
			)
			stageArgs = append(
				stageArgs, "--confirm", migrate.KlippeeAgentUpgradeAction,
			)
			raw, runErr := a.migrationRunner().Output(cmd.Context(), stageArgs...)
			if len(bytes.TrimSpace(raw)) == 0 {
				if runErr != nil {
					return fmt.Errorf(
						"%s: Klippee agent upgrade returned no JSON result: %w",
						printer.Name, runErr,
					)
				}
				return fmt.Errorf("%s: Klippee agent upgrade returned no JSON result", printer.Name)
			}
			result, parseErr := migrate.ParseKlippeeAgentUpgrade(raw)
			if parseErr != nil {
				preview := migrate.SafeDiagnosticSummary(raw)
				if preview != "" {
					parseErr = fmt.Errorf("%w (firmware stdout summary: %s)", parseErr, preview)
				}
				if runErr != nil {
					return fmt.Errorf(
						"%s: %w (result could not be validated: %v)",
						printer.Name, runErr, parseErr,
					)
				}
				return fmt.Errorf("%s: %w", printer.Name, parseErr)
			}
			if result.Account != expectedAccount {
				return fmt.Errorf(
					"%s: Klippee agent upgrade targeted account %q; expected inventory account %q",
					printer.Name, result.Account, expectedAccount,
				)
			}
			if err := writeKlippeeAgentUpgradeResult(a, printer.Name, result); err != nil {
				return err
			}
			diagnostic := result.DiagnosticSummary()
			if runErr != nil {
				if diagnostic != "" {
					return fmt.Errorf("%s: %s: %w", printer.Name, diagnostic, runErr)
				}
				return fmt.Errorf("%s: %w", printer.Name, runErr)
			}
			if !result.Succeeded() {
				if diagnostic != "" {
					return fmt.Errorf("%s: Klippee agent upgrade failed: %s", printer.Name, diagnostic)
				}
				return fmt.Errorf("%s: Klippee agent upgrade reported failure", printer.Name)
			}
			return nil
		},
	}
	command.Flags().BoolVar(
		&recoverUnresponsive,
		"recover-unresponsive",
		false,
		"upgrade an unresponsive agent when Moonraker reports the printer idle",
	)
	return command
}

func writeKlippeeAgentUpgradeResult(
	a *app,
	printer string,
	result migrate.KlippeeAgentUpgradeResult,
) error {
	status := "failed"
	if result.Succeeded() && result.DidChange() {
		status = "upgraded; agent restarted"
	} else if result.Succeeded() {
		status = "current; agent restarted"
	}
	table := output.Table{Headers: []string{
		"PRINTER", "STATUS", "VERSION", "PID", "SERVICE", "ACCOUNT",
		"KLIPPER ACTIONS", "CONFIG DEPLOYS", "DETAIL",
	}}
	beforePID, afterPID := 0, 0
	if result.BeforePID != nil {
		beforePID = *result.BeforePID
	}
	if result.AfterPID != nil {
		afterPID = *result.AfterPID
	}
	table.AddRow(
		printer, status,
		result.Before.Version+" -> "+result.After.Version,
		fmt.Sprintf("%d -> %d", beforePID, afterPID),
		result.Service+" ("+result.Scope+", "+result.ObservedState+")",
		result.Account, "none", "none", result.DiagnosticSummary(),
	)
	return output.Write(a.out, a.format, result, table)
}
