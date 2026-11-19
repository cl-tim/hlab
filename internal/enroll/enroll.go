package enroll

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/cl-tim/hlab/internal/sshx"
)

// Credentials are what the operator supplies to reach a machine for the first
// time. They are used for the enrolling connection only and are never stored.
type Credentials struct {
	User string
	// Password is empty when the operator wants to use an existing key or
	// ssh-agent identity instead.
	Password string
}

// Mode selects how a machine is brought under management.
type Mode string

const (
	// ModeProvision creates a dedicated account and installs hlab's key.
	ModeProvision Mode = "provision"
	// ModeAdopt changes nothing on the machine: it records credentials the
	// operator already has, after confirming they work. For machines that are
	// already keyed up, or that must not be modified.
	ModeAdopt Mode = "adopt"
)

// Request describes one machine to enrol.
type Request struct {
	// Name is the inventory name to record the machine under.
	Name    string
	Address string
	Port    int

	Mode        Mode
	Credentials Credentials

	// ManagementUser is the account to create, for ModeProvision.
	ManagementUser string
	SudoMode       SudoMode
	Key            *ManagementKey

	// IdentityFile is the key to authenticate with under ModeAdopt. Empty
	// means rely on the ssh-agent.
	IdentityFile string
}

// Outcome records what enrolment did, step by step, so the operator can see
// exactly what changed on the machine.
type Outcome struct {
	Name    string `json:"name" yaml:"name"`
	Address string `json:"address" yaml:"address"`
	Port    int    `json:"port" yaml:"port"`
	User    string `json:"user" yaml:"user"`

	UserCreated    bool `json:"userCreated" yaml:"userCreated"`
	SudoConfigured bool `json:"sudoConfigured" yaml:"sudoConfigured"`
	KeyInstalled   bool `json:"keyInstalled" yaml:"keyInstalled"`
	Verified       bool `json:"verified" yaml:"verified"`
	// SudoVerified reports whether the new account could actually escalate.
	SudoVerified bool `json:"sudoVerified" yaml:"sudoVerified"`
	// Provisioned reports whether hlab created the account, as opposed to
	// adopting credentials that already existed.
	Provisioned bool `json:"provisioned" yaml:"provisioned"`
	// IdentityFile is the key an adopted machine authenticates with, when the
	// operator named one.
	IdentityFile string `json:"identityFile,omitempty" yaml:"identityFile,omitempty"`

	// Steps is a human-readable trace of what happened.
	Steps []string `json:"steps,omitempty" yaml:"steps,omitempty"`
	Error string   `json:"error,omitempty" yaml:"error,omitempty"`
}

// OK reports whether the machine ended up under management.
func (o Outcome) OK() bool { return o.Error == "" && o.Verified }

// Enroller performs enrolment using a dialer configured with whatever host key
// policy the caller wants (interactive trust-on-first-use, typically).
type Enroller struct {
	dialer *sshx.Dialer
}

// NewEnroller returns an Enroller that connects through the given dialer.
func NewEnroller(dialer *sshx.Dialer) *Enroller {
	return &Enroller{dialer: dialer}
}

// Enrol brings one machine under management and verifies the result.
func (e *Enroller) Enrol(ctx context.Context, req Request) Outcome {
	if req.Mode == ModeAdopt {
		return e.adopt(ctx, req)
	}

	out := Outcome{
		Name:    req.Name,
		Address: req.Address,
		Port:    req.Port,
		User:    req.ManagementUser,
	}

	script, err := BuildBootstrapScript(req.ManagementUser, req.Key.AuthorizedKey, req.SudoMode)
	if err != nil {
		out.Error = err.Error()
		return out
	}

	target := sshx.Target{
		Name:         req.Name,
		Address:      req.Address,
		Port:         req.Port,
		User:         req.Credentials.User,
		IdentityFile: req.IdentityFile,
	}

	// 1. Connect with the operator's credential.
	var client *ssh.Client
	if req.Credentials.Password != "" {
		client, err = e.dialer.DialPassword(ctx, target, req.Credentials.Password)
	} else {
		client, err = e.dialer.Dial(ctx, target)
	}
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer func() { _ = client.Close() }()
	out.Steps = append(out.Steps, fmt.Sprintf("connected as %s", req.Credentials.User))

	// 2. Run the bootstrap script as root.
	result, err := e.runPrivileged(ctx, client, script, req.Credentials.Password)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	if !strings.Contains(result.Stdout, markerDone) {
		out.Error = bootstrapFailure(result)
		return out
	}

	out.Provisioned = true
	applyMarkers(&out, result.Stdout)

	// 3. Reconnect as the new account with hlab's own key and confirm it works.
	if err := e.verify(ctx, req, &out); err != nil {
		out.Error = err.Error()
		return out
	}

	return out
}

