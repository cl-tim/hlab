// Package cli builds printerlab's command tree.
package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/config"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
)

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

type app struct {
	configPath      string
	outputFlag      string
	concurrency     int
	connectTimeout  time.Duration
	yes             bool
	guidedFirmware  bool
	firmwareSecrets map[string]string

	format output.Format
	cfg    *config.Config

	in     io.Reader
	out    io.Writer
	errOut io.Writer
}

// NewCommand creates the printer command group.
func NewCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	a := &app{in: stdin, out: stdout, errOut: stderr}
	root := &cobra.Command{
		Use:   "printer",
		Short: "Manage a fleet of stock and mainline QIDI printers",
		Long: "Manage QIDI printers through Moonraker and SSH.\n\n" +
			"Selectors: all, @group, #tag, name, glob, and !exclusion.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			format, err := output.ParseFormat(a.outputFlag)
			if err != nil {
				return err
			}
			a.format = format
			if skipsConfig(cmd) {
				return nil
			}
			cfg, err := config.Load(a.configPath)
			if err != nil {
				return err
			}
			if cfg.Path == "" {
				return fmt.Errorf("no printer inventory found; create one with `hlab printer config init`")
			}
			a.cfg = cfg
			return nil
		},
	}
	flags := root.PersistentFlags()
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	flags.StringVar(&a.configPath, "config", "", "inventory path (default: "+config.DefaultPath()+")")
	flags.StringVarP(&a.outputFlag, "output", "o", "table", "output format: table, json, yaml")
	flags.IntVarP(&a.concurrency, "concurrency", "c", 0, "maximum concurrent read operations")
	flags.DurationVar(&a.connectTimeout, "connect-timeout", 0, "Moonraker/SSH connection timeout")
	flags.BoolVarP(&a.yes, "yes", "y", false, "assume yes for operations that require approval")

	root.AddCommand(
		newVersionCmd(a),
		newConfigCmd(a),
		newDiscoverCmd(a),
		newPrintersCmd(a),
		newComponentsCmd(a),
		newStatusCmd(a),
		newIdleTimeoutCmd(a),
		newCancelPrintCmd(a),
		newAuditCmd(a),
		newKlippeeCmd(a),
		newMainlineCmd(a),
		newFirmwareCmd(a),
	)
	return root
}

func (a *app) requireApproval(action string) error {
	if a.yes {
		return nil
	}
	return fmt.Errorf("refusing %s without -y/--yes", action)
}

func skipsConfig(cmd *cobra.Command) bool {
	if cmd.Name() == "write-emmc" && cmd.Parent() != nil && cmd.Parent().Name() == "firmware" {
		return true
	}
	if cmd.Name() == "prepare" && cmd.Parent() != nil && cmd.Parent().Name() == "firmware" {
		return true // Guided preparation can create the first inventory.
	}
	for current := cmd; current != nil; current = current.Parent() {
		if current.Name() == "version" || current.Name() == "completion" {
			return true
		}
		if current.Name() == "init" && current.Parent() != nil && current.Parent().Name() == "config" {
			return true
		}
		if current.Name() == "path" && current.Parent() != nil && current.Parent().Name() == "config" {
			return true
		}
		if current.Name() == "scan" && current.Parent() != nil && current.Parent().Name() == "discover" {
			return true
		}
	}
	return false
}

func (a *app) timeout() time.Duration {
	if a.connectTimeout > 0 {
		return a.connectTimeout
	}
	if a.cfg != nil && a.cfg.Defaults.ConnectTimeout != 0 {
		return a.cfg.Defaults.ConnectTimeout.Duration()
	}
	return 10 * time.Second
}

func (a *app) parallelism() int {
	if a.concurrency > 0 {
		return a.concurrency
	}
	if a.cfg != nil && a.cfg.Defaults.Concurrency > 0 {
		return a.cfg.Defaults.Concurrency
	}
	return 4
}

func (a *app) selectPrinters(args []string) ([]inventory.Printer, error) {
	expression := "all"
	if len(args) > 0 {
		expression = args[0]
	}
	return a.cfg.Inventory.Select(expression)
}

func (a *app) onePrinter(name string) (inventory.Printer, error) {
	selected, err := a.cfg.Inventory.Select(name)
	if err != nil {
		return inventory.Printer{}, err
	}
	if len(selected) != 1 {
		return inventory.Printer{}, fmt.Errorf("operation requires exactly one printer, got %d", len(selected))
	}
	return selected[0], nil
}
