package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	hostconfig "github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/printerlab/config"
)

const starterConfig = `defaults:
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
	command := &cobra.Command{Use: "config", Short: "Manage the printer inventory"}
	command.AddCommand(
		&cobra.Command{
			Use:   "path",
			Short: "Print the file the printer inventory is kept in",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				_, err := fmt.Fprintln(a.out, config.WritePath(a.configPath))
				return err
			},
		},
		&cobra.Command{
			Use:   "init",
			Short: "Add a starter printer inventory to the hlab config",
			Long: "Add a `" + config.SectionKey + ":` section to the hlab config, creating the config if\n" +
				"there is none. Hosts and everything else already in the file are left as they are.",
			Args: cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				path := config.WritePath(a.configPath)
				if err := ensureHostScaffold(path); err != nil {
					return err
				}
				if err := config.Init(path, starterConfig); err != nil {
					return err
				}
				_, err := fmt.Fprintf(a.out, "Added a printer inventory to %s\n", path)
				return err
			},
		},
		newConfigMigrateCmd(a),
	)
	return command
}

// newConfigMigrateCmd folds a pre-merge standalone inventory into hlab's
// config. It is explicit rather than automatic: it rewrites the file that
// also holds every server's record, which is not a thing to do as a side
// effect of `hlab printer status`.
func newConfigMigrateCmd(a *app) *cobra.Command {
	var from string
	command := &cobra.Command{
		Use:   "migrate",
		Short: "Move a standalone printer inventory into the hlab config",
		Long: "Move the printer inventory from its own file into the `" + config.SectionKey + ":` section of\n" +
			"the hlab config, so one file describes every machine. Comments are kept. The\n" +
			"hlab config is backed up beside itself first, and the old file is renamed\n" +
			"with a .migrated suffix rather than deleted.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			source := from
			if source == "" {
				source = os.Getenv(config.EnvConfigPath)
			}
			if source == "" {
				source = config.LegacyPath()
			}
			if _, err := os.Stat(source); err != nil {
				return fmt.Errorf("no standalone printer inventory to migrate: %w", err)
			}
			target := a.configPath
			if target == "" {
				target = config.UnifiedPath()
			}
			if err := ensureHostScaffold(target); err != nil {
				return err
			}
			if err := config.Migrate(source, target); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Moved the printer inventory from %s into %s\n", source, target)

			// The inventory is now in the hlab config. Retire the old file so
			// there is one answer to "where are my printers", but keep its
			// bytes: a rename is undone by renaming it back.
			retired := source + ".migrated"
			if err := os.Rename(source, retired); err != nil {
				fmt.Fprintf(a.errOut, "Could not rename %s: %v\nRemove it yourself; it is no longer read.\n", source, err)
				return nil
			}
			fmt.Fprintf(a.out, "Kept the old file as %s\n", retired)
			if os.Getenv(config.EnvConfigPath) != "" {
				fmt.Fprintf(a.errOut, "%s still points at the old file; unset it.\n", config.EnvConfigPath)
			}
			return nil
		},
	}
	command.Flags().StringVar(&from, "from", "", "standalone inventory to move (default: "+config.LegacyPath()+")")
	return command
}

// ensureHostScaffold creates hlab's config when path is its default location
// and nothing is there, so the printer section lands in a file that reads as
// an hlab config rather than one holding printers alone. Any other path is
// left to be created bare.
func ensureHostScaffold(path string) error {
	if same, err := filepath.Abs(path); err != nil || same != mustAbs(hostconfig.DefaultPath()) {
		return err
	}
	_, err := hostconfig.EnsureExists(path)
	return err
}

func mustAbs(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}
