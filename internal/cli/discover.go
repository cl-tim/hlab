package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/discover"
	"github.com/cl-tim/hlab/internal/inventory"
	"github.com/cl-tim/hlab/internal/output"
)

// defaultScanConcurrency is much higher than the executor's default: these are
// cheap TCP connects, not SSH sessions, and a /24 should finish in seconds.
const defaultScanConcurrency = 256

func newDiscoverCmd(a *app) *cobra.Command {
	cmd := newDiscoverScanCmd(a, true)
	cmd.Use = "discover [target...]"
	legacy := newDiscoverScanCmd(a, false)
	legacy.Hidden = true
	cmd.AddCommand(legacy)
	return cmd
}

func newDiscoverScanCmd(a *app, onboardByDefault bool) *cobra.Command {
	var (
		legacyAdopt   bool
		scanOnly      bool
		ports         []int
		probeTimeout  time.Duration
		bannerTimeout time.Duration
		maxTargets    int
		noResolve     bool
		onlyNew       bool
		emit          bool
		adopt         bool
		save          bool
	)
	enrollOpts := &enrollOptions{}

	cmd := &cobra.Command{
		Use:   "scan [target...]",
		Short: "Find machines with SSH open and offer to enroll them",
		Long: "Probes a range of addresses for reachable SSH ports, reads each server's\n" +
			"identification banner, resolves its name, and reports which machines are\n" +
			"already in your inventory and which are not.\n\n" +
			"Targets may be given as a CIDR block (192.0.2.0/24), a single address,\n" +
			"a range (192.0.2.10-192.0.2.50), or the last-octet shorthand\n" +
			"(192.0.2.10-50). With no target, the local IPv4 subnets are scanned.\n\n" +
			"In a terminal, discovery offers to enroll each unmanaged machine.\n" +
			"Use --scan-only to skip enrollment. Structured output and --emit also scan only.",
		Example: "  hlab discover\n" +
			"  hlab discover 192.0.2.0/24\n" +
			"  hlab discover 198.51.100.0/24 --ports 22,2222\n" +
			"  hlab discover 192.0.2.0/24 --new --emit >> homelab.yaml",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			enrolling := adopt || legacyAdopt
			if scanOnly && enrolling {
				return fmt.Errorf("--scan-only cannot be used with --enroll")
			}
			if onboardByDefault && !cmd.Flags().Changed("enroll") && !scanOnly && !emit && !a.format.Structured() {
				enrolling = enrolling || newPrompter(a.in, a.errOut).interactive()
			}
			if enrolling && emit {
				return fmt.Errorf("--enroll cannot be used with --emit")
			}
			if enrolling {
				if err := a.validateEnrollment(enrollOpts); err != nil {
					return err
				}
			} else if enrollOpts.syncAfter {
				return fmt.Errorf("--sync requires interactive enrollment")
			}
			specs := args
			if len(specs) == 0 {
				local, err := discover.LocalSubnets()
				if err != nil {
					return err
				}
				specs = local
				if !a.format.Structured() {
					fmt.Fprintf(a.errOut, "No target given; scanning local subnets: %s\n", strings.Join(specs, ", "))
				}
			}

			addrs, err := discover.ExpandTargets(specs, maxTargets)
			if err != nil {
				return err
			}

			concurrency := defaultScanConcurrency
			if cmd.Flags().Changed("concurrency") {
				concurrency = a.resolveConcurrency()
			}

			opts := discover.Options{
				Ports:         ports,
				ProbeTimeout:  probeTimeout,
				BannerTimeout: bannerTimeout,
				Concurrency:   concurrency,
				ResolveNames:  !noResolve,
			}

			if !a.format.Structured() {
				fmt.Fprintf(a.errOut, "Scanning %d address(es) on port %s...\n",
					len(addrs), joinInts(ports))

				// Only worth reporting progress on scans big enough to take a while.
				if len(addrs)*len(ports) > 512 {
					opts.OnProgress = func(scanned, total, found int) {
						fmt.Fprintf(a.errOut, "  %d/%d probed, %d found\n", scanned, total, found)
					}
				}
			}

			results := discover.NewScanner(opts).Scan(cmd.Context(), addrs)

			// Match observations to existing records, including ones recorded
			// under a DNS name that resolves to the scanned address.
			addressByName := make(map[string]string)
			taken := make(map[string]struct{})
			for _, h := range a.cfg.Inventory.Hosts() {
				addressByName[h.Name] = h.Address
				taken[h.Name] = struct{}{}
			}
			known := discover.BuildKnownHosts(cmd.Context(), addressByName, 2*time.Second)
			annotateFromInventory(results, a.cfg.Inventory, known)
			for i := range results {
				r := &results[i]
				if r.Managed {
					continue
				}
				r.Ignored = a.isIgnored(candidateFromScan(*r))
				if h, ok := a.cfg.Inventory.Host(known[r.Address]); ok && h.Port == r.Port {
					r.Ignored = r.Ignored || a.isIgnored(candidateFromHost(h))
				}
			}

			if save {
				diff, err := config.MergeObservations(
					a.configWritePath(), observations(results, known), time.Now())
				if err != nil {
					fmt.Fprintf(a.errOut, "warning: could not record the scan: %v\n", err)
				} else {
					applyDiff(results, diff)
					if !a.format.Structured() {
						fmt.Fprintf(a.errOut, "Recorded %d new, %d changed, %d unchanged in %s\n",
							diff.Added, diff.Updated, diff.Unchanged, a.configWritePath())
					}
				}
			}

			if onlyNew {
				results = discover.Unmanaged(results)
			}

			if emit {
				return discover.EmitInventory(a.out, discover.Unmanaged(results),
					taken, "hlab discover "+strings.Join(specs, " "), time.Now())
			}

			if enrolling {
				var fresh []candidate
				for _, r := range discover.Unmanaged(results) {
					if !enrollOpts.includeIgnored && r.Ignored {
						continue
					}
					fresh = append(fresh, candidateFromScan(r))
				}
				if err := a.renderScan(results, len(addrs)); err != nil {
					return err
				}
				if len(fresh) == 0 {
					fmt.Fprintln(a.errOut, "\nNo eligible machines to enroll.")
					return nil
				}
				fmt.Fprintf(a.errOut, "\nEnrolling %d unmanaged machine(s).\n", len(fresh))
				return a.runEnrollment(cmd.Context(), fresh, enrollOpts)
			}

			return a.renderScan(results, len(addrs))
		},
	}

	f := cmd.Flags()
	f.IntSliceVar(&ports, "ports", []int{22}, "TCP ports to probe on each address")
	f.DurationVar(&probeTimeout, "probe-timeout", time.Second, "per-address TCP connect timeout")
	f.DurationVar(&bannerTimeout, "banner-timeout", 2*time.Second, "how long to wait for an SSH banner once connected")
	f.IntVar(&maxTargets, "max-targets", discover.DefaultMaxTargets, "refuse to scan more addresses than this")
	f.BoolVar(&noResolve, "no-resolve", false, "skip reverse DNS lookups")
	f.BoolVar(&onlyNew, "new", false, "show only machines that are not in the inventory")
	f.BoolVar(&save, "save", true, "record what was found in the config")
	f.BoolVar(&emit, "emit", false, "print inventory YAML for the new machines instead of a table")
	f.BoolVar(&scanOnly, "scan-only", false, "scan without offering enrollment")
	f.BoolVar(&adopt, "enroll", false, "offer to enroll unmanaged machines found by the scan")
	f.BoolVar(&legacyAdopt, "adopt", false, "alias for --enroll")
	_ = f.MarkHidden("adopt")
	enrollOpts.register(f)

	return cmd
}

