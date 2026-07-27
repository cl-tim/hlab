package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/ssh"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/discover"
	"github.com/cl-tim/homelab-cli/internal/enroll"
	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

// enrollOptions are the settings shared by `hlab enroll` and `discover scan --adopt`.
type enrollOptions struct {
	managementUser string
	sudoMode       string
	keyPath        string
	writeConfig    bool
	provisionOnly  bool
}

func (o *enrollOptions) register(f interface {
	StringVar(*string, string, string, string)
	BoolVar(*bool, string, bool, string)
}) {
	f.StringVar(&o.managementUser, "management-user", "hlab", "account to create on each machine for hlab to use")
	f.StringVar(&o.sudoMode, "sudo", string(enroll.SudoNoPassword),
		"privileges for the management account: nopasswd, password, or none")
	f.StringVar(&o.keyPath, "key", "", "hlab's management private key (default: "+config.ManagementKeyPath()+")")
	f.BoolVar(&o.writeConfig, "write-config", true, "record enrolled machines in the config file")
	f.BoolVar(&o.provisionOnly, "provision", false,
		"always create the management account, without asking whether to use existing credentials")
}

func newEnrollCmd(a *app) *cobra.Command {
	opts := &enrollOptions{}

	cmd := &cobra.Command{
		Use:   "enroll [address...]",
		Short: "Bring machines under management interactively",
		Long: "Walks machines one at a time and asks whether each should be managed.\n\n" +
			"With no arguments it works through the unmanaged machines already recorded\n" +
			"by `hlab discover scan`, so there is no need to rescan. Given addresses, it\n" +
			"offers those instead; a range or CIDR block is scanned for SSH first.\n\n" +
			"For each machine you accept it asks for a name, an optional description,\n" +
			"and how hlab should get in: either by creating a dedicated management\n" +
			"account, or by recording credentials you already have (which changes\n" +
			"nothing on the machine). Either way it connects and confirms the access\n" +
			"works before recording anything.\n\n" +
			"Credentials you type are used for that connection only and are never\n" +
			"written anywhere.",
		Example: "  hlab enroll\n" +
			"  hlab enroll 192.0.2.26\n" +
			"  hlab enroll 192.0.2.0/24 --sudo none",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// With no arguments, offer what earlier scans already recorded.
			if len(args) == 0 {
				pending := a.cfg.Inventory.Unmanaged()
				if len(pending) == 0 {
					fmt.Fprintln(a.errOut, "Every machine hlab knows about is already managed.")
					fmt.Fprintln(a.errOut, "Look for more with: hlab discover scan")
					return nil
				}

				candidates := make([]candidate, 0, len(pending))
				for _, h := range pending {
					candidates = append(candidates, candidateFromHost(h))
				}
				fmt.Fprintf(a.errOut, "%d machine(s) discovered but not managed.\n", len(candidates))
				return a.runEnrollment(cmd.Context(), candidates, opts)
			}

			// An explicit host:port is taken at face value, so a machine on a
			// non-standard SSH port can be enrolled directly.
			explicit, remaining := splitExplicitTargets(args)

			if len(remaining) == 0 {
				return a.runEnrollment(cmd.Context(), explicit, opts)
			}

			addrs, err := discover.ExpandTargets(remaining, discover.DefaultMaxTargets)
			if err != nil {
				return err
			}

			candidates := explicit
			if len(addrs) == 1 && len(explicit) == 0 {
				// A single bare address is taken at face value rather than
				// scanned, so a machine that filters probes can still be
				// enrolled.
				candidates = []candidate{{Address: addrs[0].String(), Port: 22}}
			} else {
				fmt.Fprintf(a.errOut, "Scanning %d address(es) for SSH...\n", len(addrs))
				scanned := discover.NewScanner(discover.Options{
					Concurrency:  defaultScanConcurrency,
					ResolveNames: true,
				}).Scan(cmd.Context(), addrs)

				known := buildKnownHosts(cmd.Context(), a)
				discover.Annotate(scanned, known)
				for _, r := range discover.Unmanaged(scanned) {
					candidates = append(candidates, candidateFromScan(r))
				}

				if len(candidates) == 0 {
					fmt.Fprintln(a.errOut, "No unmanaged machines with SSH found.")
					return nil
				}
			}

			return a.runEnrollment(cmd.Context(), candidates, opts)
		},
	}

	opts.register(cmd.Flags())
	return cmd
}

