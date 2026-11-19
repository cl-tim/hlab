package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/facts"
)

// sameMachineName reports whether a record name and a machine's own hostname
// refer to the same thing, allowing for a domain suffix and for the
// sanitisation applied when a name is derived automatically.
func sameMachineName(recordName, hostname string) bool {
	short, _, _ := strings.Cut(hostname, ".")
	return strings.EqualFold(recordName, short) ||
		strings.EqualFold(recordName, sanitiseInventoryName(short))
}

func newHostsRenameCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <old> <new>",
		Short: "Rename a machine's inventory record",
		Long: "Changes the name a machine is recorded and selected under.\n\n" +
			"Names derived automatically come from reverse DNS, which can be wrong or\n" +
			"shared — a DNS server answering PTR queries for its whole subnet hands the\n" +
			"same name to every address on it. `hlab discover services` reports when a\n" +
			"machine's own hostname disagrees with its record.\n\n" +
			"Everything else about the record is preserved, including its collected\n" +
			"state, which is moved across in the facts store.",
		Example: "  hlab hosts rename pi-2 pi-hole-2",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			oldName, newName := args[0], args[1]

			if oldName == newName {
				return fmt.Errorf("%q is already the name", oldName)
			}
			if err := config.ValidateHostName(newName); err != nil {
				return err
			}

			host, ok := a.cfg.Inventory.Host(oldName)
			if !ok {
				return fmt.Errorf("no machine named %q; see `hlab hosts list`", oldName)
			}
			if _, taken := a.cfg.Inventory.Host(newName); taken {
				return fmt.Errorf("%q is already used by another machine", newName)
			}

			if err := config.RenameHost(a.configWritePath(), oldName, newName); err != nil {
				return err
			}

			// Collected state is keyed by name, so it has to move too or the
			// machine appears to have never been collected from.
			factsPath := config.FactsPath()
			if err := facts.RenameInStore(factsPath, oldName, newName); err != nil {
				fmt.Fprintf(a.errOut, "warning: renamed in the config, but the facts store could not be updated: %v\n", err)
			}

			fmt.Fprintf(a.out, "Renamed %s -> %s (%s)\n", oldName, newName, host.Address)
			return nil
		},
	}
}
