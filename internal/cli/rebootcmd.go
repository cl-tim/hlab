package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
)

// rebootNeededScript reports whether a machine is asking to be restarted.
const rebootNeededScript = `
if [ -f /var/run/reboot-required ]; then
  echo required
  cat /var/run/reboot-required.pkgs 2>/dev/null | head -n 5
else
  echo no
fi
uname -r 2>/dev/null
`

func newRebootCmd(a *app) *cobra.Command {
	var (
		apply    bool
		ifNeeded bool
	)

	cmd := &cobra.Command{
		Use:   "reboot [selector]",
		Short: "Restart machines and wait for them to come back",
		Long: "Restarts the selected machines one at a time, waiting for each to return\n" +
			"before starting the next, so a fleet is never all down at once.\n\n" +
			"A reboot is proved rather than assumed: hlab reads the kernel's boot id\n" +
			"before and after, because a machine answering SSH again is exactly what a\n" +
			"reboot that silently failed also looks like.\n\n" +
			"A machine that is the only recorded resolver for another managed machine is\n" +
			"refused, since taking it down removes name resolution for the other and\n" +
			"hlab — which connects by address — would never notice.\n\n" +
			"--if-needed restarts only the machines asking for one, which makes this\n" +
			"safe to point at a whole selector after an upgrade.\n\n" +
			"Nothing restarts without --apply.",
		Example: "  hlab reboot apps-1 --apply\n  hlab reboot all --if-needed --apply",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}

			exec, err := a.newExecutor()
			if err != nil {
				return err
			}
			targets := targetsFor(hosts)

			needed := map[string]bool{}
			kernels := map[string]string{}
			for _, r := range exec.Run(cmd.Context(), targets, rebootNeededScript) {
				lines := nonEmptyLines(r.Stdout)
				if len(lines) > 0 {
					needed[r.Host] = lines[0] == "required"
					kernels[r.Host] = lines[len(lines)-1]
				}
			}

			tbl := output.Table{Headers: []string{"HOST", "ASKS FOR ONE", "KERNEL", "ACTION"}}
			var todo []int
			for i, h := range hosts {
				action := "reboot"
				if ifNeeded && !needed[h.Name] {
					action = "skip"
				} else {
					todo = append(todo, i)
				}
				tbl.AddRow(h.Name, yesOrDash(needed[h.Name]), orDash(kernels[h.Name]), action)
			}
			if err := output.WriteTable(a.out, tbl); err != nil {
				return err
			}

			if len(todo) == 0 {
				fmt.Fprintln(a.out, "\nNothing to restart.")
				return nil
			}
			if !apply {
				fmt.Fprintf(a.out, "\n%d machine(s) would be restarted. Re-run with --apply.\n", len(todo))
				return nil
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}

			var done, failed int
			all := a.cfg.Inventory.Hosts()
			for _, i := range todo {
				h := hosts[i]
				fmt.Fprintf(a.errOut, "\n%s:\n", h.Name)
				// force when the operator asked for a reboot outright; with
				// --if-needed the machine's own answer decides.
				if err := a.rebootHost(cmd.Context(), dialer, h, all, !ifNeeded); err != nil {
					failed++
					fmt.Fprintf(a.errOut, "  failed: %v\n", err)
					fmt.Fprintln(a.errOut, "  stopping here rather than taking the rest down too")
					break
				}
				done++
			}

			fmt.Fprintf(a.out, "\n%d restarted, %d failed\n", done, failed)
			if failed > 0 {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&apply, "apply", false, "actually restart; without it nothing is changed")
	f.BoolVar(&ifNeeded, "if-needed", false, "only restart machines that are asking for one")
	return cmd
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range splitLines(s) {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
