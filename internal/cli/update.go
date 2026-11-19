package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/health"
	"github.com/cl-tim/hlab/internal/inventory"
	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/sshx"
	"github.com/cl-tim/hlab/internal/updates"
)

func newUpdateCmd(a *app) *cobra.Command {
	var (
		apply    bool
		rolling  bool
		allowNew bool
		reboot   bool
		pkgsOnly bool
		imgsOnly bool
		detail   bool
	)

	cmd := &cobra.Command{
		Use:   "update [selector]",
		Short: "Report and install pending package and container updates",
		Long: "Reports what is out of date on the selected machines: operating system\n" +
			"packages waiting to be installed, whether a reboot is pending, and any\n" +
			"container running an image its stack has already moved past.\n\n" +
			"The check changes nothing that runs. It does refresh the package index,\n" +
			"because asking what is upgradable against a stale index answers a question\n" +
			"nobody asked.\n\n" +
			"With --apply it installs them. Packages are upgraded with apt's `upgrade`,\n" +
			"never `full-upgrade`: it will not remove a package to satisfy a dependency.\n" +
			"Anything that would need a removal is held back and reported, which is the\n" +
			"right default for a machine you are not standing next to. Configuration\n" +
			"files already on the machine are kept.\n\n" +
			"A reboot is never taken unless --reboot is given. With it, a machine that\n" +
			"asks for one is restarted after its upgrade and hlab waits for it to come\n" +
			"back, checking the kernel's boot id so a reboot that silently failed is not\n" +
			"reported as one that worked. Machines that do not need a reboot are left\n" +
			"alone, and one that is the only recorded resolver for another machine is\n" +
			"refused.\n\n" +
			"Nothing is installed unless --apply is given.",
		Example: "  hlab update\n" +
			"  hlab update @servers\n" +
			"  hlab update apps-1 --apply\n" +
			"  hlab update --images --apply",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}
			if pkgsOnly && imgsOnly {
				return fmt.Errorf("--packages and --images are the two halves; pass neither to get both")
			}

			if rolling && !apply {
				return fmt.Errorf("--rolling requires --apply")
			}

			statuses, err := a.checkUpdates(cmd.Context(), hosts)
			if err != nil {
				return err
			}

			if err := a.reportUpdates(statuses, detail); err != nil {
				return err
			}
			if !apply {
				return a.summariseUpdates(statuses)
			}
			if rolling {
				for _, status := range statuses {
					if status.Err != "" {
						return fmt.Errorf("rolling update requires a successful update check on %s", status.Host)
					}
				}
			}
			return a.applyUpdates(cmd.Context(), hosts, statuses, !imgsOnly, !pkgsOnly, allowNew, reboot, rolling)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&rolling, "rolling", false, "verify host health and configured endpoints before updating the next host")
	f.BoolVar(&apply, "apply", false, "actually install the updates; without it nothing is changed")
	f.BoolVar(&allowNew, "allow-new", false,
		"let apt install new packages so held-back kernel updates can proceed; it still never removes one")
	f.BoolVar(&reboot, "reboot", false,
		"reboot a machine that asks for one after upgrading, and wait for it to come back")
	f.BoolVar(&pkgsOnly, "packages", false, "only operating system packages")
	f.BoolVar(&imgsOnly, "images", false, "only container images")
	f.BoolVar(&detail, "detail", false, "list the individual packages rather than counting them")
	return cmd
}

// checkUpdates asks every selected machine what is pending.
func (a *app) checkUpdates(ctx context.Context, hosts []inventory.Host) ([]updates.Status, error) {
	exec, err := a.newExecutor()
	if err != nil {
		return nil, err
	}

	targets := targetsFor(hosts)
	return updates.NewChecker(exec).Check(ctx, targets), nil
}

// reportUpdates renders the pending work.
func (a *app) reportUpdates(statuses []updates.Status, detail bool) error {
	tbl := output.Table{Headers: []string{"HOST", "PACKAGES", "SECURITY", "HELD", "CONTAINERS", "REBOOT"}}
	for _, s := range statuses {
		if s.Err != "" {
			tbl.AddRow(s.Host, "error: "+firstNonEmptyLine(s.Err), "-", "-", "-", "-")
			continue
		}
		tbl.AddRow(
			s.Host,
			fmt.Sprint(len(s.Packages)),
			orDash(countOrBlank(s.SecurityCount())),
			orDash(countOrBlank(len(s.HeldBack))),
			orDash(countOrBlank(len(s.StaleContainers()))),
			yesOrDash(s.Reboot),
		)
	}

	if err := output.Write(a.out, a.format, statuses, tbl); err != nil {
		return err
	}
	if a.format.Structured() {
		return nil
	}

	for _, s := range statuses {
		if s.Err != "" {
			continue
		}
		if s.IndexStale {
			fmt.Fprintf(a.errOut,
				"\n%s: the package index could not be refreshed, so these counts may be stale\n", s.Host)
		}
		if s.Partial {
			fmt.Fprintf(a.errOut, "\n%s: the check was cut short; this picture is incomplete\n", s.Host)
		}
		if detail && len(s.Packages) > 0 {
			fmt.Fprintf(a.out, "\n%s:\n", s.Host)
			for _, p := range s.Packages {
				mark := " "
				if p.Security {
					mark = "!"
				}
				fmt.Fprintf(a.out, "  %s %-38s %s\n", mark, p.Name, p.Version)
			}
		}
		for _, c := range s.StaleContainers() {
			fmt.Fprintf(a.out, "  %s: container %s is behind its image (%s)\n", s.Host, c.Name, c.Image)
		}
		if s.Reboot {
			fmt.Fprintf(a.out, "  %s: reboot pending%s\n", s.Host, becauseOf(s.RebootPkgs))
		}
	}
	return nil
}

