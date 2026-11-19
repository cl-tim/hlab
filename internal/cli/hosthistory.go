package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/facts"
	"github.com/cl-tim/hlab/internal/output"
)

func newHostsHistoryCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "history <host>",
		Short: "Show recent kernel, address, and service changes observed by sync",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, ok := a.cfg.Inventory.Host(args[0])
			if !ok {
				return fmt.Errorf("unknown host %q", args[0])
			}
			store, err := facts.LoadStore(config.FactsPath())
			if err != nil {
				return err
			}
			history := store[host.Name].History
			if history == nil {
				history = []facts.Change{}
			}
			tbl := output.Table{Headers: []string{"OBSERVED AT", "FIELD", "BEFORE", "AFTER"}}
			for _, change := range history {
				tbl.AddRow(change.At.Format(time.RFC3339), change.Field, orDash(change.Before), orDash(change.After))
			}
			return output.Write(a.out, a.format, history, tbl)
		},
	}
}
