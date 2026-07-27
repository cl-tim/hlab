package enroll_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/cl-tim/homelab-cli/internal/enroll"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

// Integration tests run against a real SSH host with a real sudo, because the
// bootstrap script's behaviour (useradd, visudo, authorized_keys permissions)
// cannot be verified any other way. They are skipped unless pointed at one.
//
// Bring one up with:
//
//	docker build -t hlab-enroll-test ./testdata/enroll-target
//	docker run -d --name hlab-enroll -p 52322:22 hlab-enroll-test
//	HLAB_TEST_SSH_ADDR=127.0.0.1:52322 \
//	HLAB_TEST_SSH_USER=labadmin \
//	HLAB_TEST_SSH_PASSWORD=testpass123 go test ./internal/enroll/ -run Integration -v
type testHost struct {
	address  string
	port     int
	user     string
	password string
}

func requireTestHost(t *testing.T) testHost {
	t.Helper()

	addr := os.Getenv("HLAB_TEST_SSH_ADDR")
	if addr == "" {
		t.Skip("set HLAB_TEST_SSH_ADDR to run enrolment integration tests")
	}

	host, portStr, found := strings.Cut(addr, ":")
	if !found {
		t.Fatalf("HLAB_TEST_SSH_ADDR must be host:port, got %q", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("bad port in HLAB_TEST_SSH_ADDR: %v", err)
	}

	return testHost{
		address:  host,
		port:     port,
		user:     os.Getenv("HLAB_TEST_SSH_USER"),
		password: os.Getenv("HLAB_TEST_SSH_PASSWORD"),
	}
}

// newIntegrationEnroller uses a per-test known_hosts so trust-on-first-use is
// exercised without touching the developer's real one.
func newIntegrationEnroller(t *testing.T) (*enroll.Enroller, *sshx.Dialer, int) {
	t.Helper()

	prompts := 0
	dialer, err := sshx.NewDialer(sshx.Options{
		KnownHostsFile: filepath.Join(t.TempDir(), "known_hosts"),
		ConnectTimeout: 15 * time.Second,
		OnUnknownHostKey: func(string, net.Addr, ssh.PublicKey) (bool, error) {
			prompts++
			return true, nil
		},
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	return enroll.NewEnroller(dialer), dialer, prompts
}

func TestIntegrationEnrolRealHost(t *testing.T) {
	host := requireTestHost(t)

	key, err := enroll.LoadOrCreateKey(filepath.Join(t.TempDir(), "id_hlab"), "hlab test key")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if !key.Created {
		t.Fatal("expected a freshly generated key")
	}

	enroller, _, _ := newIntegrationEnroller(t)

	req := enroll.Request{
		Name:    "target",
		Address: host.address,
		Port:    host.port,
		Credentials: enroll.Credentials{
			User:     host.user,
			Password: host.password,
		},
		ManagementUser: "hlab",
		SudoMode:       enroll.SudoNoPassword,
		Key:            key,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	out := enroller.Enrol(ctx, req)

	if out.Error != "" {
		t.Fatalf("enrolment failed: %s\nsteps: %v", out.Error, out.Steps)
	}
	if !out.UserCreated {
		t.Error("UserCreated = false, want true on a fresh host")
	}
	if !out.SudoConfigured {
		t.Error("SudoConfigured = false, want true")
	}
	if !out.KeyInstalled {
		t.Error("KeyInstalled = false, want true")
	}
	if !out.Verified {
		t.Error("Verified = false: hlab could not log in with its own key")
	}
	if !out.SudoVerified {
		t.Error("SudoVerified = false: the management account could not escalate")
	}

	// Re-running must be idempotent: the account and key already exist, and
	// nothing may break or duplicate.
	second := enroller.Enrol(ctx, req)
	if second.Error != "" {
		t.Fatalf("second enrolment failed: %s", second.Error)
	}
	if second.UserCreated {
		t.Error("UserCreated = true on re-run, want false")
	}
	if second.KeyInstalled {
		t.Error("KeyInstalled = true on re-run, want false (key was already authorised)")
	}
	if !second.Verified {
		t.Error("Verified = false on re-run")
	}
}

func TestIntegrationAdoptChangesNothing(t *testing.T) {
	// Adopting records credentials the operator already has. It must verify
	// they work, and it must not create an account or touch sudo.
	host := requireTestHost(t)

	key, err := enroll.LoadOrCreateKey(filepath.Join(t.TempDir(), "id_hlab"), "hlab test key")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}

	enroller, _, _ := newIntegrationEnroller(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out := enroller.Enrol(ctx, enroll.Request{
		Name:    "adopted",
		Address: host.address,
		Port:    host.port,
		Mode:    enroll.ModeAdopt,
		Credentials: enroll.Credentials{
			User:     host.user,
			Password: host.password,
		},
		Key: key,
	})

	if out.Error != "" {
		t.Fatalf("adoption failed: %s", out.Error)
	}
	if !out.Verified {
		t.Error("Verified = false: the supplied credentials were not confirmed")
	}
	if out.User != host.user {
		t.Errorf("User = %q, want the operator's own account %q", out.User, host.user)
	}

	// Nothing may have been provisioned.
	if out.Provisioned {
		t.Error("Provisioned = true, want false: adoption must not create anything")
	}
	if out.UserCreated || out.SudoConfigured || out.KeyInstalled {
		t.Errorf("adoption modified the machine: %+v", out)
	}
}

func TestIntegrationAdoptRejectsBadCredentials(t *testing.T) {
	// A machine must never be recorded as manageable on an unverified claim.
	host := requireTestHost(t)

	key, err := enroll.LoadOrCreateKey(filepath.Join(t.TempDir(), "id_hlab"), "hlab test key")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}

	enroller, _, _ := newIntegrationEnroller(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out := enroller.Enrol(ctx, enroll.Request{
		Name:        "adopted",
		Address:     host.address,
		Port:        host.port,
		Mode:        enroll.ModeAdopt,
		Credentials: enroll.Credentials{User: host.user, Password: "wrong"},
		Key:         key,
	})

	if out.Error == "" || out.Verified {
		t.Fatalf("adoption succeeded with bad credentials: %+v", out)
	}
}

func TestIntegrationEnrolRejectsWrongPassword(t *testing.T) {
	host := requireTestHost(t)

	key, err := enroll.LoadOrCreateKey(filepath.Join(t.TempDir(), "id_hlab"), "hlab test key")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}

	enroller, _, _ := newIntegrationEnroller(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out := enroller.Enrol(ctx, enroll.Request{
		Name:    "target",
		Address: host.address,
		Port:    host.port,
		Credentials: enroll.Credentials{
			User:     host.user,
			Password: "definitely-not-the-password",
		},
		ManagementUser: "hlab",
		SudoMode:       enroll.SudoNoPassword,
		Key:            key,
	})

	if out.Error == "" {
		t.Fatal("enrolment succeeded with a wrong password")
	}
	if out.Verified {
		t.Error("Verified = true despite a failed login")
	}
}
