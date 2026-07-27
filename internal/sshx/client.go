// Package sshx wraps golang.org/x/crypto/ssh with the connection and
// authentication policy hlab uses: agent-first auth, strict known_hosts
// verification, and context-aware dialling.
package sshx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Target is everything sshx needs to reach one machine. It is deliberately
// independent of the inventory package so the two can evolve separately.
type Target struct {
	Name         string
	Address      string
	Port         int
	User         string
	IdentityFile string
}

func (t Target) addr() string { return net.JoinHostPort(t.Address, fmt.Sprint(t.Port)) }

// Options configure connection behaviour for every target.
type Options struct {
	// KnownHostsFile is consulted to verify host keys.
	KnownHostsFile string
	// InsecureIgnoreHostKey disables host key verification entirely. This
	// defeats the protection against man-in-the-middle attacks and exists only
	// for first-run bootstrapping against machines you physically control.
	InsecureIgnoreHostKey bool
	// ConnectTimeout bounds the TCP dial and SSH handshake.
	ConnectTimeout time.Duration
	// OnUnknownHostKey, when set, is asked whether to trust a host that is not
	// yet in known_hosts. Accepted keys are recorded. A host already recorded
	// under a different key is always refused without consulting this.
	OnUnknownHostKey HostKeyPrompt
}

// Dialer opens SSH connections according to a fixed set of Options. The
// host-key callback is built once and shared across connections.
type Dialer struct {
	opts     Options
	hostKeys ssh.HostKeyCallback
}

// NewDialer validates the options and prepares the host key verifier.
func NewDialer(opts Options) (*Dialer, error) {
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 10 * time.Second
	}

	cb, err := hostKeyCallback(opts)
	if err != nil {
		return nil, err
	}

	return &Dialer{opts: opts, hostKeys: cb}, nil
}

// Dial opens an SSH connection to the target using key-based authentication:
// the target's identity file, then any keys held by the ssh-agent. The caller
// owns the returned client and must Close it.
func (d *Dialer) Dial(ctx context.Context, t Target) (*ssh.Client, error) {
	auths, err := authMethods(t)
	if err != nil {
		return nil, err
	}
	return d.dial(ctx, t, auths)
}

// DialPassword opens an SSH connection using password authentication, falling
// back to keyboard-interactive since many servers offer passwords only that
// way. It exists for enrolling a machine that hlab has no key on yet.
//
// The password is used for this handshake and never stored.
func (d *Dialer) DialPassword(ctx context.Context, t Target, password string) (*ssh.Client, error) {
	auths := []ssh.AuthMethod{
		ssh.Password(password),
		ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = password
			}
			return answers, nil
		}),
	}
	return d.dial(ctx, t, auths)
}

// dial performs the connection with a prepared credential chain.
func (d *Dialer) dial(ctx context.Context, t Target, auths []ssh.AuthMethod) (*ssh.Client, error) {
	// Validate the target before its credentials: "no user" is the more
	// actionable message when both are missing.
	if t.User == "" {
		return nil, fmt.Errorf("host %q has no user; set it on the host or in defaults.user", t.Name)
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf(
			"no SSH credentials available for %q: start an ssh-agent with a loaded key, or set identityFile", t.Name)
	}

	cfg := &ssh.ClientConfig{
		User:            t.User,
		Auth:            auths,
		HostKeyCallback: d.hostKeys,
		Timeout:         d.opts.ConnectTimeout,
	}

	dialCtx, cancel := context.WithTimeout(ctx, d.opts.ConnectTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", t.addr())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", t.addr(), err)
	}

	// The handshake has no context-aware form, so bound it with a deadline and
	// clear the deadline once the connection is established.
	if deadline, ok := dialCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		_ = conn.Close()

		// A host we have a key for, presented under a different algorithm,
		// surfaces as a mismatch — which reads as an intercepted connection
		// when it is only negotiation. OpenSSH avoids this by advertising the
		// algorithms it holds keys for; do the same and try once more.
		if algos := recordedAlgorithms(err); len(algos) > 0 {
			cfg.HostKeyAlgorithms = algos
			return d.redial(ctx, t, cfg)
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", t.addr(), err)
	}
	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(sshConn, chans, reqs), nil
}

// authMethods assembles the credential chain: an explicit identity file first
// (most specific), then whatever the running ssh-agent offers.
func authMethods(t Target) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	if t.IdentityFile != "" {
		key, err := os.ReadFile(t.IdentityFile)
		if err != nil {
			return nil, fmt.Errorf("read identity file %s: %w", t.IdentityFile, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			var passErr *ssh.PassphraseMissingError
			if errors.As(err, &passErr) {
				return nil, fmt.Errorf(
					"identity file %s is passphrase protected; load it into your ssh-agent instead (ssh-add %s)",
					t.IdentityFile, t.IdentityFile)
			}
			return nil, fmt.Errorf("parse identity file %s: %w", t.IdentityFile, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}

	if ag, err := dialAgent(); err == nil && ag != nil {
		methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(ag).Signers))
	}

	return methods, nil
}

// recordedAlgorithms returns the host key types known_hosts holds for a host,
// taken from a failed handshake's KeyError.
//
// knownhosts reports "key mismatch" both for a key that genuinely differs and
// for one whose *algorithm* we simply have no entry for — and the error does not
// say which. Retrying with these algorithms tells them apart: if the key really
// differs, the server presents the same key again under a type we do hold and
// the second attempt fails too. Nothing is trusted that was not already
// recorded; the retry only stops us asking for a type we cannot verify.
func recordedAlgorithms(err error) []string {
	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) || len(keyErr.Want) == 0 {
		return nil
	}

	var algos []string
	for _, want := range keyErr.Want {
		if want.Key != nil {
			algos = append(algos, want.Key.Type())
		}
	}
	return algos
}

// redial repeats the handshake with an adjusted client configuration.
func (d *Dialer) redial(ctx context.Context, t Target, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	dialCtx, cancel := context.WithTimeout(ctx, d.opts.ConnectTimeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(dialCtx, "tcp", t.addr())
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", t.addr(), err)
	}
	if deadline, ok := dialCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, t.addr(), cfg)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake with %s: %w", t.addr(), err)
	}
	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(sshConn, chans, reqs), nil
}
