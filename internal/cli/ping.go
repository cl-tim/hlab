package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

func newPingCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "ping <selector>",
		Short: "Check SSH reachability and authentication for the selected hosts",
		Long: "Opens and immediately closes an SSH connection to every selected host.\n" +
			"This verifies DNS, reachability, host key trust and authentication in one shot.\n\n" +
			"Exits non-zero if any host fails.",
		Example: "  hlab ping all\n  hlab ping @core\n  hlab ping '#docker'",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, targets, err := a.selectTargets(args[0])
			if err != nil {
				return err
			}

			exec, err := a.newExecutor()
			if err != nil {
				return err
			}

			results := exec.Ping(cmd.Context(), targets)
			return a.renderResults(results, false)
		},
	}
}

// renderResults prints a summary table (or structured output) and returns an
// ExitError when any host failed. When showOutput is set, each host's stdout
// and stderr are printed below the table in table mode.
func (a *app) renderResults(results []sshx.Result, showOutput bool) error {
	failed := 0
	for _, r := range results {
		if !r.OK() {
			failed++
		}
	}

	if a.format.Structured() {
		if err := output.Write(a.out, a.format, results, output.Table{}); err != nil {
			return err
		}
		if failed > 0 {
			return &ExitError{Code: 1}
		}
		return nil
	}

	if showOutput {
		for _, r := range results {
			body := r.Stdout
			if body == "" && r.Stderr == "" && r.Err == nil {
				continue
			}
			fmt.Fprintf(a.out, "\n=== %s ===\n", r.Host)
			if body != "" {
				fmt.Fprint(a.out, ensureNewline(body))
			}
			if r.Stderr != "" {
				fmt.Fprint(a.errOut, ensureNewline(r.Stderr))
			}
		}
		fmt.Fprintln(a.out)
	}

	tbl := output.Table{Headers: []string{"HOST", "STATUS", "EXIT", "TIME"}}
	for _, r := range results {
		status := "ok"
		switch {
		case r.Err != nil:
			status = "error: " + r.Err.Error()
		case r.ExitCode != 0:
			status = "failed"
		}
		exit := fmt.Sprint(r.ExitCode)
		if r.Err != nil {
			exit = "-"
		}
		tbl.AddRow(r.Host, status, exit, r.Duration.Round(1e6))
	}

	if err := output.WriteTable(a.out, tbl); err != nil {
		return err
	}

	fmt.Fprintf(a.out, "\n%d/%d ok\n", len(results)-failed, len(results))

	if failed > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

func ensureNewline(s string) string {
	if s == "" || s[len(s)-1] == '\n' {
		return s
	}
	return s + "\n"
}
