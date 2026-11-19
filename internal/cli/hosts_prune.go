package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/facts"
	"github.com/cl-tim/hlab/internal/inventory"
)

func newHostsPruneCmd(a *app) *cobra.Command {
	var (
		apply      bool
		staleAfter time.Duration
	)

	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Find duplicate and stale records, and optionally remove them",
		Long: "Reports records that describe the same machine, and unmanaged records that\n" +
			"have not been seen for a while.\n\n" +
			"A machine with both ethernet and wireless online holds two addresses, so a\n" +
			"network scan sees it twice and records it twice. Duplicates are identified\n" +
			"by the MAC addresses on each record's interfaces: the same hardware cannot\n" +
			"be two machines, whatever addresses it currently holds. Hostnames alone are\n" +
			"not enough — two machines can share a badly configured name.\n\n" +
			"When a duplicate set is collapsed the managed record is kept, since it\n" +
			"carries the credentials. Managed machines are never treated as stale: being\n" +
			"switched off is not a reason to forget how to reach one.\n\n" +
			"Nothing is removed unless --apply is given.",
		Example: "  hlab hosts prune\n" +
			"  hlab hosts prune --apply\n" +
			"  hlab hosts prune --stale-after 90d --apply",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			hosts := a.cfg.Inventory.Hosts()
			duplicates := config.FindDuplicates(hosts)
			stale := config.FindStale(hosts, time.Now().Add(-staleAfter))

			if len(duplicates) == 0 && len(stale) == 0 {
				fmt.Fprintln(a.out, "Nothing to prune.")
				return nil
			}

			var doomed []string

			for _, g := range duplicates {
				fmt.Fprintf(a.out, "\nDuplicate: %s\n", g.Reason)
				fmt.Fprintf(a.out, "  keep    %-20s %-16s %s\n",
					g.Keep.Name, g.Keep.Address, managedLabel(g.Keep.Managed))
				for _, h := range g.Remove {
					fmt.Fprintf(a.out, "  remove  %-20s %-16s %s\n",
						h.Name, h.Address, managedLabel(h.Managed))
					doomed = append(doomed, h.Name)
				}

				// The surviving record may reach the machine over the very
				// interface that is due to be switched off.
				if kind := config.InterfaceKindFor(g.Keep, g.Keep.Address); kind == inventory.IfaceWiFi {
					fmt.Fprintf(a.out,
						"  note:   %s is reached over wifi (%s) — disabling wireless would cut hlab off\n",
						g.Keep.Name, g.Keep.Address)
					if wired := config.WiredAddressOf(g.Keep); wired != "" {
						fmt.Fprintf(a.out,
							"          its ethernet address is %s; repoint the record before turning wifi off\n",
							wired)
					}
				}
			}

			if len(stale) > 0 {
				fmt.Fprintf(a.out, "\nNot seen in %s:\n", staleAfter)
				for _, h := range stale {
					fmt.Fprintf(a.out, "  remove  %-20s %-16s last seen %s\n",
						h.Name, h.Address, orDash(relativeTime(h.LastSeen)))
					doomed = append(doomed, h.Name)
				}
			}

			if !apply {
				fmt.Fprintf(a.out, "\n%d record(s) would be removed. Re-run with --apply.\n", len(doomed))
				return nil
			}

			if err := config.RemoveHosts(a.configWritePath(), doomed); err != nil {
				return err
			}
			// Their collected state goes too, or the store grows entries for
			// machines that no longer exist.
			for _, name := range doomed {
				if err := facts.RemoveFromStore(config.FactsPath(), name); err != nil {
					fmt.Fprintf(a.errOut, "warning: could not clear facts for %s: %v\n", name, err)
				}
			}

			fmt.Fprintf(a.out, "\nRemoved %d record(s) from %s\n", len(doomed), a.configWritePath())
			return nil
		},
	}

	cmd.Flags().BoolVar(&apply, "apply", false, "actually remove the records; without it nothing is changed")
	cmd.Flags().DurationVar(&staleAfter, "stale-after", 30*24*time.Hour,
		"treat unmanaged records not seen within this long as stale")

	return cmd
}

func managedLabel(managed bool) string {
	if managed {
		return "managed"
	}
	return "unmanaged"
}
