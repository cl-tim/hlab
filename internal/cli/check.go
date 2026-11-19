package cli

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/health"
	"github.com/cl-tim/hlab/internal/inventory"
	"github.com/cl-tim/hlab/internal/output"
)

func newCheckCmd(a *app) *cobra.Command {
	var (
		diskPct  int
		restarts int
		quiet    bool
	)

	cmd := &cobra.Command{
		Use:   "check [selector]",
		Short: "Assert that machines are working, not merely reachable",
		Long: "Answers a different question to `ping`. Reachable means SSH answered; this\n" +
			"asks whether the machine is doing its job.\n\n" +
			"It looks for filesystems filling up, failed systemd units, containers that\n" +
			"are exited, restarting, crash-looping or reporting themselves unhealthy,\n" +
			"managed stacks running fewer services than their compose file declares, a\n" +
			"pending reboot, an unsynchronised clock, and a machine that restarted when\n" +
			"nobody asked it to.\n\n" +
			"Thresholds are deliberately quiet. A check that cries wolf gets ignored,\n" +
			"and an ignored check is worse than no check.\n\n" +
			"Exits non-zero if anything failed, so it is usable from a timer.\n\n" +
			"This only reads.",
		Example: "  hlab check\n  hlab check apps-1\n  hlab check --quiet",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}

			thresholds := health.DefaultThresholds()
			thresholds.DiskPercent = diskPct
			thresholds.Restarts = restarts
			reports, err := a.collectHealth(cmd.Context(), hosts, thresholds)
			if err != nil {
				return err
			}

			return a.reportHealth(reports, quiet)
		},
	}

	f := cmd.Flags()
	f.IntVar(&diskPct, "disk", health.DefaultThresholds().DiskPercent,
		"percentage above which a filesystem is reported")
	f.IntVar(&restarts, "restarts", health.DefaultThresholds().Restarts,
		"restart count above which a container is treated as crash-looping")
	f.BoolVar(&quiet, "quiet", false, "only show machines with something wrong")
	return cmd
}

func (a *app) collectHealth(ctx context.Context, hosts []inventory.Host, thresholds health.Thresholds) ([]health.Report, error) {
	exec, err := a.newExecutor()
	if err != nil {
		return nil, err
	}
	reports := make([]health.Report, 0, len(hosts))
	for _, r := range exec.Run(ctx, targetsFor(hosts), health.CheckScript) {
		if !r.OK() {
			message := fmt.Sprintf("check exited with status %d", r.ExitCode)
			if r.Err != nil {
				message = r.Err.Error()
			}
			reports = append(reports, health.Report{Host: r.Host, Err: message})
			continue
		}
		report := health.Parse(r.Host, r.Stdout, thresholds)
		for _, endpoint := range a.cfg.Endpoints[r.Host] {
			if err := endpoint.Probe(ctx); err != nil {
				report.Findings = append(report.Findings, health.Finding{
					Check: "endpoint", Severity: health.Fail,
					Detail: fmt.Sprintf("%s: %v", endpoint.Name, err),
				})
			}
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Host < reports[j].Host })
	return reports, nil
}

// reportHealth renders the outcome and decides the exit code.
func (a *app) reportHealth(reports []health.Report, quiet bool) error {
	tbl := output.Table{Headers: []string{"HOST", "STATUS", "SUMMARY"}}
	var failed, warned int

	for _, r := range reports {
		switch {
		case r.Err != "":
			failed++
			tbl.AddRow(r.Host, "unreachable", firstNonEmptyLine(r.Err))
		case r.Healthy():
			if !quiet {
				tbl.AddRow(r.Host, "ok", healthySummary(r))
			}
		default:
			if r.Worst() == health.Fail {
				failed++
			} else {
				warned++
			}
			tbl.AddRow(r.Host, string(r.Worst()), r.Findings[0].Detail)
		}
	}

	if err := output.Write(a.out, a.format, reports, tbl); err != nil {
		return err
	}
	if a.format.Structured() {
		if failed > 0 {
			return &ExitError{Code: 1}
		}
		return nil
	}

	// The table shows the first finding; anything with more than one gets the
	// rest spelled out, because the second problem is often the real one.
	for _, r := range reports {
		if r.Err != "" || len(r.Findings) < 2 {
			continue
		}
		fmt.Fprintf(a.out, "\n%s:\n", r.Host)
		for _, f := range r.Findings {
			fmt.Fprintf(a.out, "  %-6s %-11s %s\n", f.Severity, f.Check, f.Detail)
		}
	}

	switch {
	case failed == 0 && warned == 0:
		fmt.Fprintf(a.out, "\n%d machine(s) checked, all healthy.\n", len(reports))
	default:
		fmt.Fprintf(a.out, "\n%d machine(s) checked, %d failing, %d with warnings.\n",
			len(reports), failed, warned)
	}

	if failed > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

// healthySummary says what was actually verified, so a clean run is evidence
// rather than an empty space.
func healthySummary(r health.Report) string {
	parts := []string{}
	if n := len(r.Filesystems); n > 0 {
		parts = append(parts, fmt.Sprintf("%s disk", worstDisk(r)))
	}
	if n := len(r.Containers); n > 0 {
		parts = append(parts, fmt.Sprintf("%d container(s)", n))
	}
	if n := len(r.Stacks); n > 0 {
		parts = append(parts, fmt.Sprintf("%d stack(s)", n))
	}
	if len(parts) == 0 {
		return "nothing to report"
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

func worstDisk(r health.Report) string {
	worst := 0
	for _, fs := range r.Filesystems {
		if fs.Percent > worst {
			worst = fs.Percent
		}
	}
	return fmt.Sprintf("%d%%", worst)
}
