package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/facts"
	"github.com/cl-tim/hlab/internal/inventory"
	"github.com/cl-tim/hlab/internal/stack"
)

func newStackCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Deploy Docker Compose stacks to managed machines",
	}
	cmd.AddCommand(
		newStackListCmd(a),
		newStackDiffCmd(a),
		newStackPullCmd(a),
		newStackAdoptCmd(a),
		newStackRenameCmd(a),
		newStackDeployCmd(a),
		newStackInstallDockerCmd(a),
	)
	return cmd
}

func newStackDeployCmd(a *app) *cobra.Command {
	var (
		file      string
		name      string
		apply     bool
		force     bool
		syncAfter bool
		withFn    []string
	)

	cmd := &cobra.Command{
		Use:   "deploy <host> [project]",
		Short: "Push a compose file to a machine and bring the stack up",
		Long: "Copies a compose file to " + stack.RemoteDir + "/<name> on the machine and runs\n" +
			"`docker compose up -d`.\n\n" +
			"Before doing anything it checks the ports the stack would publish against\n" +
			"what the machine is already listening on, using the last collected\n" +
			"snapshot. A stack that cannot bind its port fails in a way that is easy to\n" +
			"miss, so a clash is refused rather than attempted — pass --force if the\n" +
			"conflicting service is on its way out.\n\n" +
			"Configuration a stack mounts rather than embeds — a Caddyfile, a rules\n" +
			"file — is shipped with --with, so the whole stack stays in one reviewable\n" +
			"place instead of being hand-written on the machine.\n\n" +
			"Deploying is repeatable: an unchanged stack is a no-op.\n\n" +
			"Nothing is done unless --apply is given.",
		Example: "  hlab stack deploy apps-1 technitium --apply --sync\n" +
			"  hlab stack deploy apps-1 --file technitium.yaml\n" +
			"  hlab stack deploy apps-1 --file caddy.yaml --with Caddyfile --apply",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if syncAfter && !apply {
				return fmt.Errorf("--sync requires --apply")
			}
			project := ""
			if len(args) == 2 {
				project = args[1]
			}
			file, name, err := resolveDeploySource(config.Home(), project, file, name)
			if err != nil {
				return err
			}

			compose, err := os.ReadFile(file)
			if err != nil {
				return fmt.Errorf("read %s: %w", file, err)
			}

			assets, err := readAssets(withFn)
			if err != nil {
				return err
			}

			hosts, err := a.managedHosts(args[:1])
			if err != nil {
				return err
			}
			if len(hosts) != 1 {
				return fmt.Errorf("deploy takes one machine at a time; %q matched %d", args[0], len(hosts))
			}
			host := hosts[0]

			ports, err := stack.PublishedPorts(compose)
			if err != nil {
				return err
			}

			fmt.Fprintf(a.out, "Stack %q -> %s (%s)\n", name, host.Name, host.Address)
			if len(ports) > 0 {
				var claims []string
				for _, p := range ports {
					claims = append(claims, fmt.Sprintf("%d (%s)", p.Port, p.Service))
				}
				fmt.Fprintf(a.out, "  publishes: %s\n", strings.Join(claims, ", "))
			}

			conflicts := portConflicts(hostListeningPorts(host), ports, a.portsHeldByThisStack(cmd.Context(), host, name))
			if len(conflicts) > 0 {
				for _, c := range conflicts {
					fmt.Fprintf(a.out, "  conflict:  port %s\n", c)
				}
				if !force {
					return fmt.Errorf(
						"refusing to deploy: %s is already listening on those ports.\n"+
							"Free them first, or pass --force if the conflicting service is being replaced",
						host.Name)
				}
				fmt.Fprintln(a.errOut, "  --force given; deploying over the conflict")
			}

			if !apply {
				fmt.Fprintln(a.out, "\nNot applied. Re-run with --apply.")
				return nil
			}

			script, err := stack.BuildDeployScript(name, compose, assets)
			if err != nil {
				return err
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}

			res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), script)
			if err != nil {
				return err
			}
			if strings.Contains(res.Stdout, stack.MarkerNoDocker) {
				return fmt.Errorf("%s has no docker installed", host.Name)
			}
			if !strings.Contains(res.Stdout, stack.MarkerDeployed) {
				msg := firstNonEmptyLine(res.Stderr)
				if msg == "" {
					msg = fmt.Sprintf("deploy exited %d without reporting success", res.ExitCode)
				}
				return fmt.Errorf("%s", strings.TrimPrefix(msg, "hlab:error "))
			}

			fmt.Fprintln(a.out, "\nDeployed:")
			states := stack.ParseContainerStates(res.Stdout)
			if len(states) == 0 {
				fmt.Fprintln(a.out, "  (compose reported no containers)")
			}
			for _, s := range states {
				fmt.Fprintf(a.out, "  %-28s %-16s %s\n", s.Name, s.Service, s.State)
			}
			if syncAfter {
				exec, err := a.newExecutor()
				if err != nil {
					return fmt.Errorf("stack deployed, inventory refresh failed: %w", err)
				}
				snapshots := facts.NewCollector(exec).Collect(cmd.Context(), targetsFor(hosts))
				if err := factsExitCode(snapshots); err != nil {
					return fmt.Errorf("stack deployed, inventory refresh failed: %s", snapshots[0].Error)
				}
				if err := a.persistFacts(snapshots); err != nil {
					return fmt.Errorf("stack deployed, inventory refresh failed: %w", err)
				}
				return nil
			}
			fmt.Fprintf(a.errOut, "\nRun `hlab sync %s` to record what is now running.\n", host.Name)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&file, "file", "", "compose file to deploy")
	f.StringVar(&name, "name", "", "stack name (default: the compose file's base name)")
	f.StringArrayVar(&withFn, "with", nil,
		"extra file to deploy alongside the compose file, e.g. a Caddyfile; repeatable")
	f.BoolVar(&syncAfter, "sync", false, "refresh the host inventory after deploying")
	f.BoolVar(&apply, "apply", false, "actually deploy; without it nothing is changed")
	f.BoolVar(&force, "force", false, "deploy even if the machine already listens on a published port")

	return cmd
}

