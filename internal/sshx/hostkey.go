package sshx

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// HostKeyPrompt is consulted when a host is not yet in known_hosts. Returning
// true accepts the key, which is then recorded so later connections verify
// normally.
//
// It is never called for a host whose recorded key does not match: that is a
// potential man-in-the-middle and is always refused.
type HostKeyPrompt func(host string, remote net.Addr, key ssh.PublicKey) (bool, error)

// FingerprintSHA256 renders a key fingerprint the way ssh(1) displays it.
func FingerprintSHA256(key ssh.PublicKey) string {
	return ssh.FingerprintSHA256(key)
}

// hostKeyCallback builds the verification policy from the dialer options.
func hostKeyCallback(opts Options) (ssh.HostKeyCallback, error) {
	if opts.InsecureIgnoreHostKey {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // opt-in via an explicit flag
	}

	if opts.KnownHostsFile == "" {
		return nil, errors.New("no known_hosts file configured; set defaults.knownHostsFile or pass --insecure-host-key")
	}

	// When a prompt is available the file is allowed not to exist yet: the
	// first accepted key creates it. Without a prompt, a missing file is an
	// error, since nothing could ever be verified.
	if _, err := os.Stat(opts.KnownHostsFile); err != nil {
		if opts.OnUnknownHostKey == nil {
			return nil, fmt.Errorf(
				"known_hosts file %s is unreadable: %w\n"+
					"Connect once with `ssh` to record the host keys, or pass --insecure-host-key to skip verification",
				opts.KnownHostsFile, err)
		}
		if err := createKnownHostsFile(opts.KnownHostsFile); err != nil {
			return nil, err
		}
	}

	verifier, err := knownhosts.New(opts.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("load known_hosts %s: %w", opts.KnownHostsFile, err)
	}

	if opts.OnUnknownHostKey == nil {
		return verifier, nil
	}

	v := &tofuVerifier{
		path:   opts.KnownHostsFile,
		verify: verifier,
		prompt: opts.OnUnknownHostKey,
	}
	return v.callback, nil
}

// tofuVerifier verifies host keys against known_hosts, asking about hosts that
// are not recorded yet and remembering the answer.
//
// The underlying knownhosts verifier snapshots the file when it is built, so it
// is rebuilt after each accepted key. Without that, a key accepted during a run
// would look unknown again on the next connection: hlab would ask repeatedly
// and append a duplicate entry every time.
type tofuVerifier struct {
	mu     sync.Mutex
	path   string
	verify ssh.HostKeyCallback
	prompt HostKeyPrompt
}

func (v *tofuVerifier) callback(hostname string, remote net.Addr, key ssh.PublicKey) error {
	// Serialised so two connections to the same new host cannot both prompt
	// and both append.
	v.mu.Lock()
	defer v.mu.Unlock()

	err := v.verify(hostname, remote, key)
	if err == nil {
		return nil
	}

	var keyErr *knownhosts.KeyError
	if !errors.As(err, &keyErr) {
		return err
	}
	// A populated Want means the host is known but presented a different key.
	// That is exactly the case host key checking exists to catch, so it is
	// never offered to the prompt.
	if len(keyErr.Want) > 0 {
		return fmt.Errorf(
			"host key mismatch for %s: the machine presented a key that does not match %s.\n"+
				"This can mean the host was rebuilt, or that the connection is being intercepted.\n"+
				"If you are certain it was rebuilt, remove its entry from that file and retry",
			hostname, v.path)
	}

	accepted, promptErr := v.prompt(hostname, remote, key)
	if promptErr != nil {
		return promptErr
	}
	if !accepted {
		return fmt.Errorf("host key for %s was not accepted", hostname)
	}

	if err := appendKnownHost(v.path, hostname, remote, key); err != nil {
		return err
	}

	// Reload so the newly trusted key is visible to later connections.
	if reloaded, err := knownhosts.New(v.path); err == nil {
		v.verify = reloaded
	}
	return nil
}

// createKnownHostsFile creates an empty known_hosts (and its directory).
func createKnownHostsFile(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	return f.Close()
}

// appendKnownHost records an accepted key, under both the hostname and the
// address so either form verifies later.
func appendKnownHost(path, hostname string, remote net.Addr, key ssh.PublicKey) error {
	addresses := []string{knownhosts.Normalize(hostname)}
	if remote != nil {
		if normalised := knownhosts.Normalize(remote.String()); normalised != addresses[0] {
			addresses = append(addresses, normalised)
		}
	}

	line := knownhosts.Line(addresses, key)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.WriteString(line + "\n"); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
