package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/spf13/cobra"
)

func newFirmwareFixesCmd(a *app) *cobra.Command {
	var target firmwareUpdateTarget
	var check, polling, trimRunout, usbLog, fastProbing, boxDrying, fastVariableSaves bool
	var rollback string
	var wait time.Duration
	cmd := &cobra.Command{
		Use: "fixes [PRINTER]", Short: "Apply reviewed fixes to stock Plus4 1.6.0, 1.8.2 or 2.1.1",
		Long: "Check or apply output-clock, power-loss recovery and Armbian log-space fixes.\n" +
			"1.6.0 supports host logging, probing and legacy variable saves; Box and PLR fixes are skipped.\n" +
			"The status compatibility shim is added only on 1.8.2; existing 2.1.1 shims are retained.\n" +
			"--box-motion-polling optionally mitigates the Box IRQ watchdog defect in active\n" +
			"configuration and stock templates. The clock guard disables automatic RFID insertion\n" +
			"unless qidi_box_auto_insert=1 is explicitly set; manual loading is retained.\n" +
			"--trim-runout-waits removes the recognized PRINT_START runout delays;\n" +
			"--usb-event-log installs bounded persistent USB evidence without serial numbers.\n" +
			"--fast-probing sets reviewed tilt/mesh speeds to 300 mm/s, including Beacon scans;\n" +
			"--box-drying installs timed Box drying with live filament-limit checks.\n" +
			"--fast-variable-saves skips unchanged stock saves and durably replaces changed values.\n" +
			"Use --target HOST without enrollment or a configuration inventory. Existing SSH\n" +
			"and passwordless sudo are required; no Klippee agent is required.\n" +
			"Check mode changes no configuration or service. Apply requires idle/heaters off,\n" +
			"verifies a desktop recovery backup, restarts Klipper and checks startup.\n" +
			"A failed activation leaves Klipper stopped with a rollback handle. Log retention\n" +
			"cleanup can remove old journal history; rollback cannot restore expired logs.",
		Example: "  hlab printer firmware fixes --target printer.local --check\n" +
			"  hlab printer firmware fixes --target printer.local -y\n" +
			"  hlab printer firmware fixes --target printer.local --box-motion-polling -y\n" +
			"  hlab printer firmware fixes --target printer.local --rollback /var/lib/hlab/qidi-fixes/TRANSACTION -y",
		Args: func(cmd *cobra.Command, args []string) error {
			if directFirmwareUpdate(cmd) {
				if len(args) != 0 {
					return fmt.Errorf("use either a saved PRINTER or --target HOST")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !check {
				if err := a.requireApproval("stock QIDI fixes"); err != nil {
					return err
				}
			}
			if rollback != "" && (check || polling || trimRunout || usbLog || fastProbing || boxDrying || fastVariableSaves) {
				return fmt.Errorf("--rollback cannot be combined with --check or fix options")
			}
			if wait < 5*time.Second || wait > 10*time.Minute || wait%time.Second != 0 {
				return fmt.Errorf("--wait-timeout must be whole seconds from 5s to 10m")
			}
			printer, err := a.firmwareUpdatePrinter(cmd, args, target, "")
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if printer.Firmware != inventory.FirmwareStock || plrPrinterAccount(printer) != "mks" {
				return fmt.Errorf("fixes require a declared stock Plus4 with the mks printer account")
			}
			stage := append([]string{"qidi-fixes"}, migrate.ConnectionArgs(printer)...)
			stage = append(stage, "--model", printer.Model, "--moonraker-port", fmt.Sprint(printer.MoonrakerPort), "--wait-seconds", fmt.Sprint(int(wait/time.Second)))
			if printer.APIKeyEnv != "" {
				stage = append(stage, "--api-key-env", printer.APIKeyEnv)
			}
			if rollback != "" {
				stage = append(stage, "--rollback-manifest", rollback)
			}
			if polling {
				stage = append(stage, "--box-motion-polling")
			}
			if trimRunout {
				stage = append(stage, "--trim-runout-waits")
			}
			if usbLog {
				stage = append(stage, "--usb-event-log")
			}
			if fastProbing {
				stage = append(stage, "--fast-probing")
			}
			if boxDrying {
				stage = append(stage, "--box-drying")
			}
			if fastVariableSaves {
				stage = append(stage, "--fast-variable-saves")
			}
			if check {
				stage = append(stage, "--check")
			} else {
				stage = append(stage, "--confirm", migrate.QIDIFixesAction)
			}
			raw, runErr := a.migrationRunner().Output(cmd.Context(), stage...)
			result, err := migrate.ParseQIDIFixes(raw)
			if err != nil {
				return fmt.Errorf("%s: invalid fixes result: %w (%v)", printer.Name, err, runErr)
			}
			if result.CheckOnly != check || result.Polling != polling || result.TrimRunoutWaits != trimRunout ||
				result.USBEventLog != usbLog || result.FastProbing != fastProbing || result.BoxDrying != boxDrying || result.FastVariableSaves != fastVariableSaves {
				return fmt.Errorf("fixes result does not match requested mode")
			}
			table := output.Table{Headers: []string{"PRINTER", "STATUS", "VERSION", "RECOVERY", "DETAIL"}}
			table.AddRow(printer.Name, result.Status, result.Version, result.Backup, result.Error)
			for _, path := range result.Changes {
				table.AddRow("", "planned change", "", "", path)
			}
			for _, note := range result.Notes {
				table.AddRow("", "note", "", "", note)
			}
			if result.LocalBackup != "" {
				table.AddRow("", "desktop backup", "", result.LocalBackup, "")
			}
			if err := output.Write(a.out, a.format, result, table); err != nil {
				return err
			}
			if runErr != nil || !result.OK {
				return fmt.Errorf("%s: QIDI fixes failed: %s (%v)", printer.Name, strings.TrimSpace(result.Error), runErr)
			}
			return nil
		},
	}
	target.addFlags(cmd)
	cmd.Flags().BoolVar(&check, "check", false, "inspect compatibility and planned changes without editing configuration or restarting")
	cmd.Flags().BoolVar(&polling, "box-motion-polling", false, "also switch active and template Box motion sensors from QIDI IRQ to polling")
	cmd.Flags().BoolVar(&trimRunout, "trim-runout-waits", false, "remove only the recognized PRINT_START runout-flag waits")
	cmd.Flags().BoolVar(&usbLog, "usb-event-log", false, "retain bounded USB events across reboots for Klippee diagnostics")
	cmd.Flags().BoolVar(&fastProbing, "fast-probing", false, "set reviewed tilt/mesh travel and Beacon scan speeds to 300 mm/s")
	cmd.Flags().BoolVar(&boxDrying, "box-drying", false, "install timed Box drying with live filament-limit checks")
	cmd.Flags().BoolVar(&fastVariableSaves, "fast-variable-saves", false, "skip unchanged stock variable saves and durably replace changed values")
	cmd.Flags().StringVar(&rollback, "rollback", "", "exact recovery transaction path returned by fixes; refuses subsequent file changes")
	cmd.Flags().DurationVar(&wait, "wait-timeout", 3*time.Minute, "maximum startup verification time")
	return cmd
}
