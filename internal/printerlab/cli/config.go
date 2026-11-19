package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/printerlab/config"
)

const starterConfig = `# printerlab inventory
defaults:
  user: mks
  port: 22
  moonrakerPort: 7125
  connectTimeout: 10s
  concurrency: 4
  # identityFile: ~/.ssh/id_ed25519
  # apiKeyEnv: PLAB_MOONRAKER_API_KEY

  # workspaceRoot creates a local Klippee folder for each printer.
  klippee:
    executable: klippee
    # workspaceRoot: C:\path\to\printer-workspaces

# Discover printers with:
#   hlab printer discover scan --emit
#
# Add printers here:
# printers:
#   - name: printer-name
#     address: printer-name.example
#     model: plus4
#     firmware: stock
#     profile: mainline-klipper-beacon
#     tags: [plus4, stock]
#     # components:
#     #   hotend:
#     #     type: hotend
#     #     model: Replace with the actual installed model
#     #     status: installed
#     # Collect configured hardware with: hlab printer components capture printer-name
#     # SSH bootstrap password environment variable; remove after key setup.
#     # identityFile: C:\Users\operator\.ssh\printer-name-plab-ed25519
#     # sshPasswordEnv: PRINTER_NAME_SSH_BOOTSTRAP_PASSWORD
#     # migration:
#     #   beaconSerial: /dev/serial/by-id/usb-Beacon_Beacon_RevH_CHANGE_ME-if00
#     #   profileDir: C:\path\to\custom-profile  # optional override
#     # klippee:
#     #   workspace: C:\an\explicit\printer-workspace

printers: []
groups: {}
`

func newConfigCmd(a *app) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Manage printerlab configuration"}
	command.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the inventory path",
			RunE: func(_ *cobra.Command, _ []string) error {
				path := a.configPath
				if path == "" {
					path = config.DefaultPath()
				}
				_, err := fmt.Fprintln(a.out, path)
				return err
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Create a starter inventory",
			RunE: func(_ *cobra.Command, _ []string) error {
				path := a.configPath
				if path == "" {
					path = config.DefaultPath()
				}
				if _, err := os.Stat(path); err == nil {
					return fmt.Errorf("%s already exists", path)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(starterConfig), 0o600); err != nil {
					return err
				}
				_, err := fmt.Fprintf(a.out, "Created %s\n", path)
				return err
			},
		},
	)
	return command
}
