package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func newFirmwareUpdateCmd(a *app) *cobra.Command {
	var check bool
	var wait time.Duration
	var version, configPolicy, firmwareDir string
	var hostOnly bool
	command := &cobra.Command{
		Use: "update PRINTER", Short: "Update stock QIDI firmware or mainline Klipper",
		Long: "For stock QIDI printers, select a reviewed --version from firmware releases.\n" +
			"Verify and retain a desktop recovery backup, install the vendor Linux package\n" +
			"and touchscreen image, preserve configuration unless --config-policy vendor\n" +
			"is selected, then verify startup. --host-only explicitly skips the screen.\n\n" +
			"For mainline printers, update Klipper to the latest official master revision.\n" +
			"Preserve configuration and extensions, retain a rollback backup, and verify the\n" +
			"service, controllers, Beacon and installed host tuning. Roll back a failed startup.\n" +
			"Controller flashing and Python dependency migrations remain separate operations.\n" +
			"With --check, fetch upstream and verify the current host without changing its\n" +
			"checkout or restarting it. SSH and a working loopback Moonraker API are required.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !check {
				if err := a.requireApproval("printer firmware update"); err != nil {
					return err
				}
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if printer.Firmware == inventory.FirmwareStock {
				return a.updateQIDI(cmd, printer, version, configPolicy, firmwareDir, hostOnly, check, wait)
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if printer.Firmware != inventory.FirmwareMainline {
				return fmt.Errorf("%s: firmware update requires a declared mainline printer", printer.Name)
			}
			if version != "" || hostOnly || configPolicy != "preserve" || firmwareDir != "" {
				return fmt.Errorf("--version, --host-only, --firmware-dir and --config-policy apply only to stock QIDI firmware")
			}
			if wait < 5*time.Second || wait > 10*time.Minute || wait%time.Second != 0 {
				return fmt.Errorf("--wait-timeout must be a whole number of seconds from 5s to 10m")
			}
			stageArgs := append([]string{"update-klipper"}, migrate.ConnectionArgs(printer)...)
			stageArgs = append(stageArgs, "--moonraker-port", fmt.Sprint(printer.MoonrakerPort), "--wait-seconds", fmt.Sprint(int(wait/time.Second)))
			if printer.APIKeyEnv != "" {
				stageArgs = append(stageArgs, "--api-key-env", printer.APIKeyEnv)
			}
			if check {
				stageArgs = append(stageArgs, "--check")
			} else {
				stageArgs = append(stageArgs, "--confirm", migrate.KlipperUpdateAction)
				fmt.Fprintf(a.errOut, "%s: checking idle state, backing up and updating Klipper; startup verification may take %s\n", printer.Name, wait)
			}
			raw, runErr := a.migrationRunner().Output(cmd.Context(), stageArgs...)
			result, parseErr := migrate.ParseKlipperUpdate(raw)
			if parseErr != nil {
				if runErr != nil {
					return fmt.Errorf("%s: %w", printer.Name, runErr)
				}
				return fmt.Errorf("%s: %w", printer.Name, parseErr)
			}
			account := printer.PrinterUser
			if account == "" {
				account = printer.User
			}
			if result.Account != account || result.CheckOnly != check {
				return fmt.Errorf("%s: Klipper update receipt targeted a different account or mode", printer.Name)
			}
			table := output.Table{Headers: []string{"PRINTER", "STATUS", "VERSION", "BACKUP", "DETAIL"}}
			table.AddRow(printer.Name, result.Status, result.Version, result.Backup, result.Error)
			if err := output.Write(a.out, a.format, result, table); err != nil {
				return err
			}
			if runErr != nil || !result.OK {
				detail := strings.TrimSpace(result.Error + " " + result.RollbackError)
				if runErr != nil {
					detail = strings.TrimSpace(detail + " " + runErr.Error())
				}
				return fmt.Errorf("%s: Klipper update %s: %s", printer.Name, result.Status, detail)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&check, "check", false, "check prerequisites without installing or restarting (mainline also fetches upstream)")
	command.Flags().StringVar(&version, "version", "", "stock QIDI release from firmware releases (required for stock printers)")
	command.Flags().StringVar(&firmwareDir, "firmware-dir", "", "use verified vendor payload files from this PC instead of downloading on the printer")
	command.Flags().BoolVar(&hostOnly, "host-only", false, "update stock QIDI Linux firmware without flashing the touchscreen")
	command.Flags().StringVar(&configPolicy, "config-policy", "preserve", "stock QIDI configuration policy: preserve existing files, or install vendor defaults")
	command.Flags().DurationVar(&wait, "wait-timeout", 3*time.Minute, "maximum startup verification time (including host tuning)")
	return command
}