// annotateFromInventory marks each result against what is already recorded.
func annotateFromInventory(results []discover.Result, inv *inventory.Inventory, known discover.KnownHosts) {
	for i := range results {
		host, found := inv.ByAddress(results[i].Address, results[i].Port)
		if !found {
			// The machine may be recorded under a DNS name instead.
			if name, ok := known[results[i].Address]; ok {
				host, found = inv.Host(name)
			}
		}
		if !found {
			continue
		}
		results[i].SeenBefore = true
		results[i].Managed = host.Managed
		if host.Managed {
			results[i].ManagedAs = host.Name
		}
	}
}

// observations converts scan results into the form the config merges.
func observations(results []discover.Result, known discover.KnownHosts) []config.Observation {
	out := make([]config.Observation, 0, len(results))
	for _, r := range results {
		out = append(out, config.Observation{
			Address:   r.Address,
			Port:      r.Port,
			Hostname:  r.Hostname,
			OS:        r.OS,
			Banner:    r.Banner,
			MatchName: known[r.Address],
		})
	}
	return out
}

// applyDiff copies the merge outcome back onto the results for reporting.
func applyDiff(results []discover.Result, diff config.ScanDiff) {
	for i := range results {
		change, ok := diff.ByAddress[results[i].Address]
		if !ok {
			continue
		}
		results[i].Change = string(change.Kind)
		results[i].Changes = change.Changes
		if change.Kind == config.ChangeAdded {
			results[i].SeenBefore = false
		}
	}
}

// renderScan prints scan results as a table, or as structured data.
func (a *app) renderScan(results []discover.Result, scanned int) error {
	if a.format.Structured() {
		return output.Write(a.out, a.format, results, output.Table{})
	}

	if len(results) == 0 {
		fmt.Fprintf(a.errOut, "\nNothing responded across %d address(es).\n", scanned)
		return nil
	}

	tbl := output.Table{Headers: []string{"ADDRESS", "PORT", "HOSTNAME", "OS", "SSH", "MANAGED", "SINCE LAST SCAN"}}
	var managed, ignored, changed, fresh int

	for _, r := range results {
		managedCell := "no"
		if r.Managed {
			managedCell = "yes"
			managed++
		} else if r.Ignored {
			managedCell = "ignored"
			ignored++
		}

		status := "-"
		switch r.Change {
		case string(config.ChangeAdded):
			status = "new"
			fresh++
		case string(config.ChangeUpdated):
			status = strings.Join(r.Changes, "; ")
			changed++
		case string(config.ChangeUnchanged):
			status = "unchanged"
		}

		tbl.AddRow(r.Address, r.Port, orDash(shortHostname(r.Hostname)),
			orDash(r.OS), orDash(r.Software), managedCell, status)
	}

	fmt.Fprintln(a.out)
	if err := output.WriteTable(a.out, tbl); err != nil {
		return err
	}

	unmanaged := len(results) - managed - ignored
	fmt.Fprintf(a.out, "\n%d responded, %d managed, %d unmanaged, %d ignored, %d new, %d changed\n",
		len(results), managed, unmanaged, ignored, fresh, changed)
	return nil
}

// shortHostname trims a FQDN down to its first label for table display.
func shortHostname(h string) string {
	if h == "" {
		return ""
	}
	label, _, _ := strings.Cut(h, ".")
	return label
}

func joinInts(vals []int) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, fmt.Sprint(v))
	}
	return strings.Join(parts, ",")
}
