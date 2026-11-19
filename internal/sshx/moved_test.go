package sshx_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/cl-tim/hlab/internal/sshtest"
	"github.com/cl-tim/hlab/internal/sshx"
)

// The old address is one nothing listens on: the machine has moved away
// from it, and only the recorded key ties the new address back to it.
const oldAddress = "192.0.2.50"

func movedSetup(t *testing.T, recorded ssh.PublicKey) (*sshtest.Server, *sshx.Dialer, sshx.Target, string) {
	t.Helper()
	srv := sshtest.Start(t, ok)
	path := filepath.Join(t.TempDir(), "known_hosts")
	body := ""
	if recorded != nil {
		body = knownhosts.Line([]string{knownhosts.Normalize(fmt.Sprintf("%s:%d", oldAddress, srv.Port))}, recorded) + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := sshx.NewDialer(sshx.Options{KnownHostsFile: path, ConnectTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	target := sshx.Target{Name: "kube", Address: oldAddress, Port: srv.Port, User: "hlab", IdentityFile: sshtest.WriteKey(t)}
	return srv, d, target, path
}

func TestDialMovedFollowsTheRecordedHostKey(t *testing.T) {
	srv, d, target, path := movedSetup(t, nil)
	// Record the server's real key against the old address.
	line := knownhosts.Line([]string{knownhosts.Normalize(fmt.Sprintf("%s:%d", oldAddress, srv.Port))}, srv.HostKey)
	_ = os.WriteFile(path, []byte(line+"\n"), 0o600)
	d, _ = sshx.NewDialer(sshx.Options{KnownHostsFile: path, ConnectTimeout: 3 * time.Second})

	client, err := d.DialMoved(context.Background(), target, srv.Host)
	if err != nil {
		t.Fatalf("DialMoved: %v", err)
	}
	_ = client.Close()

	// From now on the new address verifies the ordinary way.
	fresh, _ := sshx.NewDialer(sshx.Options{KnownHostsFile: path, ConnectTimeout: 3 * time.Second})
	target.Address = srv.Host
	client, err = fresh.Dial(context.Background(), target)
	if err != nil {
		t.Fatalf("the new address is not trusted afterwards: %v", err)
	}
	_ = client.Close()
}

// The whole point: a name resolving somewhere new must not be enough. A
// machine that cannot present the recorded key is somebody else.
func TestDialMovedRefusesAnImpostor(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	other, _ := ssh.NewPublicKey(pub)
	srv, d, target, path := movedSetup(t, other)

	if _, err := d.DialMoved(context.Background(), target, srv.Host); err == nil {
		t.Fatal("followed a machine presenting a different host key")
	}
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), "\n") != 1 {
		t.Errorf("known_hosts was written after a refusal:\n%s", raw)
	}
}

func TestDialMovedNeedsSomethingRecorded(t *testing.T) {
	srv, d, target, _ := movedSetup(t, nil)
	_, err := d.DialMoved(context.Background(), target, srv.Host)
	if err == nil || !strings.Contains(err.Error(), "no host key is recorded") {
		t.Fatalf("DialMoved with nothing recorded: %v", err)
	}
}

func TestDialMovedRefusesWithoutHostKeyChecking(t *testing.T) {
	srv := sshtest.Start(t, ok)
	d, _ := sshx.NewDialer(sshx.Options{InsecureIgnoreHostKey: true})
	_, err := d.DialMoved(context.Background(), sshx.Target{
		Name: "x", Address: oldAddress, Port: srv.Port, User: "hlab", IdentityFile: sshtest.WriteKey(t),
	}, srv.Host)
	if err == nil {
		t.Fatal("followed a move with host key checking off, where nothing can be proved")
	}
}
