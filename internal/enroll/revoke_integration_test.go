package enroll_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/homelab-cli/internal/enroll"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

// asAdmin runs a command as the operator's account, over a password login
// that revocation does not touch.
func asAdmin(ctx context.Context, t *testing.T, d *sshx.Dialer, h testHost, script string) sshx.ExecResult {
	t.Helper()
	client, err := d.DialPassword(ctx, sshx.Target{
		Name: "target", Address: h.address, Port: h.port, User: h.user,
	}, h.password)
	if err != nil {
		t.Fatalf("admin login: %v", err)
	}
	defer func() { _ = client.Close() }()
	res, err := sshx.Exec(ctx, client, "sudo -S -p '' sh -s", strings.NewReader(h.password+"\n"+script))
	if err != nil {
		t.Fatalf("admin command: %v", err)
	}
	return res
}

func revokeAs(ctx context.Context, t *testing.T, d *sshx.Dialer, h testHost, user, keyPath, script string) sshx.ExecResult {
	t.Helper()
	client, err := d.Dial(ctx, sshx.Target{
		Name: "target", Address: h.address, Port: h.port, User: user, IdentityFile: keyPath,
	})
	if err != nil {
		t.Fatalf("login as %s: %v", user, err)
	}
	defer func() { _ = client.Close() }()
	res, err := sshx.Exec(ctx, client, "sudo -n sh -s", strings.NewReader(script))
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	return res
}

func TestIntegrationRevokeRemovesEveryWayIn(t *testing.T) {
	host := requireTestHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	key, err := enroll.LoadOrCreateKey(filepath.Join(t.TempDir(), "id_hlab"), "hlab test key")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	enroller, dialer, _ := newIntegrationEnroller(t)
	out := enroller.Enrol(ctx, enroll.Request{
		Name: "target", Address: host.address, Port: host.port,
		Credentials:    enroll.Credentials{User: host.user, Password: host.password},
		ManagementUser: "hlab", SudoMode: enroll.SudoNoPassword, Key: key,
	})
	if !out.OK() {
		t.Fatalf("enrol: %s", out.Error)
	}

	// The key also authorised somewhere enrolment never put it: revocation
	// must find it there too.
	blob, _ := enroll.KeyBlob(key.AuthorizedKey)
	asAdmin(ctx, t, dialer, host, `
mkdir -p /home/`+host.user+`/.ssh
echo '`+key.AuthorizedKey+`' >> /home/`+host.user+`/.ssh/authorized_keys
echo 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOtherKeyThatMustSurviveRevocation0000000 keep-me' >> /home/`+host.user+`/.ssh/authorized_keys
`)

	script, err := enroll.BuildRevokeScript(enroll.RevokeOptions{User: "hlab", KeyBlob: blob, RemoveAccount: true})
	if err != nil {
		t.Fatalf("BuildRevokeScript: %v", err)
	}
	res := revokeAs(ctx, t, dialer, host, "hlab", key.PrivatePath, script)
	rev := enroll.ParseRevocation(res.Stdout)
	if !rev.Complete {
		t.Fatalf("revocation incomplete: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
	if len(rev.KeyFiles) != 2 || !rev.SudoRemoved || !rev.AccountRemoving {
		t.Errorf("revocation = %+v, want the key from 2 files, sudo removed, account removing", rev)
	}

	_, err = dialer.DialKeyOnly(ctx, sshx.Target{
		Name: "target", Address: host.address, Port: host.port, User: "hlab", IdentityFile: key.PrivatePath,
	})
	if !sshx.AuthRefused(err) {
		t.Errorf("hlab's key after revocation: %v, want refused", err)
	}

	// The account is deleted from a detached process after the session ends.
	deadline := time.Now().Add(20 * time.Second)
	for {
		check := asAdmin(ctx, t, dialer, host, `
id hlab >/dev/null 2>&1 && echo account-present
[ -e /etc/sudoers.d/60-hlab ] && echo sudoers-present
[ -d /home/hlab ] && echo home-present
grep -c keep-me /home/`+host.user+`/.ssh/authorized_keys
grep -c '`+blob+`' /home/`+host.user+`/.ssh/authorized_keys || true
`)
		lines := strings.Fields(check.Stdout)
		done := !strings.Contains(check.Stdout, "present")
		if done || time.Now().After(deadline) {
			if !done {
				t.Errorf("after revocation: %v", lines)
			}
			// Unrelated keys survive; hlab's is gone.
			if n := len(lines); n < 2 || lines[n-2] != "1" || lines[n-1] != "0" {
				t.Errorf("operator's authorized_keys: keep-me count, hlab-key count = %v, want 1 0", lines)
			}
			break
		}
		time.Sleep(time.Second)
	}
}

// On an adopted machine the recorded account is the operator's own. Asking to
// remove it must be refused before anything on the machine changes.
func TestIntegrationRevokeRefusesToDeleteAnAccountHlabDidNotCreate(t *testing.T) {
	host := requireTestHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	key, err := enroll.LoadOrCreateKey(filepath.Join(t.TempDir(), "id_hlab"), "hlab test key")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	_, dialer, _ := newIntegrationEnroller(t)
	blob, _ := enroll.KeyBlob(key.AuthorizedKey)
	asAdmin(ctx, t, dialer, host, `
mkdir -p /home/`+host.user+`/.ssh
echo '`+key.AuthorizedKey+`' >> /home/`+host.user+`/.ssh/authorized_keys
chown -R `+host.user+` /home/`+host.user+`/.ssh
printf '%s ALL=(ALL) NOPASSWD:ALL\n' `+host.user+` > /etc/sudoers.d/60-hlab
chmod 0440 /etc/sudoers.d/60-hlab
`)

	script, _ := enroll.BuildRevokeScript(enroll.RevokeOptions{User: host.user, KeyBlob: blob, RemoveAccount: true})
	res := revokeAs(ctx, t, dialer, host, host.user, key.PrivatePath, script)
	if enroll.ParseRevocation(res.Stdout).Complete || !strings.Contains(res.Stderr, "not created by hlab") {
		t.Fatalf("revocation of %s was not refused: stdout=%q stderr=%q", host.user, res.Stdout, res.Stderr)
	}

	// Refused means untouched: the key and the sudo rule are both still there.
	client, err := dialer.DialKeyOnly(ctx, sshx.Target{
		Name: "target", Address: host.address, Port: host.port, User: host.user, IdentityFile: key.PrivatePath,
	})
	if err != nil {
		t.Fatalf("the key stopped working after a refused revocation: %v", err)
	}
	_ = client.Close()
	check := asAdmin(ctx, t, dialer, host, `[ -e /etc/sudoers.d/60-hlab ] && echo sudoers-present; rm -f /etc/sudoers.d/60-hlab`)
	if !strings.Contains(check.Stdout, "sudoers-present") {
		t.Error("the sudo rule was removed by a refused revocation")
	}
}
