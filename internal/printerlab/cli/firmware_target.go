package cli

import (
	"fmt"
	"strings"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/spf13/cobra"
)

type firmwareUpdateTarget struct {
	address, model, user, identity, apiKeyEnv string
	port, moonrakerPort                       int
}

func (target *firmwareUpdateTarget) addFlags(cmd *cobra.Command) {
	flags := cmd.Flags()
	flags.StringVar(&target.address, "target", "", "stock QIDI hostname or IPv4 address; bypass inventory without enrolling")
	flags.StringVar(&target.model, "model", "", "model for --target (inferred when the catalogue selection has one reviewed model)")
	flags.StringVar(&target.user, "user", "", "SSH login for --target (default: release's printer account)")
	flags.IntVar(&target.port, "ssh-port", 22, "SSH port for --target")
	flags.StringVar(&target.identity, "identity", "", "SSH private key for --target (default: normal OpenSSH authentication)")
	flags.IntVar(&target.moonrakerPort, "moonraker-port", 7125, "printer's loopback Moonraker port for --target")
	flags.StringVar(&target.apiKeyEnv, "api-key-env", "", "environment variable containing the Moonraker API key for --target")
}

// Only an explicit flag bypasses inventory. A misspelled saved name must never
// silently become a network address, and an empty --target must fail closed.
func directFirmwareUpdate(cmd *cobra.Command) bool {
	return cmd.Name() == "update" && cmd.Parent() != nil && cmd.Parent().Name() == "firmware" && cmd.Flags().Changed("target")
}

func (a *app) firmwareUpdatePrinter(cmd *cobra.Command, args []string, target firmwareUpdateTarget, version string) (inventory.Printer, error) {
	if !directFirmwareUpdate(cmd) {
		for _, name := range []string{"model", "user", "ssh-port", "identity", "moonraker-port", "api-key-env"} {
			if cmd.Flags().Changed(name) {
				return inventory.Printer{}, fmt.Errorf("--%s requires --target; saved printers use their inventory connection settings", name)
			}
		}
		return a.onePrinter(args[0])
	}
	release, err := migrate.SelectQIDIRelease(target.model, version)
	if err != nil {
		return inventory.Printer{}, err
	}
	user := target.user
	if user == "" {
		user = release.Account
	}
	printer := inventory.Printer{
		Name: target.address, Address: target.address, Model: release.Model, Firmware: inventory.FirmwareStock,
		User: user, PrinterUser: release.Account, Port: target.port, IdentityFile: target.identity,
		MoonrakerPort: target.moonrakerPort, APIKeyEnv: target.apiKeyEnv,
	}
	if err := migrate.ValidateSSHConnection(printer); err != nil {
		return inventory.Printer{}, err
	}
	if target.moonrakerPort < 1 || target.moonrakerPort > 65535 {
		return inventory.Printer{}, fmt.Errorf("--moonraker-port must be between 1 and 65535")
	}
	if strings.ContainsAny(target.identity, "\x00\r\n") {
		return inventory.Printer{}, fmt.Errorf("SSH identity path contains a control character")
	}
	if strings.ContainsAny(target.apiKeyEnv, "\x00\r\n=") {
		return inventory.Printer{}, fmt.Errorf("invalid API key environment variable name")
	}
	return printer, nil
}
