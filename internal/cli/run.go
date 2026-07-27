package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newRunCmd(a *app) *cobra.Command {
	var confirm bool

	cmd := &cobra.Command{
		Use:   "run <selector> -- <command>...",
		Short: "Run a shell command on the selected hosts",
		Long: "Runs a command on every selected host, in parallel, and reports the results.\n\n" +
			"Put the command after `--` so its own flags are not parsed by hlab.\n" +
			"The command words are joined with spaces and handed to the remote login shell,\n" +
			"so shell syntax (pipes, redirects, &&) works — quote it to protect it from your\n" +
			"local shell.\n\n" +
			"Exits non-zero if any host fails to connect or the command exits non-zero anywhere.",
		Example: "  hlab run all -- uptime\n" +
			"  hlab run @core -- 'df -h /'\n" +
			"  hlab run '#debian' -- 'apt list --upgradable 2>/dev/null | wc -l'",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			selector := args[0]
			command := strings.Join(args[1:], " ")
			if strings.TrimSpace(command) == "" {
				return fmt.Errorf("no command given")
			}

			hosts, targets, err := a.selectTargets(selector)
			if err != nil {
				return err
			}

			if confirm {
				names := make([]string, 0, len(hosts))
				for _, h := range hosts {
					names = append(names, h.Name)
				}
				fmt.Fprintf(a.errOut, "About to run on %d host(s): %s\n  $ %s\n",
					len(names), strings.Join(names, ", "), command)
				ok, err := askYesNo(cmd.InOrStdin(), a.errOut, "Proceed?")
				if err != nil {
					return err
				}
				if !ok {
					fmt.Fprintln(a.errOut, "Aborted.")
					return &ExitError{Code: 130}
				}
			}

			exec, err := a.newExecutor()
			if err != nil {
				return err
			}

			results := exec.Run(cmd.Context(), targets, command)
			return a.renderResults(results, true)
		},
	}

	cmd.Flags().BoolVar(&confirm, "confirm", false, "list the target hosts and ask before running")
	return cmd
}
