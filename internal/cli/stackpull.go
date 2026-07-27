package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/stack"
)

func newStackListCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list <host>",
		Short: "Show the Docker Compose projects running on a machine",
		Long: "Lists every compose project docker knows about, whether hlab deployed it or\n" +
			"not, and says which are already under hlab's ownership.\n\n" +
			"This only reads.",
		Example: "  hlab stack list apps-1",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args)
			if err != nil {
				return err
			}

			projects, err := a.listProjects(cmd.Context(), host)
			if err != nil {
				return err
			}

			tbl := output.Table{Headers: []string{"PROJECT", "STATUS", "OWNER", "CONFIG"}}
			for _, p := range projects {
				owner := "external"
				if p.Adopted() {
					owner = "hlab"
				}
				tbl.AddRow(p.Name, p.Status, owner, p.ConfigFiles)
			}
			return output.Write(a.out, a.format, projects, tbl)
		},
	}
	return cmd
}

func newStackPullCmd(a *app) *cobra.Command {
	var out string

	cmd := &cobra.Command{
		Use:   "pull <host> [project]",
		Short: "Copy compose files off a machine so hlab can manage them",
		Long: "Reads the compose files of projects that were set up by hand and writes them\n" +
			"locally, so a stack that only ever existed on one machine becomes something\n" +
			"reviewable and redeployable.\n\n" +
			"Files are written under " + stack.LocalDirName + " in hlab's home directory, not the\n" +
			"working directory. That is deliberate: a compose file routinely carries\n" +
			"secrets — API keys, encryption keys, database passwords — and hlab is often\n" +
			"run from inside a git checkout. Pass --out to put them somewhere else, and\n" +
			"read what you get before committing it anywhere.\n\n" +
			"Nothing on the machine is changed.",
		Example: "  hlab stack pull apps-1\n  hlab stack pull apps-1 apps",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args[:1])
			if err != nil {
				return err
			}

			projects, err := a.listProjects(cmd.Context(), host)
			if err != nil {
				return err
			}

			wanted := ""
			if len(args) == 2 {
				wanted = args[1]
			}

			var selected []stack.Project
			for _, p := range projects {
				if wanted != "" && p.Name != wanted {
					continue
				}
				// A project hlab already owns has its source of truth here
				// already; pulling it back would overwrite the reviewed copy
				// with whatever is on the machine.
				if wanted == "" && p.Adopted() {
					continue
				}
				selected = append(selected, p)
			}
			if len(selected) == 0 {
				if wanted != "" {
					return fmt.Errorf("%s has no compose project called %q", host.Name, wanted)
				}
				fmt.Fprintf(a.out, "Nothing to pull: every project on %s is already managed by hlab.\n", host.Name)
				return nil
			}

			var paths []string
			for _, p := range selected {
				paths = append(paths, p.Files()...)
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), stack.BuildReadScript(paths))
			if err != nil {
				return err
			}
			contents := stack.ParseFiles(res.Stdout)

			root := out
			if root == "" {
				root = filepath.Join(config.Home(), stack.LocalDirName)
			}

			for _, p := range selected {
				if err := a.writePulledProject(root, host, p, contents); err != nil {
					return err
				}
			}

			fmt.Fprintf(a.errOut,
				"\nReview these before deploying or committing them; compose files carry secrets.\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&out, "out", "", "directory to write into (default: hlab's home)")
	return cmd
}

// writePulledProject saves one project's compose file and reports the data it
// keeps beside itself.
func (a *app) writePulledProject(
	root string, host inventory.Host, p stack.Project, contents map[string]string,
) error {
	if err := stack.ValidateName(p.Name); err != nil {
		return fmt.Errorf("project %q cannot be stored locally: %w", p.Name, err)
	}

	files := p.Files()
	if len(files) == 0 || contents[files[0]] == "" {
		fmt.Fprintf(a.errOut, "%-14s could not be read from %s\n", p.Name, host.Name)
		return nil
	}
	body := contents[files[0]]

	dir := filepath.Join(root, p.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// 0600 for the same reason the starter config is: these routinely hold
	// keys and passwords.
	target := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(target, []byte(body+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}

	fmt.Fprintf(a.out, "%s -> %s\n", p.Name, target)

	if ports, err := stack.PublishedPorts([]byte(body)); err == nil && len(ports) > 0 {
		var claims []string
		for _, port := range ports {
			claims = append(claims, fmt.Sprintf("%d (%s)", port.Port, port.Service))
		}
		fmt.Fprintf(a.out, "  publishes: %s\n", strings.Join(claims, ", "))
	}

	// The compose file alone is not the stack. Anything mounted from beside it
	// is data that has to move with it, and saying so here is the difference
	// between an adoption that works and one that comes up empty.
	mounts, err := stack.RelativeBindMounts([]byte(body))
	if err == nil && len(mounts) > 0 {
		fmt.Fprintf(a.out, "  keeps data in %s, relative to %s on %s:\n",
			plural(len(mounts), "path", "paths"), orDash(p.Dir()), host.Name)
		for _, m := range mounts {
			fmt.Fprintf(a.out, "    %-28s (%s)\n", m.Source, m.Service)
		}
	}
	return nil
}

// listProjects asks a machine what compose projects it has.
func (a *app) listProjects(ctx context.Context, host inventory.Host) ([]stack.Project, error) {
	dialer, err := a.newDialer()
	if err != nil {
		return nil, err
	}

	res, err := runPrivilegedOn(ctx, dialer, targetFor(host), stack.BuildListScript())
	if err != nil {
		return nil, err
	}
	if strings.Contains(res.Stdout, stack.MarkerNoDocker) {
		return nil, fmt.Errorf("%s has no docker installed", host.Name)
	}
	return stack.ParseProjects(res.Stdout)
}

// oneManagedHost resolves a selector that must name exactly one machine.
func (a *app) oneManagedHost(args []string) (inventory.Host, error) {
	hosts, err := a.managedHosts(args)
	if err != nil {
		return inventory.Host{}, err
	}
	if len(hosts) != 1 {
		return inventory.Host{}, fmt.Errorf(
			"this takes one machine at a time; %q matched %d", args[0], len(hosts))
	}
	return hosts[0], nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
