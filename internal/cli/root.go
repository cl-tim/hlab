// Package cli builds the hlab command tree.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

// ExitError carries a specific process exit code up to main.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

func (e *ExitError) Unwrap() error { return e.Err }

// app holds state shared by every subcommand: the parsed global flags and the
// loaded config.
type app struct {
	configPath      string
	outputFlag      string
	concurrency     int
	connectTimeout  time.Duration
	insecureHostKey bool

	format output.Format
	cfg    *config.Config

	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

// Execute builds and runs the root command.
func Execute(ctx context.Context) error {
	root := NewRootCmd(os.Stdin, os.Stdout, os.Stderr)
	return root.ExecuteContext(ctx)
}

// NewRootCmd assembles the full command tree. Streams are injected so tests
// can drive prompts and capture output.
func NewRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	a := &app{in: stdin, out: stdout, errOut: stderr}

	root := &cobra.Command{
		Use:   "hlab",
		Short: "Centralised management for your homelab servers",
		Long: "hlab manages a fleet of Linux machines over SSH from a single inventory file.\n\n" +
			"Commands that act on machines take a target selector:\n" +
			"  all            every host\n" +
			"  @group         every member of a group\n" +
			"  #tag           every host with a tag\n" +
			"  name / web-*   a host name, optionally globbed\n" +
			"  !term          subtract matches, e.g. \"@prod,!#retired\"",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Cobra prints usage on flag errors; suppress the duplicate error line
		// since main renders it.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			format, err := output.ParseFormat(a.outputFlag)
			if err != nil {
				return err
			}
			a.format = format

			// `version` and `completion` must work without a valid config.
			if skipsConfig(cmd) {
				return nil
			}

			cfg, err := config.Load(a.configPath)
			if err != nil {
				return err
			}

			// With no config anywhere, create one so commands have somewhere to
			// record what they learn. Only the default location is created: an
			// explicit --config or $HLAB_CONFIG that does not exist has already
			// failed above, so a typo there cannot silently become a new file.
			if cfg.Path == "" {
				created, err := config.EnsureExists(config.DefaultPath())
				if err != nil {
					return err
				}
				if created {
					fmt.Fprintf(a.errOut, "Created %s\n", config.DefaultPath())
				}
				if cfg, err = config.Load(config.DefaultPath()); err != nil {
					return err
				}
			}

			a.cfg = cfg
			return nil
		},
	}

	f := root.PersistentFlags()
	f.StringVar(&a.configPath, "config", "", "path to the config file (default: "+config.DefaultPath()+")")
	f.StringVarP(&a.outputFlag, "output", "o", string(output.FormatTable), "output format: table, json, yaml")
	f.IntVarP(&a.concurrency, "concurrency", "c", 0, "max hosts to act on at once (default: from config, else 10)")
	f.DurationVar(&a.connectTimeout, "connect-timeout", 0, "SSH connect timeout (default: from config, else 10s)")
	f.BoolVar(&a.insecureHostKey, "insecure-host-key", false, "skip SSH host key verification (unsafe; for bootstrapping only)")

	root.AddCommand(
		newVersionCmd(a),
		newConfigCmd(a),
		newDiscoverCmd(a),
		newEnrollCmd(a),
		newSyncCmd(a),
		newNetCmd(a),
		newStackCmd(a),
		newHostsCmd(a),
		newRepairCmd(a),
		newUnenrollCmd(a),
		newPingCmd(a),
		newRunCmd(a),
		newUpdateCmd(a),
		newBackupCmd(a),
		newRestoreCmd(a),
		newRebootCmd(a),
		newLogsCmd(a),
		newCheckCmd(a),
		newSecretCmd(a),
		newDNSCmd(a),
	)

	return root
}

// skipsConfig reports whether a command can run without loading the inventory.
func skipsConfig(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "version", "completion", "help", "init":
			return true
		}
	}
	return false
}