// portConflicts reports published ports the machine is already listening on.
//
// The listeners are passed in rather than read here so the decision can be
// tested without a collected snapshot on disk.
func resolveDeploySource(home, project, file, name string) (string, string, error) {
	if project != "" {
		if file != "" || name != "" {
			return "", "", fmt.Errorf("pass a project name or --file/--name")
		}
		name = project
		file = filepath.Join(home, stack.LocalDirName, project, "compose.yaml")
	} else if file == "" {
		return "", "", fmt.Errorf("pass a saved project name or --file")
	} else if name == "" {
		name = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	if err := stack.ValidateName(name); err != nil {
		return "", "", err
	}
	return file, name, nil
}

func portConflicts(open []listeningPort, ports []stack.PublishedPort, own map[int]bool) []string {
	if len(ports) == 0 {
		return nil
	}

	// Only ports reachable off-box can actually clash with a published port.
	listening := map[int]string{}
	for _, p := range open {
		listening[p.port] = p.process
	}

	var out []string
	for _, p := range ports {
		// A port this stack already publishes is not a conflict: `compose up`
		// replaces those containers. Reporting it as one would mean passing
		// --force on every routine redeploy, which is precisely when the check
		// should still be doing its job.
		if own[p.Port] {
			continue
		}
		if proc, taken := listening[p.Port]; taken {
			out = append(out, fmt.Sprintf("%d is held by %s", p.Port, orDash(proc)))
		}
	}
	return out
}

// portsHeldByThisStack reports the host ports the machine's current copy of
// this stack already publishes, read from the compose file hlab deployed.
//
// Anything that goes wrong here yields no exclusions, so a stack that cannot
// be read is checked as strictly as a brand new one.
func (a *app) portsHeldByThisStack(
	ctx context.Context, host inventory.Host, name string,
) map[int]bool {
	dialer, err := a.newDialer()
	if err != nil {
		return nil
	}

	path := stack.RemoteDir + "/" + name + "/compose.yaml"
	res, err := runPrivilegedOn(ctx, dialer, targetFor(host), stack.BuildReadScript([]string{path}))
	if err != nil {
		return nil
	}
	body := stack.ParseFiles(res.Stdout)[path]
	if body == "" {
		return nil
	}
	ports, err := stack.PublishedPorts([]byte(body))
	if err != nil {
		return nil
	}

	held := make(map[int]bool, len(ports))
	for _, p := range ports {
		held[p.Port] = true
	}
	return held
}

// listeningPort is one socket from the last collected snapshot.
type listeningPort struct {
	port    int
	process string
}

// hostListeningPorts reads what a machine was last seen listening on.
//
// Ports live in the facts store rather than on the host record, so a conflict
// check depends on a collection having happened. No snapshot means no check —
// reported by the caller rather than silently passing.
func hostListeningPorts(h inventory.Host) []listeningPort {
	store, err := facts.LoadStore(config.FactsPath())
	if err != nil {
		return nil
	}
	snapshot, ok := store[h.Name]
	if !ok {
		return nil
	}

	out := make([]listeningPort, 0, len(snapshot.Ports))
	for _, p := range snapshot.Ports {
		// A loopback-only socket cannot clash with a published container port.
		if !p.Exposed() {
			continue
		}
		out = append(out, listeningPort{port: p.Port, process: p.Process})
	}
	return out
}

func newStackInstallDockerCmd(a *app) *cobra.Command {
	var apply bool

	cmd := &cobra.Command{
		Use:   "install-docker <host>",
		Short: "Install Docker on a managed machine",
		Long: "Installs Docker using the vendor's convenience script from get.docker.com.\n\n" +
			"That script is fetched into a file and sanity-checked before being run,\n" +
			"rather than piped straight into a root shell, so a truncated download\n" +
			"cannot half-execute. It configures Docker's own apt repository, which is\n" +
			"why it is preferred over the distribution's package.\n\n" +
			"A machine that already has Docker and the compose plugin is left alone.\n\n" +
			"Nothing is done unless --apply is given.",
		Example: "  hlab stack install-docker dns-2 --apply",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}
			if len(hosts) != 1 {
				return fmt.Errorf("install-docker takes one machine at a time; %q matched %d", args[0], len(hosts))
			}
			host := hosts[0]

			fmt.Fprintf(a.out, "Install Docker on %s (%s)\n", host.Name, host.Address)
			fmt.Fprintln(a.out, "  source: https://get.docker.com (Docker's own installer)")

			if !apply {
				fmt.Fprintln(a.out, "\nNot applied. Re-run with --apply.")
				return nil
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}

			res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), stack.InstallDockerScript)
			if err != nil {
				return err
			}

			switch {
			case strings.Contains(res.Stdout, stack.MarkerDockerPresent):
				fmt.Fprintf(a.out, "\nAlready installed: %s\n", stack.DockerVersionFrom(res.Stdout))
			case strings.Contains(res.Stdout, stack.MarkerDockerInstalled):
				fmt.Fprintf(a.out, "\nInstalled: %s\n", stack.DockerVersionFrom(res.Stdout))
				fmt.Fprintf(a.errOut, "Run `hlab sync %s` to record it.\n", host.Name)
			default:
				msg := firstNonEmptyLine(res.Stderr)
				if msg == "" {
					msg = fmt.Sprintf("the installer exited %d without reporting success", res.ExitCode)
				}
				return fmt.Errorf("%s", strings.TrimPrefix(msg, "hlab:error "))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "actually install; without it nothing is changed")
	return cmd
}

// readAssets loads the files given with --with.
//
// The name on the machine is the file's base name, so a stack can be deployed
// from anywhere without the local directory layout leaking into the remote one.
func readAssets(paths []string) ([]stack.Asset, error) {
	assets := make([]stack.Asset, 0, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		name := filepath.Base(path)
		if err := stack.ValidateAssetName(name); err != nil {
			return nil, err
		}
		assets = append(assets, stack.Asset{Name: name, Content: content})
	}
	return assets, nil
}
