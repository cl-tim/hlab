package sshx_test

import (
	"context"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cl-tim/hlab/internal/sshtest"
	"github.com/cl-tim/hlab/internal/sshx"
)

func ok(string) sshtest.Response { return sshtest.Response{Stdout: "ok\n"} }

func keyOnlyDialer(t *testing.T) *sshx.Dialer {
	t.Helper()
	d, err := sshx.NewDialer(sshx.Options{InsecureIgnoreHostKey: true, ConnectTimeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	return d
}

// Unenroll's proof rests on telling "this key is refused" apart from "the
// machine did not answer". Calling an unreachable machine refused would record
// a revocation nobody verified.
func TestAuthRefusedIsDistinctFromUnreachable(t *testing.T) {
	refusing := sshtest.StartWithKeys(t, ok, func(ssh.PublicKey) bool { return false })
	key := sshtest.WriteKey(t)
	d := keyOnlyDialer(t)
	ctx := context.Background()

	_, err := d.DialKeyOnly(ctx, sshx.Target{
		Name: "r", Address: refusing.Host, Port: refusing.Port, User: "hlab", IdentityFile: key,
	})
	if !sshx.AuthRefused(err) {
		t.Errorf("refused key: AuthRefused(%v) = false, want true", err)
	}

	_, err = d.DialKeyOnly(ctx, sshx.Target{
		Name: "gone", Address: "127.0.0.1", Port: sshtest.ClosedPort(t), User: "hlab", IdentityFile: key,
	})
	if err == nil || sshx.AuthRefused(err) {
		t.Errorf("unreachable: AuthRefused(%v) = true, want false", err)
	}

	accepting := sshtest.Start(t, ok)
	client, err := d.DialKeyOnly(ctx, sshx.Target{
		Name: "a", Address: accepting.Host, Port: accepting.Port, User: "hlab", IdentityFile: key,
	})
	if err != nil {
		t.Fatalf("accepted key: %v", err)
	}
	_ = client.Close()
}

func TestDialKeyOnlyNeedsAKey(t *testing.T) {
	srv := sshtest.Start(t, ok)
	_, err := keyOnlyDialer(t).DialKeyOnly(context.Background(), sshx.Target{
		Name: "x", Address: srv.Host, Port: srv.Port, User: "hlab",
	})
	if err == nil {
		t.Fatal("DialKeyOnly with no identity file succeeded; it must not fall back to the agent")
	}
}