// Repair re-asserts the management account's configuration on a host hlab can
// already reach: the account itself, its sudoers fragment, and its authorised
// key. It is the convergence path — it pushes what hlab currently expects onto
// a machine that may have drifted, or that was set up by an older version.
//
// It connects as the management account, so it cannot rescue a host whose key
// or sudo is already broken. That case needs re-enrolment with an
// administrative credential, and the error says so.
func (e *Enroller) Repair(ctx context.Context, req Request) Outcome {
	req.Mode = ModeProvision
	req.Credentials = Credentials{User: req.ManagementUser}
	if req.IdentityFile == "" && req.Key != nil {
		req.IdentityFile = req.Key.PrivatePath
	}

	out := e.Enrol(ctx, req)
	if out.Error != "" {
		out.Error = fmt.Sprintf(
			"%s\nThe %s account is locked out, so repair cannot fix it from the inside. "+
				"Come in through an administrative account instead:\n"+
				"  hlab repair %s --as <your-admin-user>",
			out.Error, req.ManagementUser, req.Name)
	}
	return out
}

// adopt records credentials the operator already has, without touching the
// machine. It still connects and confirms those credentials work, so a machine
// is never recorded as manageable on the strength of an assertion alone.
func (e *Enroller) adopt(ctx context.Context, req Request) Outcome {
	out := Outcome{
		Name:    req.Name,
		Address: req.Address,
		Port:    req.Port,
		User:    req.Credentials.User,
	}

	target := sshx.Target{
		Name:         req.Name,
		Address:      req.Address,
		Port:         req.Port,
		User:         req.Credentials.User,
		IdentityFile: req.IdentityFile,
	}

	var (
		client *ssh.Client
		err    error
	)
	if req.Credentials.Password != "" {
		client, err = e.dialer.DialPassword(ctx, target, req.Credentials.Password)
	} else {
		client, err = e.dialer.Dial(ctx, target)
	}
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer func() { _ = client.Close() }()

	who, err := sshx.Exec(ctx, client, "id -un", nil)
	if err != nil {
		out.Error = fmt.Errorf("verify login: %w", err).Error()
		return out
	}
	if got := strings.TrimSpace(who.Stdout); got != req.Credentials.User {
		out.Error = fmt.Sprintf("connected as %q, expected %q", got, req.Credentials.User)
		return out
	}

	out.Verified = true
	out.IdentityFile = req.IdentityFile
	out.Steps = append(out.Steps, fmt.Sprintf("verified login as %s (nothing changed on the machine)", out.User))

	// Report whether privileged commands will work, without requiring them.
	if res, err := sshx.Exec(ctx, client, "sudo -n true", nil); err == nil && res.OK() {
		out.SudoVerified = true
		out.Steps = append(out.Steps, "verified passwordless sudo")
	} else {
		out.Steps = append(out.Steps, "note: passwordless sudo is not available for this account")
	}

	return out
}

