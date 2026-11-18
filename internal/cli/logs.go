package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/shellword"
)

// logsTemplate fetches the logs of a container or a systemd unit.
//
// The name is resolved rather than declared: an operator chasing a problem
// knows what the thing is called, not which of the two kinds of thing it is.
// Containers are tried first because a name that is both is nearly always the
// container — the unit of the same name is usually what starts it.
const logsTemplate = `
SUDO=
if [ "$(id -u 2>/dev/null)" != 0 ] && command -v sudo >/dev/null 2>&1; then
  if sudo -n true >/dev/null 2>&1; then
    SUDO='sudo -n'
  fi
fi

NAME=__NAME__
LINES=__LINES__

if command -v docker >/dev/null 2>&1 && $SUDO docker inspect "$NAME" >/dev/null 2>&1; then
  $SUDO docker logs --tail "$LINES" __SINCE_D__ "$NAME" 2>&1
elif command -v journalctl >/dev/null 2>&1 && systemctl list-unit-files "$NAME*" 2>/dev/null | grep -q "$NAME"; then
  $SUDO journalctl -u "$NAME" -n "$LINES" __SINCE_J__ --no-pager 2>&1
else
  echo "hlab:not-found no container or service called $NAME here"
fi
`

func newLogsCmd(a *app) *cobra.Command {
	var (
		lines int
		since string
		grep  string
	)

	cmd := &cobra.Command{
		Use:   "logs <selector> <name>",
		Short: "Fetch the logs of a container or service across machines",
		Long: "Fetches recent logs for a named container or systemd unit from every\n" +
			"selected machine.\n\n" +
			"The name is resolved on each machine rather than declared up front:\n" +
			"chasing a problem, you know what the thing is called, not whether it is a\n" +
			"container or a unit. Containers are checked first, because a name that is\n" +
			"both is almost always the container.\n\n" +
			"A machine that has neither says so rather than returning nothing, which\n" +
			"would be indistinguishable from a service that is simply quiet.\n\n" +
			"This only reads.",
		Example: "  hlab logs apps-1 caddy\n" +
			"  hlab logs all technitium --lines 200\n" +
			"  hlab logs apps-1 technitium --grep error",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args[:1])
			if err != nil {
				return err
			}
			name := args[1]
			if strings.ContainsAny(name, " \t\n") {
				return fmt.Errorf("%q is not a container or service name", name)
			}

			exec, err := a.newExecutor()
			if err != nil {
				return err
			}
			targets := targetsFor(hosts)

			sinceD, sinceJ := "", ""
			if since != "" {
				sinceD = "--since " + shellword.Quote(since)
				sinceJ = "--since " + shellword.Quote(since)
			}
			script := strings.NewReplacer(
				"__NAME__", shellword.Quote(name),
				"__LINES__", fmt.Sprint(lines),
				"__SINCE_D__", sinceD,
				"__SINCE_J__", sinceJ,
			).Replace(logsTemplate)

			var missing int
			for _, r := range exec.Run(cmd.Context(), targets, script) {
				fmt.Fprintf(a.out, "\n=== %s ===\n", r.Host)
				if r.Err != nil {
					fmt.Fprintf(a.out, "  unreachable: %v\n", r.Err)
					continue
				}
				if strings.Contains(r.Stdout, "hlab:not-found") {
					missing++
					fmt.Fprintf(a.out, "  no container or service called %q here\n", name)
					continue
				}
				for _, line := range splitLines(r.Stdout) {
					if grep != "" && !strings.Contains(strings.ToLower(line), strings.ToLower(grep)) {
						continue
					}
					fmt.Fprintf(a.out, "  %s\n", line)
				}
			}

			if missing == len(hosts) {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&lines, "lines", 50, "how many lines to fetch from each machine")
	f.StringVar(&since, "since", "", "only entries newer than this, e.g. 1h or 2026-07-27")
	f.StringVar(&grep, "grep", "", "only lines containing this, matched case-insensitively")
	return cmd
}

func splitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		out = append(out, strings.TrimRight(l, "\r"))
	}
	// Drop a single trailing blank produced by the final newline.
	if len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}
