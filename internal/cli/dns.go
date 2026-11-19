package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/dnssync"
	"github.com/cl-tim/hlab/internal/output"
)

func newDNSCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dns",
		Short: "Keep the DNS server in step with the inventory",
	}
	cmd.AddCommand(newDNSSyncCmd(a))
	return cmd
}

func newDNSSyncCmd(a *app) *cobra.Command {
	var (
		zone     string
		apply    bool
		noPin    bool
		showAll  bool
		hostArgs string
	)
	_ = hostArgs

	cmd := &cobra.Command{
		Use:   "sync <dns-host>",
		Short: "Publish the inventory as DNS records and DHCP reservations",
		Long: "hlab already knows every machine's name, address and hardware address.\n" +
			"This publishes that to Technitium: an A record per managed machine, and a\n" +
			"DHCP reservation pinning each address to the interface that holds it.\n\n" +
			"Everything runs on the DNS server itself, against its own loopback, using\n" +
			"the admin password already stored beside that stack. hlab therefore needs\n" +
			"no Technitium credential of its own, nothing is kept on this workstation,\n" +
			"and no token crosses the network.\n\n" +
			"Records the zone holds that the inventory does not are reported and never\n" +
			"removed. The zone is shared — DHCP registers every device on the network\n" +
			"into it — so an unrecognised name is far more likely to be a phone than a\n" +
			"mistake.\n\n" +
			"Unmanaged machines are excluded: a scan saw them once, nobody has verified\n" +
			"them, and hlab cannot tell when they go away.\n\n" +
			"Nothing is written unless --apply is given.",
		Example: "  hlab dns sync apps-1\n  hlab dns sync apps-1 --apply",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args)
			if err != nil {
				return err
			}
			if err := dnssync.ValidateZone(zone); err != nil {
				return err
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}

			readScript, err := dnssync.BuildReadScript(zone)
			if err != nil {
				return err
			}
			res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), readScript)
			if err != nil {
				return err
			}
			have, err := dnssync.ParseRecords(res.Stdout)
			if err != nil {
				return err
			}

			all := a.cfg.Inventory.Hosts()
			want, skipped := dnssync.DesiredRecords(all, zone)
			plan := dnssync.Diff(zone, want, have)
			plan.Skipped = skipped
			if !noPin {
				lo, hi := dnssync.ParseScopeRange(res.Stdout)
				plan.Reservations = dnssync.MissingReservations(
					dnssync.DesiredReservations(all), dnssync.ParseReservations(res.Stdout), lo, hi)
			}

			if err := a.reportDNSPlan(plan, showAll); err != nil {
				return err
			}
			// Structured output has to stay parseable, so nothing follows the
			// document on stdout.
			if !plan.Actionable() {
				if !a.format.Structured() {
					fmt.Fprintln(a.out, "\nThe zone already matches the inventory.")
				}
				return nil
			}
			if !apply {
				if !a.format.Structured() {
					fmt.Fprintln(a.out, "\nNot applied. Re-run with --apply.")
				}
				return nil
			}

			syncScript, err := dnssync.BuildSyncScript(zone, plan.Changes, plan.Reservations)
			if err != nil {
				return err
			}
			out, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), syncScript)
			if err != nil {
				return err
			}
			for _, line := range splitLines(out.Stdout) {
				if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "hlab:") {
					fmt.Fprintf(a.errOut, "  %s\n", line)
				}
			}
			if !strings.Contains(out.Stdout, dnssync.MarkerDone) {
				return fmt.Errorf("%s",
					strings.TrimPrefix(orDash(firstNonEmptyLine(out.Stderr)), "hlab:error "))
			}

			fmt.Fprintf(a.out, "\nZone %s now matches the inventory.\n", zone)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&zone, "zone", "lan", "the DNS zone to publish into")
	f.BoolVar(&apply, "apply", false, "actually write the records; without it nothing is changed")
	f.BoolVar(&noPin, "no-reservations", false, "publish names only, without pinning DHCP addresses")
	f.BoolVar(&showAll, "all", false, "also list records that already match")
	return cmd
}

// reportDNSPlan renders the difference between the inventory and the zone.
func (a *app) reportDNSPlan(plan dnssync.Plan, showAll bool) error {
	tbl := output.Table{Headers: []string{"ACTION", "NAME", "ADDRESS", "NOTE"}}
	for _, c := range plan.Changes {
		switch c.Action {
		case dnssync.Unchanged:
			if !showAll {
				continue
			}
			tbl.AddRow(string(c.Action), c.Name, c.Address, "")
		case dnssync.Update:
			tbl.AddRow(string(c.Action), c.Name, c.Address, "was "+c.Was)
		case dnssync.Extra:
			tbl.AddRow(string(c.Action), c.Name, c.Address, "not in the inventory; left alone")
		default:
			tbl.AddRow(string(c.Action), c.Name, c.Address, "")
		}
	}

	if err := output.Write(a.out, a.format, plan, tbl); err != nil {
		return err
	}
	if a.format.Structured() {
		return nil
	}

	if len(plan.Reservations) > 0 {
		fmt.Fprintf(a.out, "\n%d DHCP reservation(s) would be asserted, pinning each machine's\n"+
			"address to the interface that actually holds it.\n", len(plan.Reservations))
	}
	for _, s := range plan.Skipped {
		fmt.Fprintf(a.errOut, "  skipped: %s\n", s)
	}
	return nil
}
