package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/output"
)

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and bootstrap the hlab configuration",
	}
	cmd.AddCommand(newConfigInitCmd(), newConfigPathCmd(a), newConfigHomeCmd(a), newConfigShowCmd(a))
	return cmd
}

func newConfigHomeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "home",
		Short: "Show the directory hlab stores its data in",
		Long: "Prints the directory holding hlab's config, its management key and any\n" +
			"other state it owns. It lives under your home directory so that running\n" +
			"hlab inside a repository never leaves data there. Override with $HLAB_HOME.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(a.out, config.Home())
			return nil
		},
	}
}

func newConfigInitCmd() *cobra.Command {
	var (
		path  string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter config file",
		Long:  "Writes a commented example config you can edit to describe your fleet.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			target := path
			if target == "" {
				target = config.DefaultPath()
			}

			if _, err := os.Stat(target); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite it", target)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("stat %s: %w", target, err)
			}

			if dir := filepath.Dir(target); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return fmt.Errorf("create %s: %w", dir, err)
				}
			}

			// 0600: the inventory records usernames and internal addresses.
			if err := os.WriteFile(target, []byte(starterConfig), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", target, err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\nEdit it to describe your hosts, then run `hlab hosts list`.\n", target)
			return nil
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "where to write the config (default: "+config.DefaultPath()+")")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}

func newConfigPathCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Show which config file is in use",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.cfg.Path == "" {
				fmt.Fprintln(a.errOut, "No config file found. Searched:")
				for _, p := range config.SearchPaths() {
					fmt.Fprintf(a.errOut, "  %s\n", p)
				}
				fmt.Fprintln(a.errOut, "Run `hlab config init` to create one.")
				return &ExitError{Code: 1}
			}
			fmt.Fprintln(a.out, a.cfg.Path)
			return nil
		},
	}
}

func newConfigShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the resolved configuration, with defaults applied",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			type resolved struct {
				Path     string              `json:"path" yaml:"path"`
				Home     string              `json:"home" yaml:"home"`
				KeyPath  string              `json:"managementKey" yaml:"managementKey"`
				Defaults config.Defaults     `json:"defaults" yaml:"defaults"`
				Hosts    []hostView          `json:"hosts" yaml:"hosts"`
				Groups   map[string][]string `json:"groups,omitempty" yaml:"groups,omitempty"`
			}

			view := resolved{
				Path:     a.cfg.Path,
				Home:     config.Home(),
				KeyPath:  config.ManagementKeyPath(),
				Defaults: a.cfg.Defaults,
				Groups:   a.cfg.Inventory.Groups(),
			}
			for _, h := range a.cfg.Inventory.Hosts() {
				view.Hosts = append(view.Hosts, newHostView(h))
			}

			tbl := output.Table{Headers: []string{"SETTING", "VALUE"}}
			path := a.cfg.Path
			if path == "" {
				path = "(none — using built-in defaults)"
			}
			tbl.AddRow("config", path)
			tbl.AddRow("home", config.Home())
			tbl.AddRow("managementKey", config.ManagementKeyPath())
			tbl.AddRow("user", orDash(a.cfg.Defaults.User))
			tbl.AddRow("port", a.cfg.Defaults.Port)
			tbl.AddRow("identityFile", orDash(a.cfg.Defaults.IdentityFile))
			tbl.AddRow("knownHostsFile", orDash(a.cfg.Defaults.KnownHostsFile))
			tbl.AddRow("connectTimeout", a.cfg.Defaults.ConnectTimeout)
			tbl.AddRow("concurrency", a.cfg.Defaults.Concurrency)
			tbl.AddRow("hosts", len(view.Hosts))
			tbl.AddRow("groups", len(view.Groups))

			return output.Write(a.out, a.format, view, tbl)
		},
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

const starterConfig = `# hlab configuration
#
# Target selectors used by commands like ` + "`hlab run`" + `:
#   all          every host
#   @web         every member of the "web" group
#   #docker      every host tagged "docker"
#   nas-01       one host by name (globs work too: "pi-*")
#   !term        subtract, e.g. "all,!#retired"

defaults:
  # Applied to every host that does not override them.
  user: root
  port: 22
  # identityFile: ~/.ssh/id_ed25519
  # knownHostsFile defaults to ~/.ssh/known_hosts
  connectTimeout: 10s
  concurrency: 10

hosts:
  - name: nas-01
    address: 192.0.2.10
    tags: [storage, debian]
    description: Main NAS

  - name: docker-01
    address: 192.0.2.20
    user: youruser
    tags: [docker, debian]
    description: Container host

  - name: pi-dns
    address: 192.0.2.30
    user: pi
    tags: [dns, raspbian]
    description: Pi-hole

groups:
  core:
    - nas-01
    - docker-01
  edge:
    - pi-dns
`