// runPrivileged executes the script as root. When the login is already root it
// runs directly; otherwise it goes through sudo.
//
// sudo -S reads the password from the first line of stdin and passes the rest
// through, so the password and the script travel in one stream and the password
// never appears in the command line or in the process list.
func (e *Enroller) runPrivileged(ctx context.Context, client *ssh.Client, script, password string) (sshx.ExecResult, error) {
	idRes, err := sshx.Exec(ctx, client, "id -u", nil)
	if err != nil {
		return sshx.ExecResult{}, fmt.Errorf("determine remote user: %w", err)
	}

	if strings.TrimSpace(idRes.Stdout) == "0" {
		return sshx.Exec(ctx, client, "sh -s", strings.NewReader(script))
	}

	// With no password to offer — a key or agent login, which is how repair
	// connects — require sudo to be passwordless rather than feeding it a
	// blank line and waiting for a prompt that will never be answered.
	if password == "" {
		res, err := sshx.Exec(ctx, client, "sudo -n sh -s", strings.NewReader(script))
		if err != nil {
			return res, err
		}
		if res.ExitCode != 0 && looksLikeSudoRefusal(res.Stderr) {
			return res, fmt.Errorf(
				"passwordless sudo is not available for this account: %s", firstLine(res.Stderr))
		}
		return res, nil
	}

	stdin := strings.NewReader(password + "\n" + script)
	res, err := sshx.Exec(ctx, client, "sudo -S -p '' sh -s", stdin)
	if err != nil {
		return res, err
	}

	if res.ExitCode != 0 && looksLikeSudoRefusal(res.Stderr) {
		return res, fmt.Errorf(
			"sudo refused for %s: %s\nEnrolment needs an account that can escalate to root",
			"the supplied account", firstLine(res.Stderr))
	}
	return res, nil
}

// verify reconnects as the management account using hlab's key, proving the
// machine is genuinely manageable rather than merely reporting success.
func (e *Enroller) verify(ctx context.Context, req Request, out *Outcome) error {
	target := sshx.Target{
		Name:         req.Name,
		Address:      req.Address,
		Port:         req.Port,
		User:         req.ManagementUser,
		IdentityFile: req.Key.PrivatePath,
	}

	client, err := e.dialer.Dial(ctx, target)
	if err != nil {
		return fmt.Errorf("the account was provisioned but hlab could not log in as %s: %w", req.ManagementUser, err)
	}
	defer func() { _ = client.Close() }()

	who, err := sshx.Exec(ctx, client, "id -un", nil)
	if err != nil {
		return fmt.Errorf("verify login: %w", err)
	}
	if got := strings.TrimSpace(who.Stdout); got != req.ManagementUser {
		return fmt.Errorf("verify login: connected as %q, expected %q", got, req.ManagementUser)
	}
	out.Verified = true
	out.Steps = append(out.Steps, fmt.Sprintf("verified login as %s with hlab's key", req.ManagementUser))

	// Only passwordless sudo can be proven without prompting.
	if req.SudoMode == SudoNoPassword {
		res, err := sshx.Exec(ctx, client, "sudo -n true", nil)
		if err == nil && res.OK() {
			out.SudoVerified = true
			out.Steps = append(out.Steps, "verified passwordless sudo")
		} else {
			out.Steps = append(out.Steps, "warning: passwordless sudo did not work")
		}
	}

	return nil
}

// applyMarkers reads the script's progress markers into the outcome.
func applyMarkers(out *Outcome, stdout string) {
	switch {
	case strings.Contains(stdout, markerUserCreated):
		out.UserCreated = true
		out.Steps = append(out.Steps, fmt.Sprintf("created account %s", out.User))
	case strings.Contains(stdout, markerUserExists):
		out.Steps = append(out.Steps, fmt.Sprintf("account %s already existed", out.User))
	}

	switch {
	case strings.Contains(stdout, markerSudoConfigured):
		out.SudoConfigured = true
		out.Steps = append(out.Steps, "installed /etc/sudoers.d/60-hlab")
	case strings.Contains(stdout, markerSudoSkipped):
		out.Steps = append(out.Steps, "no sudo granted")
	}

	switch {
	case strings.Contains(stdout, markerKeyInstalled):
		out.KeyInstalled = true
		out.Steps = append(out.Steps, "installed hlab's key in authorized_keys")
	case strings.Contains(stdout, markerKeyPresent):
		out.Steps = append(out.Steps, "hlab's key was already authorised")
	}
}

// bootstrapFailure turns a failed script run into an actionable message.
func bootstrapFailure(res sshx.ExecResult) string {
	if msg := firstLine(res.Stderr); msg != "" {
		return strings.TrimPrefix(msg, "hlab:error ")
	}
	if msg := firstLine(res.Stdout); msg != "" {
		return msg
	}
	return fmt.Sprintf("bootstrap script exited %d without output", res.ExitCode)
}

func looksLikeSudoRefusal(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "incorrect password") ||
		strings.Contains(s, "sorry, try again") ||
		strings.Contains(s, "is not in the sudoers") ||
		strings.Contains(s, "a password is required") ||
		strings.Contains(s, "no tty present")
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
