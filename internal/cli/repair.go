package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/enroll"
	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/output"
)

// repairResult is one machine's outcome, for structured output.
type repairResult struct {
	Host           string   `json:"host" yaml:"host"`
	Address        string   `json:"address" yaml:"address"`
	User           string   `json:"user" yaml:"user"`
	AccountCreated bool     `json:"accountCreated" yaml:"accountCreated"`
	SudoConfigured bool     `json:"sudoConfigured" yaml:"sudoConfigured"`
	KeyInstalled   bool     `json:"keyInstalled" yaml:"keyInstalled"`
	Verified       bool     `json:"verified" yaml:"verified"`
	Steps          []string `json:"steps,omitempty" yaml:"steps,omitempty"`
	Error          string   `json:"error,omitempty" yaml:"error,omitempty"`
}

func (r repairResult) ok() bool { return r.Error == "" && r.Verified }

// changed reports whether anything actually had to be put back.
func (r repairResult) changed() bool {
	return r.AccountCreated || r.KeyInstalled
}

func newRepairCmd(a *app) *cobra.Command {
	var (
		managementUser string
		sudoMode       string
		keyPath        string
		asUser         string
	)

	cmd := &cobra.Command{
		Use:   "repair [selector]",
		Short: "Re-assert hlab's management account on the hosts it manages",
		Long: "Pushes what hlab currently expects onto each managed machine: the\n" +
			"management account, its sudoers fragment, and its authorised key.\n\n" +
			"Use it after a machine has been hand-edited, or after upgrading hlab, to\n" +
			"bring hosts back into line. It is idempotent — a machine already in the\n" +
			"expected state is reported as such and left alone — and it runs without\n" +
			"prompting, so it can be scheduled.\n\n" +
			"By default it connects as the management account itself, which means it\n" +
			"cannot rescue a host whose key or sudo is already broken — that account is\n" +
			"locked out too. Pass --as <admin-user> to come in through an\n" +
			"administrative account instead and rebuild the management account from\n" +
			"the outside; that asks for a password, so it needs a terminal.\n\n" +
			"With no selector it covers every managed machine.",
		Example: "  hlab repair\n" +
			"  hlab repair docker\n" +
			"  hlab repair '#debian' --sudo password\n" +
			"  hlab repair pi-dns --as tim        # rescue a host hlab cannot reach",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := enroll.ParseSudoMode(sudoMode)
			if err != nil {
				return err
			}
			if err := enroll.ValidateUsername(managementUser); err != nil {
				return err
			}

			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}

			key, err := enroll.LoadOrCreateKey(a.managementKeyPath(keyPath), "hlab management key")
			if err != nil {
				return err
			}
			if key.Created {
				return fmt.Errorf(
					"no management key existed, so one was just generated (%s).\n"+
						"Nothing is enrolled with it yet — use `hlab enroll` rather than repair",
					key.PrivatePath)
			}

			// Repairing as the management account cannot fix a host whose sudo
			// or key is already broken — it is locked out too. --as connects
			// with an administrative account instead, which is the only way
			// back in.
			var rescuePassword string
			if asUser != "" {
				p := newPrompter(a.in, a.errOut)
				if !p.interactive() {
					return fmt.Errorf("--as needs a terminal: it asks for a password that must not be echoed")
				}
				rescuePassword, err = p.Password(
					fmt.Sprintf("Password for %s (blank to use your ssh-agent or key):", asUser))
				if err != nil {
					return err
				}
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			enroller := enroll.NewEnroller(dialer)

			if !a.format.Structured() {
				fmt.Fprintf(a.errOut, "Repairing %d host(s)...\n", len(hosts))
			}

			results := make([]repairResult, 0, len(hosts))
			for _, h := range hosts {
				user := h.User
				if user == "" {
					user = managementUser
				}

				req := enroll.Request{
					Name:           h.Name,
					Address:        h.Address,
					Port:           h.Port,
					ManagementUser: user,
					SudoMode:       mode,
					Key:            key,
					IdentityFile:   h.IdentityFile,
				}

				var outcome enroll.Outcome
				if asUser != "" {
					// Rescue: come in as an administrator and rebuild the
					// management account from the outside.
					req.Mode = enroll.ModeProvision
					req.Credentials = enroll.Credentials{User: asUser, Password: rescuePassword}
					req.IdentityFile = ""
					outcome = enroller.Enrol(cmd.Context(), req)
				} else {
					outcome = enroller.Repair(cmd.Context(), req)
				}

				results = append(results, repairResult{
					Host:           h.Name,
					Address:        h.Address,
					User:           user,
					AccountCreated: outcome.UserCreated,
					SudoConfigured: outcome.SudoConfigured,
					KeyInstalled:   outcome.KeyInstalled,
					Verified:       outcome.Verified,
					Steps:          outcome.Steps,
					Error:          outcome.Error,
				})
			}

			return a.renderRepair(results)
		},
	}

	f := cmd.Flags()
	f.StringVar(&managementUser, "management-user", "hlab",
		"account to re-assert, for hosts whose record does not name one")
	f.StringVar(&sudoMode, "sudo", string(enroll.SudoNoPassword),
		"privileges to re-assert: nopasswd, password, or none")
	f.StringVar(&keyPath, "key", "", "hlab's management private key")
	f.StringVar(&asUser, "as", "",
		"connect as this administrative account instead of the management account, to rescue a host it can no longer reach")

	return cmd
}

// renderRepair reports what was put back on each machine.
func (a *app) renderRepair(results []repairResult) error {
	if a.format.Structured() {
		if err := output.Write(a.out, a.format, results, output.Table{}); err != nil {
			return err
		}
		return repairExitCode(results)
	}

	tbl := output.Table{Headers: []string{"HOST", "ACCOUNT", "SUDO", "KEY", "RESULT"}}
	var repaired, failed int

	for _, r := range results {
		if !r.ok() {
			failed++
			tbl.AddRow(r.Host, "-", "-", "-", "failed")
			continue
		}

		account := "present"
		if r.AccountCreated {
			account = "recreated"
		}
		sudo := "unchanged"
		if r.SudoConfigured {
			sudo = "reasserted"
		}
		keyState := "present"
		if r.KeyInstalled {
			keyState = "reinstalled"
		}

		result := "ok"
		if r.changed() {
			result = "repaired"
			repaired++
		}
		tbl.AddRow(r.Host, account, sudo, keyState, result)
	}

	fmt.Fprintln(a.out)
	if err := output.WriteTable(a.out, tbl); err != nil {
		return err
	}

	for _, r := range results {
		if r.Error != "" {
			fmt.Fprintf(a.errOut, "\n%s:\n  %s\n", r.Host, strings.ReplaceAll(r.Error, "\n", "\n  "))
		}
	}

	fmt.Fprintf(a.out, "\n%d/%d ok, %d needed repair\n", len(results)-failed, len(results), repaired)
	return repairExitCode(results)
}

func repairExitCode(results []repairResult) error {
	for _, r := range results {
		if !r.ok() {
			return &ExitError{Code: 1}
		}
	}
	return nil
}

// managedHosts resolves an optional selector to managed host records,
// defaulting to the whole managed fleet.
func (a *app) managedHosts(args []string) ([]inventory.Host, error) {
	if a.cfg == nil {
		return nil, fmt.Errorf("no configuration loaded")
	}

	if len(args) > 0 {
		hosts, _, err := a.selectTargets(args[0])
		return hosts, err
	}

	managed := a.cfg.Inventory.Managed()
	if len(managed) == 0 {
		return nil, fmt.Errorf(
			"no machines are under management yet; enrol them with `hlab enroll`")
	}
	return managed, nil
}