// summariseUpdates prints the closing line of a check.
func (a *app) summariseUpdates(statuses []updates.Status) error {
	if a.format.Structured() {
		return nil
	}

	var pkgs, sec, hosts, failed int
	held := false
	for _, s := range statuses {
		if s.Err != "" {
			failed++
			continue
		}
		if len(s.HeldBack) > 0 {
			held = true
		}
		if !s.UpToDate() {
			hosts++
		}
		pkgs += len(s.Packages)
		sec += s.SecurityCount()
	}

	if pkgs == 0 && hosts == 0 {
		fmt.Fprintln(a.out, "\nEverything is up to date.")
	} else {
		fmt.Fprintf(a.out, "\n%d package(s) pending across %d machine(s), %d from security.\n",
			pkgs, hosts, sec)
		fmt.Fprintln(a.out, "Re-run with --apply to install them.")
		if held {
			fmt.Fprintln(a.out,
				"Some are held back because upgrading them needs a new package installed —\n"+
					"a kernel update looks like this. Add --allow-new to include them; apt still\n"+
					"never removes anything.")
		}
	}
	if failed > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

// applyUpdates installs what is pending, one machine at a time.
//
// Serial on purpose, like the network changes: an upgrade that breaks a
// machine should stop the run rather than reach the whole fleet before anyone
// notices.
func (a *app) applyUpdates(
	ctx context.Context, hosts []inventory.Host, statuses []updates.Status,
	doPkgs, doImgs, allowNew, reboot, rolling bool,
) error {
	pending := map[string]updates.Status{}
	for _, s := range statuses {
		pending[s.Host] = s
	}

	dialer, err := a.newDialer()
	if err != nil {
		return err
	}

	var changed, failed int
	for _, h := range hosts {
		s := pending[h.Name]
		if s.Err != "" {
			continue
		}
		// A machine with nothing pending but a reboot already outstanding is
		// precisely the one --reboot exists for, so the work it needs is
		// decided per kind rather than by the package count alone.
		wantPkgs := doPkgs && len(s.Packages) > 0
		wantImgs := doImgs && len(s.Stacks) > 0
		wantReboot := reboot && s.Reboot
		if !wantPkgs && !wantImgs && !wantReboot {
			continue
		}

		fmt.Fprintf(a.errOut, "\n%s:\n", h.Name)
		hostFailed := false

		if wantPkgs {
			needsReboot, err := a.runUpdateStep(ctx, dialer, h, "packages", updates.BuildUpgradeScript(allowNew))
			if err != nil {
				fmt.Fprintf(a.errOut, "  packages failed: %v\n", err)
				hostFailed = true
			} else if needsReboot {
				wantReboot = reboot
				if !reboot {
					fmt.Fprintf(a.errOut, "  %s now needs a reboot\n", h.Name)
				}
			}
		}
		if wantImgs && !hostFailed {
			if _, err := a.runUpdateStep(ctx, dialer, h, "images", updates.BuildImageScript()); err != nil {
				fmt.Fprintf(a.errOut, "  images failed: %v\n", err)
				hostFailed = true
			}
		}

		// The reboot comes last and only for a machine that asks for one, so a
		// selector can carry --reboot without restarting anything needlessly.
		if wantReboot && !hostFailed {
			if err := a.rebootHost(ctx, dialer, h, a.cfg.Inventory.Hosts(), false); err != nil {
				fmt.Fprintf(a.errOut, "  reboot failed: %v\n", err)
				hostFailed = true
			}
		}

		if rolling && !hostFailed {
			reports, err := a.collectHealth(ctx, []inventory.Host{h}, health.DefaultThresholds())
			if err != nil {
				return err
			}
			for _, report := range reports {
				if report.Err != "" || report.Partial || report.Worst() == health.Fail {
					hostFailed = true
					fmt.Fprintln(a.errOut, "  health check failed")
					if report.Err != "" {
						fmt.Fprintln(a.errOut, "  "+report.Err)
					}
					for _, finding := range report.Findings {
						fmt.Fprintf(a.errOut, "  %s: %s\n", finding.Check, finding.Detail)
					}
				}
			}
		}

		if hostFailed {
			failed++
			fmt.Fprintln(a.errOut, "  stopping here rather than repeating this on the rest")
			break
		}
		changed++
	}

	fmt.Fprintf(a.out, "\n%d updated, %d failed\n", changed, failed)
	if failed > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

// runUpdateStep runs one script on one machine and reports what it said.
func (a *app) runUpdateStep(
	ctx context.Context, dialer *sshx.Dialer, h inventory.Host, what, script string,
) (bool, error) {
	res, err := runPrivilegedOn(ctx, dialer, targetFor(h), script)
	if err != nil {
		return false, err
	}

	switch {
	case strings.Contains(res.Stdout, updates.MarkerNoop):
		fmt.Fprintf(a.errOut, "  %s: nothing to do\n", what)
		return false, nil
	case strings.Contains(res.Stdout, updates.MarkerUpgraded):
		fmt.Fprintf(a.errOut, "  %s: done\n", what)
		return strings.Contains(res.Stdout, "reboot:required"), nil
	}

	msg := firstNonEmptyLine(res.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exited %d without reporting success", res.ExitCode)
	}
	return false, fmt.Errorf("%s", strings.TrimPrefix(msg, "hlab:error "))
}

func countOrBlank(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

func yesOrDash(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}

func becauseOf(pkgs []string) string {
	if len(pkgs) == 0 {
		return ""
	}
	return " (" + strings.Join(pkgs, ", ") + ")"
}
