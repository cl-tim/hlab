package sshx_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cl-tim/hlab/internal/sshtest"
	"github.com/cl-tim/hlab/internal/sshx"
)

// firstKnownHostsLine returns the first non-empty line of a known_hosts file.
func firstKnownHostsLine(t *testing.T, path string) string {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if l := strings.TrimSpace(line); l != "" {
			return l
		}
	}
	t.Fatalf("%s has no entries", path)
	return ""
}

// dialOnce connects to the server and closes immediately, returning any error.
func dialOnce(t *testing.T, dialer *sshx.Dialer, srv *sshtest.Server, key string) error {
	t.Helper()

	client, err := dialer.Dial(context.Background(), sshx.Target{
		Name: "target", Address: srv.Host, Port: srv.Port, User: "tester", IdentityFile: key,
	})
	if err != nil {
		return err
	}
	return client.Close()
}

func TestUnknownHostKeyIsRecordedOnceAccepted(t *testing.T) {
	srv := sshtest.Start(t, func(string) sshtest.Response { return sshtest.Response{} })
	key := sshtest.WriteKey(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	prompted := 0
	dialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile: knownHosts,
		ConnectTimeout: 5 * time.Second,
		OnUnknownHostKey: func(string, net.Addr, ssh.PublicKey) (bool, error) {
			prompted++
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}

	if err := dialOnce(t, dialer, srv, key); err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if prompted != 1 {
		t.Errorf("prompted %d times, want 1", prompted)
	}

	recorded, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if !strings.Contains(string(recorded), "ssh-ed25519") {
		t.Errorf("known_hosts does not contain the accepted key:\n%s", recorded)
	}

	// A second connection must verify against the record without asking again.
	if err := dialOnce(t, dialer, srv, key); err != nil {
		t.Fatalf("second dial: %v", err)
	}
	if prompted != 1 {
		t.Errorf("prompted %d times across two dials, want 1", prompted)
	}
}

func TestRejectedHostKeyFailsTheConnection(t *testing.T) {
	srv := sshtest.Start(t, func(string) sshtest.Response { return sshtest.Response{} })
	key := sshtest.WriteKey(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	dialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile: knownHosts,
		ConnectTimeout: 5 * time.Second,
		OnUnknownHostKey: func(string, net.Addr, ssh.PublicKey) (bool, error) {
			return false, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}

	err = dialOnce(t, dialer, srv, key)
	if err == nil {
		t.Fatal("dial succeeded despite the host key being rejected")
	}
	if !strings.Contains(err.Error(), "not accepted") {
		t.Errorf("error = %q, want it to say the key was not accepted", err)
	}

	// Nothing may be recorded for a key the operator refused.
	recorded, err := os.ReadFile(knownHosts)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if strings.Contains(string(recorded), "ssh-ed25519") {
		t.Errorf("a rejected key was recorded:\n%s", recorded)
	}
}

func TestMismatchedHostKeyIsNeverOffered(t *testing.T) {
	// A host recorded under a different key is the case host key checking
	// exists to catch. It must fail outright, never reach the prompt.
	srv := sshtest.Start(t, func(string) sshtest.Response { return sshtest.Response{} })
	key := sshtest.WriteKey(t)
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")

	// Record the real entry for srv, then substitute a different server's key
	// material into it, leaving the host pattern intact. That is what a
	// rebuilt machine — or an interception — looks like.
	recordDialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile:   knownHosts,
		ConnectTimeout:   5 * time.Second,
		OnUnknownHostKey: func(string, net.Addr, ssh.PublicKey) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	if err := dialOnce(t, recordDialer, srv, key); err != nil {
		t.Fatalf("record srv: %v", err)
	}

	otherSrv := sshtest.Start(t, func(string) sshtest.Response { return sshtest.Response{} })
	otherKnownHosts := filepath.Join(t.TempDir(), "other_known_hosts")
	otherDialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile:   otherKnownHosts,
		ConnectTimeout:   5 * time.Second,
		OnUnknownHostKey: func(string, net.Addr, ssh.PublicKey) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	if err := dialOnce(t, otherDialer, otherSrv, key); err != nil {
		t.Fatalf("record otherSrv: %v", err)
	}

	srvLine := firstKnownHostsLine(t, knownHosts)
	otherLine := firstKnownHostsLine(t, otherKnownHosts)

	// known_hosts line: <host patterns> <key type> <base64 key>
	srvFields := strings.Fields(srvLine)
	otherFields := strings.Fields(otherLine)
	if len(srvFields) < 3 || len(otherFields) < 3 {
		t.Fatalf("unexpected known_hosts format:\n%s\n%s", srvLine, otherLine)
	}
	forged := strings.Join([]string{srvFields[0], otherFields[1], otherFields[2]}, " ")

	if err := os.WriteFile(knownHosts, []byte(forged+"\n"), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	prompted := 0
	dialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile: knownHosts,
		ConnectTimeout: 5 * time.Second,
		OnUnknownHostKey: func(string, net.Addr, ssh.PublicKey) (bool, error) {
			prompted++
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}

	err = dialOnce(t, dialer, srv, key)
	if err == nil {
		t.Fatal("dial succeeded despite a host key mismatch")
	}
	if prompted != 0 {
		t.Errorf("the operator was asked about a mismatched key %d time(s); it must be refused outright", prompted)
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %q, want it to report a mismatch", err)
	}
}

func TestMissingKnownHostsIsCreatedWhenPromptAvailable(t *testing.T) {
	srv := sshtest.Start(t, func(string) sshtest.Response { return sshtest.Response{} })
	key := sshtest.WriteKey(t)
	// A path whose directory does not exist either.
	knownHosts := filepath.Join(t.TempDir(), "nested", "known_hosts")

	dialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile:   knownHosts,
		ConnectTimeout:   5 * time.Second,
		OnUnknownHostKey: func(string, net.Addr, ssh.PublicKey) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	if err := dialOnce(t, dialer, srv, key); err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := os.Stat(knownHosts); err != nil {
		t.Errorf("known_hosts was not created: %v", err)
	}
}

func TestMissingKnownHostsStillFailsWithoutPrompt(t *testing.T) {
	// Without a way to ask, a missing known_hosts cannot be silently accepted.
	_, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts"),
	})
	if err == nil {
		t.Fatal("want an error when known_hosts is missing and nothing can prompt")
	}
}
