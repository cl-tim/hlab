package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
)

func newFirmwareUpdateCmd(a *app) *cobra.Command {
	var check bool
	var wait time.Duration
	command := &cobra.Command{
		Use: "update PRINTER", Short: "Update mainline Klipper and verify startup",
		Long: "Update one idle printer's Klipper host to the latest official master revision.\n" +
			"Preserve configuration and extensions, retain a rollback backup, and verify the\n" +
			"service, controllers, Beacon and installed host tuning. Roll back a failed startup.\n" +
			"Controller flashing and Python dependency migrations remain separate operations.\n" +
			"With --check, fetch upstream and verify the current host without changing its\n" +
			"checkout or restarting it. SSH and a working loopback Moonraker API are required.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !check {
				if err := a.requireApproval("Klipper host update"); err != nil {
					return err
				}
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if printer.Firmware != inventory.FirmwareMainline {
				return fmt.Errorf("%s: firmware update requires a declared mainline printer", printer.Name)
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
	command.Flags().BoolVar(&check, "check", false, "check upstream revision and current startup without updating or restarting")
	command.Flags().DurationVar(&wait, "wait-timeout", 3*time.Minute, "maximum startup verification time (including host tuning)")
	return command
}
