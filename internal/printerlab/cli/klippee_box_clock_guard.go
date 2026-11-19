package cli

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newKlippeeInstallBoxClockGuardCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:   "install-box-output-clock-guard PRINTER",
		Short: "Install the QIDI Box output-clock guard",
		Long: "Install the Box output-clock guard module through SSH. Requires an idle agent.\n" +
			"To activate it, deploy [box_output_clock_guard] and run service-restart.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("Box output clock guard installation"); err != nil {
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
			if expectedAccount != migrate.BoxClockGuardAccount {
				return fmt.Errorf(
					"%s: clock-guard install requires inventory printer account %q; got %q",
					printer.Name, migrate.BoxClockGuardAccount, expectedAccount,
				)
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: cannot prove Klippee agent quiescent: %w", printer.Name, err)
			}
			status, err := requestKlippeeOperationStatus(cmd.Context(), client, "")
			if err != nil {
				return fmt.Errorf(
					"%s: clock-guard install requires current klippee_operation_status support: %w",
					printer.Name, err,
				)
			}
			if err := requireKlippeeOperationQuiescent(status); err != nil {
				return fmt.Errorf(
					"%s: clock-guard install blocked because agent quiescence was not proven: %w",
					printer.Name, err,
				)
			}

			stageArgs := append(
				[]string{"install-box-output-clock-guard"},
				migrate.ConnectionArgs(printer)...,
			)
			stageArgs = append(stageArgs, "--confirm", migrate.BoxClockGuardInstallAction)
			raw, runErr := a.migrationRunner().Output(cmd.Context(), stageArgs...)
			if len(bytes.TrimSpace(raw)) == 0 {
				if runErr != nil {
					return fmt.Errorf("%s: clock-guard firmware operation returned no JSON result: %w", printer.Name, runErr)
				}
				return fmt.Errorf("%s: clock-guard firmware operation returned no JSON result", printer.Name)
			}
			result, parseErr := migrate.ParseBoxClockGuardInstall(raw)
			if parseErr != nil {
				preview := migrate.SafeDiagnosticSummary(raw)
				if preview != "" {
					parseErr = fmt.Errorf("%w (firmware stdout summary: %s)", parseErr, preview)
				}
				if runErr != nil {
					return fmt.Errorf("%s: %w (result could not be validated: %v)", printer.Name, runErr, parseErr)
				}
				return fmt.Errorf("%s: %w", printer.Name, parseErr)
			}
			if result.Account != expectedAccount {
				return fmt.Errorf(
					"%s: clock-guard firmware result targeted account %q; expected inventory account %q",
					printer.Name, result.Account, expectedAccount,
				)
			}
			if err := writeKlippeeBoxClockGuardResult(a, printer.Name, result); err != nil {
				return err
			}
			diagnostic := result.DiagnosticSummary()
			if runErr != nil {
				if diagnostic != "" {
					return fmt.Errorf("%s: %s: %w", printer.Name, diagnostic, runErr)
				}
				return fmt.Errorf("%s: %w", printer.Name, runErr)
			}
			if result.OK != nil && *result.OK &&
				result.StagingCleanupVerified != nil && !*result.StagingCleanupVerified {
				return fmt.Errorf(
					"%s: clock guard was installed, but remote staging cleanup was not verified; inspect the reported transaction before retrying",
					printer.Name,
				)
			}
			if !result.Succeeded() {
				if diagnostic != "" {
					return fmt.Errorf("%s: clock-guard install failed: %s", printer.Name, diagnostic)
				}
				return fmt.Errorf("%s: clock-guard install reported failure", printer.Name)
			}
			return nil
		},
	}
	return command
}

func writeKlippeeBoxClockGuardResult(
	a *app,
	printer string,
	result migrate.BoxClockGuardInstallResult,
) error {
	status := "failed"
	switch {
	case result.OK != nil && *result.OK &&
		result.StagingCleanupVerified != nil && !*result.StagingCleanupVerified:
		status = "installed; staging cleanup unverified"
	case result.Succeeded() && result.DidChange():
		status = "installed; activation required"
	case result.Succeeded():
		status = "already installed; activation required"
	}
	backup := ""
	if result.Backup != nil {
		backup = result.Backup.Path
	}
	table := output.Table{Headers: []string{
		"PRINTER", "STATUS", "ACCOUNT", "TRANSACTION", "TARGET", "BACKUP",
		"ACTIVATION", "KLIPPER ACTION", "CONFIG CHANGE", "DETAIL",
	}}
	table.AddRow(
		printer, status, result.Account, result.TransactionID, result.Target.Path,
		backup, result.RequiredConfigSection, result.KlipperServiceAction,
		fmt.Sprint(result.ConfigChanged != nil && *result.ConfigChanged),
		strings.TrimSpace(result.DiagnosticSummary()),
	)
	return output.Write(a.out, a.format, result, table)
}
