package cli

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/secrets"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

func newSecretCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage the credentials a stack reads at runtime",
		Long: "Credentials live beside the stack that uses them, at 0600, and are\n" +
			"referenced from the compose file rather than embedded in it. Compose files\n" +
			"are reviewed, committed and shared; the things they authenticate with must\n" +
			"not be.\n\n" +
			"A generated secret is created on the machine that will use it and never\n" +
			"crosses the network, so it cannot be intercepted or left in a terminal's\n" +
			"scrollback. `hlab backup` picks these up with the rest of the stack.",
	}
	cmd.AddCommand(
		newSecretListCmd(a),
		newSecretGenCmd(a),
		newSecretSetCmd(a),
		newSecretShowCmd(a),
	)
	return cmd
}

func newSecretListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list [selector]",
		Short:   "List the secrets each machine holds, without their values",
		Example: "  hlab secret list\n  hlab secret list apps-1",
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

			tbl := output.Table{Headers: []string{"HOST", "STACK", "FILE", "MODE", "BYTES"}}
			var found int
			for _, r := range exec.Run(cmd.Context(), targets, secrets.ListScript) {
				if r.Err != nil {
					tbl.AddRow(r.Host, "-", "unreachable: "+firstNonEmptyLine(r.Err.Error()), "-", "-")
					continue
				}
				for _, line := range splitLines(r.Stdout) {
					f := strings.Split(strings.TrimSpace(line), "|")
					if len(f) != 4 || f[1] == "" {
						continue
					}
					found++
					mode := f[2]
					// A credential readable by anyone on the machine is worth
					// pointing at rather than listing quietly.
					if mode != "600" && mode != "400" {
						mode += " (!)"
					}
					tbl.AddRow(r.Host, f[0], f[1], mode, f[3])
				}
			}

			if err := output.WriteTable(a.out, tbl); err != nil {
				return err
			}
			if found == 0 {
				fmt.Fprintln(a.out, "\nNo secrets found.")
			} else {
				fmt.Fprintf(a.errOut,
					"\nValues are never listed. `hlab secret show` prints one, deliberately explicitly.\n")
			}
			return nil
		},
	}
}

func newSecretGenCmd(a *app) *cobra.Command {
	var (
		length int
		key    string
		force  bool
	)

	cmd := &cobra.Command{
		Use:   "gen <host> <stack> <file>",
		Short: "Generate a secret on the machine that will use it",
		Long: "Creates a random value on the target machine and writes it beside the\n" +
			"stack at 0600. The value is generated there and never transmitted, which\n" +
			"is the point: a secret that never crosses the network cannot be captured\n" +
			"from it.\n\n" +
			"With --env the value becomes one KEY=value line in an env file, leaving\n" +
			"any other keys in that file alone — the shape compose's env_file wants.\n" +
			"Without it the file's whole content is the secret, which is what the\n" +
			"_FILE convention wants.",
		Example: "  hlab secret gen apps-1 technitium config/admin-password\n" +
			"  hlab secret gen apps-1 homepage homepage.env --env API_KEY",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args[:1])
			if err != nil {
				return err
			}

			script, err := secrets.BuildGenerateScript(secrets.Options{
				Stack: args[1], File: args[2], Key: key, Length: length, Force: force,
			})
			if err != nil {
				return err
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), script)
			if err != nil {
				return err
			}
			if !strings.Contains(res.Stdout, secrets.MarkerWritten) {
				return fmt.Errorf("%s",
					strings.TrimPrefix(orDash(firstNonEmptyLine(res.Stderr)), "hlab:error "))
			}

			fmt.Fprintf(a.out, "Generated %s/%s on %s\n", args[1], args[2], host.Name)
			fmt.Fprintf(a.errOut,
				"The value was created on the machine and not transmitted.\n"+
					"Read it with: hlab secret show %s %s %s\n", host.Name, args[1], args[2])
			return nil
		},
	}

	f := cmd.Flags()
	f.IntVar(&length, "length", secrets.DefaultLength, "how many characters to generate")
	f.StringVar(&key, "env", "", "write as KEY=value in an env file rather than a whole-file secret")
	f.BoolVar(&force, "force", false, "replace an existing secret")
	return cmd
}

func newSecretSetCmd(a *app) *cobra.Command {
	var key string

	cmd := &cobra.Command{
		Use:   "set <host> <stack> <file>",
		Short: "Store a value you already have",
		Long: "Prompts for a value and writes it beside the stack at 0600.\n\n" +
			"For a credential issued by somebody else — an API key, a licence — where\n" +
			"generating one is not an option. The value is read without echo and sent\n" +
			"as the command's input, so it appears neither in your shell history nor in\n" +
			"the machine's process list.",
		Example: "  hlab secret set media-1 media plex.env --env PLEX_CLAIM",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args[:1])
			if err != nil {
				return err
			}

			p := newPrompter(a.in, a.errOut)
			if !p.interactive() {
				return fmt.Errorf(
					"reading a secret needs a terminal, so the value is not echoed.\n" +
						"Use `hlab secret gen` for a generated one")
			}
			value, err := p.Password(fmt.Sprintf("Value for %s/%s: ", args[1], args[2]))
			if err != nil {
				return err
			}
			if value == "" {
				return fmt.Errorf("no value given; nothing was written")
			}

			script, err := secrets.BuildSetScript(secrets.Options{
				Stack: args[1], File: args[2], Key: key,
			})
			if err != nil {
				return err
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			client, err := dialer.Dial(cmd.Context(), targetFor(host))
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()

			// The value is staged by its own invocation, carrying nothing else.
			// It must not travel on the same stream as the script: sudo hands
			// the whole stream to the shell, which reads the value as another
			// line of script and tries to run it.
			staged, err := sshx.Exec(cmd.Context(), client, secrets.StageCommand,
				bytes.NewReader([]byte(value)))
			if err != nil {
				return err
			}
			if !staged.OK() {
				return fmt.Errorf("could not stage the value: %s",
					orDash(firstNonEmptyLine(staged.Stderr)))
			}

			res, err := sshx.Exec(cmd.Context(), client, "sudo -n sh -s",
				bytes.NewReader([]byte(script)))
			if err != nil {
				return err
			}
			if !strings.Contains(res.Stdout, secrets.MarkerWritten) {
				return fmt.Errorf("%s",
					strings.TrimPrefix(orDash(firstNonEmptyLine(res.Stderr)), "hlab:error "))
			}

			fmt.Fprintf(a.out, "Stored %s/%s on %s\n", args[1], args[2], host.Name)
			return nil
		},
	}

	cmd.Flags().StringVar(&key, "env", "", "write as KEY=value in an env file")
	return cmd
}

func newSecretShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "show <host> <stack> <file>",
		Short:   "Print one secret's value",
		Long:    "Prints the value to stdout. Separate from `list` on purpose: seeing a\nsecret should be something you asked for, not something that happens.",
		Example: "  hlab secret show apps-1 technitium config/admin-password",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args[:1])
			if err != nil {
				return err
			}

			script, err := secrets.BuildShowScript(args[1], args[2])
			if err != nil {
				return err
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), script)
			if err != nil {
				return err
			}
			if strings.Contains(res.Stdout, secrets.MarkerMissing) {
				return fmt.Errorf("%s has no secret %s/%s", host.Name, args[1], args[2])
			}

			fmt.Fprintln(a.out, strings.TrimRight(res.Stdout, "\r\n"))
			return nil
		},
	}
}