// resolveConcurrency applies the flag override on top of the config value.
func (a *app) resolveConcurrency() int {
	if a.concurrency > 0 {
		return a.concurrency
	}
	if a.cfg != nil && a.cfg.Defaults.Concurrency > 0 {
		return a.cfg.Defaults.Concurrency
	}
	return 10
}

func (a *app) resolveConnectTimeout() time.Duration {
	if a.connectTimeout > 0 {
		return a.connectTimeout
	}
	if a.cfg != nil && a.cfg.Defaults.ConnectTimeout > 0 {
		return a.cfg.Defaults.ConnectTimeout.Duration()
	}
	return 10 * time.Second
}

// managedTargets resolves an optional selector to SSH targets, defaulting to
// every managed machine when none is given.
//
// Commands that read from the whole fleet use this so the operator does not
// have to name a selector just to mean "the machines I manage".
func (a *app) managedTargets(args []string) ([]sshx.Target, error) {
	if len(args) > 0 {
		_, targets, err := a.selectTargets(args[0])
		return targets, err
	}

	if a.cfg == nil {
		return nil, fmt.Errorf("no configuration loaded")
	}

	managed := a.cfg.Inventory.Managed()
	if len(managed) == 0 {
		return nil, fmt.Errorf(
			"no machines are under management yet; find them with `hlab discover` " +
				"and enrol them with `hlab enroll`")
	}

	targets := targetsFor(managed)
	return targets, nil
}

// selectTargets resolves a selector expression into SSH targets.
func (a *app) selectTargets(expr string) ([]inventory.Host, []sshx.Target, error) {
	if a.cfg == nil {
		return nil, nil, fmt.Errorf("no configuration loaded")
	}
	if len(a.cfg.Inventory.Managed()) == 0 {
		return nil, nil, fmt.Errorf(
			"no machines are under management yet; find them with `hlab discover` " +
				"and enrol them with `hlab discover --enroll`")
	}

	hosts, skipped, err := a.cfg.Inventory.Select(expr)
	if err != nil {
		return nil, nil, err
	}

	// Never silently narrow a selection: if the expression also matched
	// machines hlab cannot reach, say so.
	if len(skipped) > 0 && !a.format.Structured() {
		names := make([]string, 0, len(skipped))
		for _, h := range skipped {
			names = append(names, h.Name)
		}
		fmt.Fprintf(a.errOut, "Skipping %d unmanaged machine(s): %s\n",
			len(skipped), strings.Join(names, ", "))
	}

	targets := targetsFor(hosts)
	return hosts, targets, nil
}

func targetFor(h inventory.Host) sshx.Target {
	return sshx.Target{
		Name:         h.Name,
		Address:      h.Address,
		Port:         h.Port,
		User:         h.User,
		IdentityFile: h.IdentityFile,
	}
}

func targetsFor(hosts []inventory.Host) []sshx.Target {
	targets := make([]sshx.Target, len(hosts))
	for i, host := range hosts {
		targets[i] = targetFor(host)
	}
	return targets
}

// newDialer builds a dialer with the configured host key policy and no
// interactive prompt, for commands that must run unattended.
func (a *app) newDialer() (*sshx.Dialer, error) {
	knownHosts := ""
	if a.cfg != nil {
		knownHosts = a.cfg.Defaults.KnownHostsFile
	}
	return sshx.NewDialer(sshx.Options{
		KnownHostsFile:        knownHosts,
		InsecureIgnoreHostKey: a.insecureHostKey,
		ConnectTimeout:        a.resolveConnectTimeout(),
	})
}

// newExecutor builds a fan-out executor from the resolved global options.
func (a *app) newExecutor() (*sshx.Executor, error) {
	knownHosts := ""
	if a.cfg != nil {
		knownHosts = a.cfg.Defaults.KnownHostsFile
	}

	dialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile:        knownHosts,
		InsecureIgnoreHostKey: a.insecureHostKey,
		ConnectTimeout:        a.resolveConnectTimeout(),
	})
	if err != nil {
		return nil, err
	}
	return sshx.NewExecutor(dialer, a.resolveConcurrency()), nil
}
