package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/enroll"
	"github.com/cl-tim/homelab-cli/internal/facts"
	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

// unenrollView is the structured form of what unenroll did.
type unenrollView struct {
	Host           string   `json:"host" yaml:"host"`
	Revoked        bool     `json:"revoked" yaml:"revoked"`
	KeyRemovedFrom []string `json:"keyRemovedFrom,omitempty" yaml:"keyRemovedFrom,omitempty"`
	SudoRemoved    bool     `json:"sudoRemoved" yaml:"sudoRemoved"`
	AccountRemoval string   `json:"accountRemoval,omitempty" yaml:"accountRemoval,omitempty"`
	// Verified is "refused" when hlab's key was proved not to log in,
	// "skipped" when there was nothing to test, "inconclusive" otherwise.
	Verified string   `json:"verified" yaml:"verified"`
	Record   string   `json:"record" yaml:"record"`
	Groups   []string `json:"removedFromGroups,omitempty" yaml:"removedFromGroups,omitempty"`
}

func newUnenrollCmd(a *app) *cobra.Command {
	var (
		apply         bool
		forget        bool
		removeAccount bool
		offline       bool
	)

	cmd := &cobra.Command{
		Use:   "unenroll <host>",
		Short: "Take a machine out of management and revoke hlab's access to it",
		Long: "Removes hlab's key from every account on the machine and deletes the\n" +
			"passwordless sudo rule enrolment installed, then proves the key no longer\n" +
			"logs in. Only then is the inventory changed: the record is kept as an\n" +
			"unmanaged device, or deleted entirely with --forget.\n\n" +
			"Editing the config alone is not enough. The key stays authorised and\n" +
			"remains root on that machine — which matters most for a machine that is\n" +
			"leaving your hands.\n\n" +
			"--remove-account also deletes the management account, but only one hlab\n" +
			"created. On an adopted machine the account is your own and is refused.\n" +
			"Deletion happens after the session ends and so cannot be confirmed.\n\n" +
			"--offline is for a machine that is already gone: the inventory is updated\n" +
			"and nothing is revoked, because nothing can be.\n\n" +
			"Services the machine runs, and everything under /opt/hlab, are left alone.\n" +
			"Nothing changes without --apply.",
		Example: "  hlab unenroll pi-3\n" +
			"  hlab unenroll pi-3 --apply\n" +
			"  hlab unenroll pi-3 --forget --remove-account --apply\n" +
			"  hlab unenroll dead-pi --offline --forget --apply",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if offline && removeAccount {
				return fmt.Errorf("--remove-account needs the machine; it cannot be combined with --offline")
			}

			host, err := a.oneManagedHost(args)
			if err != nil {
				return err
			}

			key, err := enroll.LoadExistingKey(config.ManagementKeyPath())
			switch {
			case errors.Is(err, os.ErrNotExist):
				// No key here means this workstation never installed one.
				key = nil
			case err != nil:
				return err
			}

			if !a.format.Structured() {
				a.describeUnenroll(host, key, forget, removeAccount, offline)
			}
			if !apply {
				fmt.Fprintln(a.out, "\nNot applied. Re-run with --apply.")
				return nil
			}

			view := unenrollView{Host: host.Name, Verified: "skipped"}
			inconclusive := false

			if !offline {
				if err := a.revokeAccess(cmd.Context(), host, key, removeAccount, &view); err != nil {
					return err
				}
				inconclusive = view.Verified == "inconclusive"
			}

			path := a.configWritePath()
			if forget {
				if err := config.RemoveHosts(path, []string{host.Name}); err != nil {
					return err
				}
				view.Record = "removed"
			} else {
				groups, err := config.UnmanageHost(path, host.Name)
				if err != nil {
					return err
				}
				view.Record = "unmanaged"
				view.Groups = groups
			}
			// Nothing will refresh these again, so they would only go stale.
			if err := facts.RemoveFromStore(config.FactsPath(), host.Name); err != nil {
				fmt.Fprintf(a.errOut, "warning: could not clear facts for %s: %v\n", host.Name, err)
			}

			if a.format.Structured() {
				tbl := output.Table{Headers: []string{"HOST", "RECORD", "VERIFIED"}}
				tbl.AddRow(view.Host, view.Record, view.Verified)
				if err := output.Write(a.out, a.format, view, tbl); err != nil {
					return err
				}
				if inconclusive {
					return &ExitError{Code: 1}
				}
				return nil
			}
			a.reportUnenroll(host, view, offline)
			if inconclusive {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&apply, "apply", false, "actually unenroll; without it nothing is changed")
	f.BoolVar(&forget, "forget", false, "delete the record instead of keeping it as an unmanaged device")
	f.BoolVar(&removeAccount, "remove-account", false,
		"also delete the management account (refused unless hlab created it)")
	f.BoolVar(&offline, "offline", false,
		"the machine is gone: update the inventory only, revoking nothing")
	return cmd
}

// revokeAccess runs the revocation on the machine and proves the result.
//
// The record is only changed after this returns nil, so a machine hlab could
// not revoke stays managed rather than becoming an unmanaged record that still
// trusts the key.
func (a *app) revokeAccess(
	ctx context.Context, host inventory.Host, key *enroll.ManagementKey, removeAccount bool, view *unenrollView,
) error {
	opts := enroll.RevokeOptions{User: host.User, RemoveAccount: removeAccount}
	if key != nil {
		blob, err := enroll.KeyBlob(key.AuthorizedKey)
		if err != nil {
			return err
		}
		opts.KeyBlob = blob
	}
	script, err := enroll.BuildRevokeScript(opts)
	if err != nil {
		return err
	}

	dialer, err := a.newDialer()
	if err != nil {
		return err
	}
	res, err := runPrivilegedOn(ctx, dialer, targetFor(host), script)
	if err != nil {
		return fmt.Errorf("could not reach %s to revoke access: %w\n"+
			"Nothing was changed. If the machine is gone for good, --offline updates the inventory only",
			host.Name, err)
	}

	rev := enroll.ParseRevocation(res.Stdout)
	if !rev.Complete {
		msg := strings.TrimPrefix(firstNonEmptyLine(res.Stderr), "hlab:error ")
		if msg == "" {
			msg = fmt.Sprintf("the revocation did not finish (exit %d)", res.ExitCode)
		}
		return fmt.Errorf("%s: %s\nThe inventory was not changed", host.Name, msg)
	}

	view.Revoked = true
	view.KeyRemovedFrom = rev.KeyFiles
	view.SudoRemoved = rev.SudoRemoved
	if rev.AccountRemoving {
		view.AccountRemoval = "scheduled"
	}

	if key == nil {
		return nil
	}

	// Proof, not assertion: hlab's key, offered alone, must now be refused.
	probe := targetFor(host)
	probe.IdentityFile = key.PrivatePath
	client, err := dialer.DialKeyOnly(ctx, probe)
	switch {
	case err == nil:
		_ = client.Close()
		return fmt.Errorf(
			"%s: hlab's key still logs in as %s after revocation, so the machine is left managed.\n"+
				"Look for the key in an authorized_keys file outside the account homes, or an AuthorizedKeysFile setting",
			host.Name, host.User)
	case sshx.AuthRefused(err):
		view.Verified = "refused"
	default:
		view.Verified = "inconclusive"
		fmt.Fprintf(a.errOut, "warning: could not confirm the key is refused: %v\n", err)
	}
	return nil
}

func (a *app) describeUnenroll(
	host inventory.Host, key *enroll.ManagementKey, forget, removeAccount, offline bool,
) {
	fmt.Fprintf(a.out, "Unenroll %s (%s, logged in as %s)\n", host.Name, host.Address, host.User)
	switch {
	case offline:
		fmt.Fprintln(a.out, "  offline: nothing on the machine is touched")
	default:
		if key != nil {
			fmt.Fprintf(a.out, "  remove hlab's key (%s) from every account\n", key.Fingerprint)
		} else {
			fmt.Fprintln(a.out, "  no management key on this workstation, so none to remove")
		}
		fmt.Fprintf(a.out, "  remove %s\n", enroll.SudoersFragment)
		if removeAccount {
			fmt.Fprintf(a.out, "  delete the %s account and its home directory\n", host.User)
		} else {
			fmt.Fprintf(a.out, "  keep the %s account\n", host.User)
		}
		if key != nil {
			fmt.Fprintln(a.out, "  then prove the key is refused")
		}
	}
	if forget {
		fmt.Fprintln(a.out, "  delete the record")
	} else {
		fmt.Fprintln(a.out, "  keep the record as an unmanaged device")
	}
	fmt.Fprintln(a.out, "  services it runs and /opt/hlab are left as they are")
}

func (a *app) reportUnenroll(host inventory.Host, view unenrollView, offline bool) {
	fmt.Fprintln(a.out)
	if offline {
		fmt.Fprintf(a.out, "%s was not contacted. hlab's key is still authorised on it and\n"+
			"remains root there; if the machine turns up again, wipe it before reuse.\n", host.Name)
	} else {
		for _, f := range view.KeyRemovedFrom {
			fmt.Fprintf(a.out, "  removed hlab's key from %s\n", f)
		}
		if len(view.KeyRemovedFrom) == 0 {
			fmt.Fprintln(a.out, "  hlab's key was not authorised anywhere")
		}
		if view.SudoRemoved {
			fmt.Fprintf(a.out, "  removed %s\n", enroll.SudoersFragment)
		} else {
			fmt.Fprintf(a.out, "  no %s to remove\n", enroll.SudoersFragment)
		}
		if view.AccountRemoval != "" {
			fmt.Fprintf(a.out, "  %s will be deleted once this session closes (cannot be confirmed:\n"+
				"  hlab no longer has a way in to look)\n", host.User)
		}
		switch view.Verified {
		case "refused":
			fmt.Fprintf(a.out, "  verified: hlab's key is refused for %s\n", host.User)
		case "inconclusive":
			fmt.Fprintln(a.out, "  NOT verified: the machine did not answer the check")
		}
	}

	switch view.Record {
	case "removed":
		fmt.Fprintf(a.out, "\n%s removed from %s\n", host.Name, a.configWritePath())
	default:
		fmt.Fprintf(a.out, "\n%s is now an unmanaged record in %s\n", host.Name, a.configWritePath())
		if len(view.Groups) > 0 {
			fmt.Fprintf(a.out, "  removed from groups: %s\n", strings.Join(view.Groups, ", "))
		}
	}
}