// splitExplicitTargets pulls out arguments written as host:port, which name a
// single machine exactly, from those that describe a range to be scanned.
func splitExplicitTargets(args []string) (explicit []candidate, remaining []string) {
	for _, arg := range args {
		if ap, err := netip.ParseAddrPort(arg); err == nil {
			explicit = append(explicit, candidate{
				Address: ap.Addr().Unmap().String(),
				Port:    int(ap.Port()),
			})
			continue
		}
		remaining = append(remaining, arg)
	}
	return explicit, remaining
}

// buildKnownHosts indexes the current inventory by IP for scan cross-reference.
func buildKnownHosts(ctx context.Context, a *app) discover.KnownHosts {
	addressByName := make(map[string]string)
	for _, h := range a.cfg.Inventory.Hosts() {
		addressByName[h.Name] = h.Address
	}
	return discover.BuildKnownHosts(ctx, addressByName, 2e9)
}

// runEnrollment walks the candidates, asking about each in turn.
func (a *app) runEnrollment(ctx context.Context, candidates []candidate, opts *enrollOptions) error {
	if a.format.Structured() {
		return errors.New("enrolment is interactive; run it without --output json/yaml")
	}

	sudoMode, err := enroll.ParseSudoMode(opts.sudoMode)
	if err != nil {
		return err
	}
	if err := enroll.ValidateUsername(opts.managementUser); err != nil {
		return err
	}

	p := newPrompter(a.in, a.errOut)
	if !p.interactive() {
		return errors.New("enrolment needs a terminal: it asks for credentials that must not be echoed")
	}

	key, err := enroll.LoadOrCreateKey(a.managementKeyPath(opts.keyPath), "hlab management key")
	if err != nil {
		return err
	}
	if key.Created {
		fmt.Fprintf(a.errOut, "Generated hlab's management key at %s\n  %s\n\n", key.PrivatePath, key.Fingerprint)
	}

	if sudoMode == enroll.SudoNoPassword {
		fmt.Fprintf(a.errOut,
			"Note: the %s account will get passwordless sudo, so anyone holding\n"+
				"      %s has root on every enrolled machine. Use --sudo password\n"+
				"      or --sudo none for a narrower grant.\n\n",
			opts.managementUser, key.PrivatePath)
	}

	dialer, err := a.enrollmentDialer(p)
	if err != nil {
		return err
	}
	enroller := enroll.NewEnroller(dialer)

	var enrolled, skipped, failed int

	for i, c := range candidates {
		fmt.Fprintf(a.errOut, "\n[%d/%d] %s\n", i+1, len(candidates), c.describe())

		outcome, action, description, err := a.enrolOne(ctx, p, enroller, c, key, opts, sudoMode)
		if errors.Is(err, ErrPromptAborted) {
			fmt.Fprintln(a.errOut, "\nStopped.")
			break
		}
		if err != nil {
			return err
		}

		switch action {
		case actionSkip:
			skipped++
			continue
		case actionQuit:
			fmt.Fprintln(a.errOut, "Stopped.")
			a.reportEnrollment(enrolled, skipped, failed)
			return nil
		}

		if !outcome.OK() {
			failed++
			fmt.Fprintf(a.errOut, "  failed: %s\n", outcome.Error)
			continue
		}

		enrolled++
		for _, step := range outcome.Steps {
			fmt.Fprintf(a.errOut, "  %s\n", step)
		}

		if opts.writeConfig {
			if err := a.recordEnrolledHost(outcome, key, c, description); err != nil {
				fmt.Fprintf(a.errOut, "  warning: could not update the config: %v\n", err)
				fmt.Fprintf(a.errOut, "  add this entry by hand:\n%s\n", manualEntry(outcome, key))
			} else {
				fmt.Fprintf(a.errOut, "  recorded in %s\n", a.configWritePath())
			}
		} else {
			fmt.Fprintf(a.errOut, "  add this entry to your config:\n%s\n", manualEntry(outcome, key))
		}
	}

	a.reportEnrollment(enrolled, skipped, failed)
	if failed > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

type enrollAction int

const (
	actionEnrol enrollAction = iota
	actionSkip
	actionQuit
)

// enrolOne handles the question-and-answer for a single candidate.
func (a *app) enrolOne(
	ctx context.Context,
	p *prompter,
	enroller *enroll.Enroller,
	c candidate,
	key *enroll.ManagementKey,
	opts *enrollOptions,
	sudoMode enroll.SudoMode,
) (enroll.Outcome, enrollAction, string, error) {
	choice, err := p.Choice("  Bring this machine under management?", []string{"y", "n", "q"}, "n")
	if err != nil {
		return enroll.Outcome{}, actionSkip, "", err
	}
	switch choice {
	case "q":
		return enroll.Outcome{}, actionQuit, "", nil
	case "y":
	default:
		fmt.Fprintln(a.errOut, "  skipped")
		return enroll.Outcome{}, actionSkip, "", nil
	}

	name, err := p.Line("  Inventory name:", c.suggestedName(a.cfg.Inventory))
	if err != nil {
		return enroll.Outcome{}, actionSkip, "", err
	}
	if name == "" {
		fmt.Fprintln(a.errOut, "  no name given; skipped")
		return enroll.Outcome{}, actionSkip, "", nil
	}

	description, err := p.Line("  Description (optional):", c.Description)
	if err != nil {
		return enroll.Outcome{}, actionSkip, "", err
	}

	// Provisioning creates an account and changes the machine. Adopting only
	// records access the operator already has. Offer the choice rather than
	// assuming every machine wants a new account.
	mode := enroll.ModeProvision
	if !opts.provisionOnly {
		access, err := p.Choice(
			fmt.Sprintf("  Create a dedicated %q account, or use credentials you already have? [c/e]",
				opts.managementUser),
			[]string{"c", "e"}, "c")
		if err != nil {
			return enroll.Outcome{}, actionSkip, "", err
		}
		if access == "e" {
			mode = enroll.ModeAdopt
		}
	}

	loginUser, err := p.Line("  SSH username:", "")
	if err != nil {
		return enroll.Outcome{}, actionSkip, "", err
	}
	if loginUser == "" {
		fmt.Fprintln(a.errOut, "  no username given; skipped")
		return enroll.Outcome{}, actionSkip, "", nil
	}

	req := enroll.Request{
		Name:           name,
		Address:        c.Address,
		Port:           c.dialPort(),
		Mode:           mode,
		Credentials:    enroll.Credentials{User: loginUser},
		ManagementUser: opts.managementUser,
		SudoMode:       sudoMode,
		Key:            key,
	}

	if mode == enroll.ModeAdopt {
		identity, err := p.Line("  Private key to use (blank to use your ssh-agent):", "")
		if err != nil {
			return enroll.Outcome{}, actionSkip, "", err
		}
		req.IdentityFile = identity

		if identity == "" {
			password, err := p.Password("  SSH password (blank to use your ssh-agent):")
			if err != nil {
				return enroll.Outcome{}, actionSkip, "", err
			}
			req.Credentials.Password = password
		}
	} else {
		password, err := p.Password("  SSH password (blank to use your ssh-agent or key):")
		if err != nil {
			return enroll.Outcome{}, actionSkip, "", err
		}
		req.Credentials.Password = password
	}

	fmt.Fprintln(a.errOut, "  connecting...")
	return enroller.Enrol(ctx, req), actionEnrol, description, nil
}

// dialPort is the port to connect on, defaulting to 22.
func (c candidate) dialPort() int {
	if c.Port == 0 {
		return 22
	}
	return c.Port
}

// enrollmentDialer builds a dialer that asks before trusting an unknown host
// key. Enrolment sends a password, so an unverified host is a real risk: the
// fingerprint is shown and must be accepted explicitly.
func (a *app) enrollmentDialer(p *prompter) (*sshx.Dialer, error) {
	knownHosts := ""
	if a.cfg != nil {
		knownHosts = a.cfg.Defaults.KnownHostsFile
	}

	return sshx.NewDialer(sshx.Options{
		KnownHostsFile:        knownHosts,
		InsecureIgnoreHostKey: a.insecureHostKey,
		ConnectTimeout:        a.resolveConnectTimeout(),
		OnUnknownHostKey: func(host string, remote net.Addr, pub ssh.PublicKey) (bool, error) {
			fmt.Fprintf(a.errOut,
				"\n  %s is not in %s.\n    key type:    %s\n    fingerprint: %s\n",
				host, knownHosts, pub.Type(), ssh.FingerprintSHA256(pub))
			fmt.Fprintln(a.errOut,
				"  Accept only if this matches the machine you expect; a password is about to be sent to it.")
			return p.Confirm("  Trust this host key?", false)
		},
	})
}

// managementKeyPath resolves where hlab's own key lives.
//
// It always defaults into hlab's home directory, never next to the config file:
// a --config pointing at a repository checkout must not put a root-equivalent
// private key inside it.
func (a *app) managementKeyPath(override string) string {
	if override != "" {
		return override
	}
	return config.ManagementKeyPath()
}

// configWritePath is the config file enrolment records into.
func (a *app) configWritePath() string {
	if a.cfg != nil && a.cfg.Path != "" {
		return a.cfg.Path
	}
	return config.DefaultPath()
}

// recordEnrolledHost adds the machine to the config so later commands use the
// management account and key.
func (a *app) recordEnrolledHost(outcome enroll.Outcome, key *enroll.ManagementKey, c candidate, description string) error {
	host := inventory.Host{
		Name:        outcome.Name,
		Address:     outcome.Address,
		Port:        outcome.Port,
		Managed:     true,
		User:        outcome.User,
		Description: description,
		Hostname:    c.Hostname,
		OS:          c.OS,
		Banner:      c.Banner,
	}

	// An adopted machine authenticates with whatever the operator already had,
	// which may be an agent identity rather than a file.
	if outcome.IdentityFile != "" {
		host.IdentityFile = outcome.IdentityFile
	} else if outcome.Provisioned {
		host.IdentityFile = key.PrivatePath
	}

	if c.OS != "" {
		host.Tags = append(host.Tags, strings.ToLower(c.OS))
	}
	// AddHost upserts on address, so a machine a scan already recorded is
	// promoted in place rather than duplicated.
	return config.AddHost(a.configWritePath(), host)
}

// manualEntry renders the config snippet for a machine hlab could not record.
func manualEntry(outcome enroll.Outcome, key *enroll.ManagementKey) string {
	var b strings.Builder
	fmt.Fprintf(&b, "    - name: %s\n", outcome.Name)
	fmt.Fprintf(&b, "      address: %s\n", outcome.Address)
	if outcome.Port != 0 && outcome.Port != 22 {
		fmt.Fprintf(&b, "      port: %d\n", outcome.Port)
	}
	fmt.Fprintf(&b, "      user: %s\n", outcome.User)
	fmt.Fprintf(&b, "      identityFile: %s\n", key.PrivatePath)
	fmt.Fprint(&b, "      tags: [managed]")
	return b.String()
}

func (a *app) reportEnrollment(enrolled, skipped, failed int) {
	fmt.Fprintf(a.out, "\n%d enrolled, %d skipped, %d failed\n", enrolled, skipped, failed)
	if enrolled > 0 {
		fmt.Fprintln(a.errOut, "Check them with: hlab ping '#managed'")
	}
}
