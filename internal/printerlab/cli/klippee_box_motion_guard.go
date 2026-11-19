package cli

import (
	"bytes"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newKlippeeHardenBoxMotionTemplateCmd(a *app) *cobra.Command {
	return newKlippeeBoxMotionTemplateCmd(a, false)
}

func newKlippeeRestoreBoxMotionTemplateCmd(a *app) *cobra.Command {
	return newKlippeeBoxMotionTemplateCmd(a, true)
}

func newKlippeeBoxMotionTemplateCmd(a *app, restoreVendor bool) *cobra.Command {
	use := "harden-box-motion-template PRINTER"
	short := "Enable Box motion sensor polling in the stock template"
	long := "Back up and update /home/mks/cfg_with_N for the detected Box count.\n" +
		"Requires an idle printer and Klippee agent.\n" +
		"To activate the settings, deploy box.cfg through Klippee and restart Klipper."
	approval := "Box motion template hardening"
	stage := "harden-box-motion-template"
	action := migrate.BoxMotionGuardAction
	if restoreVendor {
		use = "restore-box-motion-template PRINTER"
		short = "Restore QIDI's Box IRQ and debounce settings in the stock template"
		long = "Back up the stock template and restore use_irq=True and debounce_us=50\n" +
			"in /home/mks/cfg_with_N for the detected Box count.\n" +
			"Requires an idle printer and Klippee agent."
		approval = "Box vendor motion template restore"
		stage = "restore-box-motion-template"
		action = migrate.BoxMotionVendorAction
	}
	command := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval(approval); err != nil {
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
				return fmt.Errorf(
					"%s: Box motion guard requires inventory printer account %q; got %q",
					printer.Name, migrate.BoxClockGuardAccount, account,
				)
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: cannot verify Box motion guard preconditions: %w", printer.Name, err)
			}
			if err := client.EnsureIdle(cmd.Context()); err != nil {
				return fmt.Errorf("%s: Box motion guard blocked: %w", printer.Name, err)
			}
			status, err := requestKlippeeOperationStatus(cmd.Context(), client, "")
			if err != nil {
				return fmt.Errorf("%s: Box motion guard requires current Klippee operation status: %w", printer.Name, err)
			}
			if err := requireKlippeeOperationQuiescent(status); err != nil {
				return fmt.Errorf("%s: Box motion guard blocked because agent quiescence was not proven: %w", printer.Name, err)
			}
			guardStatus, err := client.BoxGuardStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: cannot detect active QIDI Box count: %w", printer.Name, err)
			}
			if guardStatus.BoxCount < 1 || guardStatus.BoxCount > 4 {
				return fmt.Errorf("%s: detected unsupported QIDI Box count %d", printer.Name, guardStatus.BoxCount)
			}

			stageArgs := append(
				[]string{stage},
				migrate.ConnectionArgs(printer)...,
			)
			stageArgs = append(
				stageArgs,
				"--box-count", fmt.Sprint(guardStatus.BoxCount),
				"--confirm", action,
			)
			raw, runErr := a.migrationRunner().Output(cmd.Context(), stageArgs...)
			if len(bytes.TrimSpace(raw)) == 0 {
				if runErr != nil {
					return fmt.Errorf("%s: Box motion firmware operation returned no JSON result: %w", printer.Name, runErr)
				}
				return fmt.Errorf("%s: Box motion firmware operation returned no JSON result", printer.Name)
			}
			var result migrate.BoxMotionGuardResult
			var parseErr error
			if restoreVendor {
				result, parseErr = migrate.ParseBoxMotionVendorRestore(raw)
			} else {
				result, parseErr = migrate.ParseBoxMotionGuard(raw)
			}
			if parseErr != nil {
				if runErr != nil {
					return fmt.Errorf("%s: %w (result could not be validated: %v)", printer.Name, runErr, parseErr)
				}
				return fmt.Errorf("%s: %w", printer.Name, parseErr)
			}
			if result.BoxCount != guardStatus.BoxCount {
				return fmt.Errorf(
					"%s: Box motion firmware result targeted count %d; live count was %d",
					printer.Name, result.BoxCount, guardStatus.BoxCount,
				)
			}
			if err := writeKlippeeBoxMotionGuardResult(
				a, printer.Name, result, restoreVendor,
			); err != nil {
				return err
			}
			if runErr != nil {
				return fmt.Errorf("%s: Box motion firmware operation failed: %w", printer.Name, runErr)
			}
			if !result.Succeeded() {
				return fmt.Errorf("%s: Box motion firmware operation failed: %s", printer.Name, result.Error)
			}
			return nil
		},
	}
	return command
}

func writeKlippeeBoxMotionGuardResult(
	a *app,
	printer string,
	result migrate.BoxMotionGuardResult,
	restoreVendor bool,
) error {
	status := "failed"
	if restoreVendor && result.Succeeded() && result.DidChange() {
		status = "vendor IRQ restored; config deployment required"
	} else if restoreVendor && result.Succeeded() {
		status = "vendor IRQ already restored; config deployment required"
	} else if result.Succeeded() && result.DidChange() {
		status = "hardened; config deployment required"
	} else if result.Succeeded() {
		status = "already hardened; config deployment required"
	}
	backup := ""
	if result.Backup != nil {
		backup = result.Backup.Path
	}
	table := output.Table{Headers: []string{
		"PRINTER", "STATUS", "BOXES", "TRANSACTION", "TARGET", "BACKUP", "KLIPPER ACTION", "ACTIVE CONFIG CHANGE",
	}}
	table.AddRow(
		printer, status, result.BoxCount, result.TransactionID, result.Target.Path,
		backup, result.KlipperServiceAction, fmt.Sprint(result.ActiveConfigChanged != nil && *result.ActiveConfigChanged),
	)
	return output.Write(a.out, a.format, result, table)
}
