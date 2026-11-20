package cli

import (
	"fmt"
	"time"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/spf13/cobra"
)

func newFirmwareReleasesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "releases [MODEL]", Short: "List reviewed stock QIDI releases supported by this hlab build", Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			releases, err := migrate.QIDIReleases()
			if err != nil {
				return err
			}
			selected := []migrate.QIDIRelease{}
			table := output.Table{Headers: []string{"MODEL", "VERSION", "CONFIGURATION", "SCREEN"}}
			for _, release := range releases {
				if len(args) != 0 && args[0] != release.Model {
					continue
				}
				selected = append(selected, release)
				table.AddRow(release.Model, release.Version, "preserve or vendor", release.Screen.SHA256)
			}
			if len(selected) == 0 {
				return fmt.Errorf("no reviewed stock QIDI releases for this model")
			}
			return output.Write(a.out, a.format, selected, table)
		},
	}
}

func (a *app) updateQIDI(cmd *cobra.Command, printer inventory.Printer, version, policy, firmwareDir string, hostOnly, check bool, wait time.Duration) error {
	if _, err := migrate.QIDIReleaseFor(printer.Model, version); err != nil {
		return err
	}
	if policy != "preserve" && policy != "vendor" {
		return fmt.Errorf("--config-policy must be preserve or vendor")
	}
	if wait < 5*time.Second || wait > 10*time.Minute || wait%time.Second != 0 {
		return fmt.Errorf("--wait-timeout must be a whole number of seconds from 5s to 10m")
	}
	args := append([]string{"update-qidi"}, migrate.ConnectionArgs(printer)...)
	args = append(args, "--model", printer.Model, "--firmware-version", version, "--config-policy", policy,
		"--moonraker-port", fmt.Sprint(printer.MoonrakerPort), "--wait-seconds", fmt.Sprint(int(wait/time.Second)))
	if printer.APIKeyEnv != "" {
		args = append(args, "--api-key-env", printer.APIKeyEnv)
	}
	if hostOnly {
		args = append(args, "--host-only")
	}
	if firmwareDir != "" {
		args = append(args, "--firmware-dir", firmwareDir)
	}
	if check {
		args = append(args, "--check")
	} else {
		args = append(args, "--confirm", migrate.QIDIUpdateAction)
		fmt.Fprintf(a.errOut, "%s: preparing verified QIDI %s firmware and a local recovery backup before installation\n", printer.Name, version)
	}
	raw, runErr := a.migrationRunner().Output(cmd.Context(), args...)
	result, parseErr := migrate.ParseQIDIUpdate(raw)
	if parseErr != nil {
		if runErr != nil {
			return fmt.Errorf("%s: %w", printer.Name, runErr)
		}
		return parseErr
	}
	if result.Model != printer.Model || result.Target != version || result.CheckOnly != check || result.HostOnly != hostOnly || result.ConfigPolicy != policy {
		return fmt.Errorf("QIDI update receipt does not match the requested operation")
	}
	table := output.Table{Headers: []string{"PRINTER", "STATUS", "VERSION", "TARGET", "SCREEN", "LOCAL BACKUP", "DETAIL"}}
	table.AddRow(printer.Name, result.Status, result.After, result.Target, result.ScreenStatus, result.LocalBackup, result.Error)
	if err := output.Write(a.out, a.format, result, table); err != nil {
		return err
	}
	if runErr != nil {
		if result.Error != "" {
			return fmt.Errorf("%s: %s", printer.Name, result.Error)
		}
		return fmt.Errorf("%s: %w", printer.Name, runErr)
	}
	if !result.OK {
		return fmt.Errorf("%s: QIDI upgrade failed: %s", printer.Name, result.Error)
	}
	return nil
}
