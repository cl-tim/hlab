package sshx_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cl-tim/homelab-cli/internal/sshtest"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

// newTestExecutor wires an executor at the given server, skipping host key
// verification since the fake server's key is generated per-run.
func newTestExecutor(t *testing.T, concurrency int) *sshx.Executor {
	t.Helper()

	dialer, err := sshx.NewDialer(sshx.Options{
		InsecureIgnoreHostKey: true,
		ConnectTimeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	return sshx.NewExecutor(dialer, concurrency)
}

func targetsFor(srv *sshtest.Server, keyPath string, names ...string) []sshx.Target {
	targets := make([]sshx.Target, 0, len(names))
	for _, n := range names {
		targets = append(targets, sshx.Target{
			Name: n, Address: srv.Host, Port: srv.Port, User: "tester", IdentityFile: keyPath,
		})
	}
	return targets
}

func TestRunCapturesOutputAndExitZero(t *testing.T) {
	srv := sshtest.Start(t, func(string) sshtest.Response {
		return sshtest.Response{Stdout: "up 3 days\n"}
	})
	key := sshtest.WriteKey(t)
	exec := newTestExecutor(t, 4)

	results := exec.Run(context.Background(), targetsFor(srv, key, "a"), "uptime")

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Err != nil {
		t.Fatalf("unexpected error: %v", r.Err)
	}
	if !r.OK() {
		t.Errorf("OK() = false, want true")
	}
	if r.Stdout != "up 3 days\n" {
		t.Errorf("Stdout = %q, want %q", r.Stdout, "up 3 days\n")
	}
	if r.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", r.ExitCode)
	}
	if got := srv.Commands(); len(got) != 1 || got[0] != "uptime" {
		t.Errorf("server saw %v, want [uptime]", got)
	}
}

func TestRunNonZeroExitIsNotAnError(t *testing.T) {
	// A command that runs and fails is a successful execution reporting a
	// non-zero status, not a transport error.
	srv := sshtest.Start(t, func(string) sshtest.Response {
		return sshtest.Response{Stderr: "no such file\n", Exit: 2}
	})
	key := sshtest.WriteKey(t)
	exec := newTestExecutor(t, 1)

	results := exec.Run(context.Background(), targetsFor(srv, key, "a"), "cat /nope")

	r := results[0]
	if r.Err != nil {
		t.Fatalf("Err = %v, want nil", r.Err)
	}
	if r.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", r.ExitCode)
	}
	if r.Stderr != "no such file\n" {
		t.Errorf("Stderr = %q", r.Stderr)
	}
	if r.OK() {
		t.Error("OK() = true, want false")
	}
}

func TestRunFansOutAndSortsByHost(t *testing.T) {
	srv := sshtest.Start(t, func(string) sshtest.Response {
		return sshtest.Response{Stdout: "ok\n"}
	})
	key := sshtest.WriteKey(t)
	exec := newTestExecutor(t, 4)

	results := exec.Run(context.Background(), targetsFor(srv, key, "zeta", "alpha", "mike"), "true")

	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	want := []string{"alpha", "mike", "zeta"}
	for i, w := range want {
		if results[i].Host != w {
			t.Errorf("results[%d].Host = %q, want %q", i, results[i].Host, w)
		}
		if !results[i].OK() {
			t.Errorf("host %s not ok: %v", results[i].Host, results[i].Err)
		}
	}
}

func TestRunRespectsConcurrencyLimit(t *testing.T) {
	var (
		mu      sync.Mutex
		active  int
		maxSeen int
	)

	srv := sshtest.Start(t, func(string) sshtest.Response {
		mu.Lock()
		active++
		if active > maxSeen {
			maxSeen = active
		}
		mu.Unlock()

		time.Sleep(50 * time.Millisecond)

		mu.Lock()
		active--
		mu.Unlock()
		return sshtest.Response{Stdout: "ok\n"}
	})
	key := sshtest.WriteKey(t)
	exec := newTestExecutor(t, 2)

	names := []string{"h1", "h2", "h3", "h4", "h5", "h6"}
	results := exec.Run(context.Background(), targetsFor(srv, key, names...), "sleep")

	for _, r := range results {
		if !r.OK() {
			t.Fatalf("host %s failed: %v", r.Host, r.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if maxSeen > 2 {
		t.Errorf("max concurrent sessions = %d, want <= 2", maxSeen)
	}
}

func TestRunCancelledContext(t *testing.T) {
	srv := sshtest.Start(t, func(string) sshtest.Response {
		return sshtest.Response{Stdout: "ok\n"}
	})
	key := sshtest.WriteKey(t)
	exec := newTestExecutor(t, 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before any work starts

	results := exec.Run(ctx, targetsFor(srv, key, "a", "b"), "uptime")

	for _, r := range results {
		if r.OK() {
			t.Errorf("host %s reported ok despite cancellation", r.Host)
		}
	}
}

func TestPingSucceedsAgainstLiveServer(t *testing.T) {
	srv := sshtest.Start(t, func(string) sshtest.Response { return sshtest.Response{} })
	key := sshtest.WriteKey(t)
	exec := newTestExecutor(t, 4)

	results := exec.Ping(context.Background(), targetsFor(srv, key, "a", "b"))

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if !r.OK() {
			t.Errorf("ping %s failed: %v", r.Host, r.Err)
		}
		if r.DurationMs < 0 {
			t.Errorf("ping %s has negative duration", r.Host)
		}
	}
}

func TestPingFailsOnClosedPort(t *testing.T) {
	port := sshtest.ClosedPort(t)
	key := sshtest.WriteKey(t)

	dialer, err := sshx.NewDialer(sshx.Options{InsecureIgnoreHostKey: true, ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	exec := sshx.NewExecutor(dialer, 1)

	results := exec.Ping(context.Background(), []sshx.Target{{
		Name: "dead", Address: "127.0.0.1", Port: port, User: "tester", IdentityFile: key,
	}})

	r := results[0]
	if r.Err == nil {
		t.Fatal("Err = nil, want a dial failure")
	}
	if r.Error == "" {
		t.Error("Error string not populated for structured output")
	}
	if r.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1", r.ExitCode)
	}
}

func TestDialRequiresUser(t *testing.T) {
	dialer, err := sshx.NewDialer(sshx.Options{InsecureIgnoreHostKey: true})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}

	_, err = dialer.Dial(context.Background(), sshx.Target{
		Name: "a", Address: "127.0.0.1", Port: 22,
	})
	if err == nil {
		t.Fatal("Dial: want error for missing user, got nil")
	}
	if !strings.Contains(err.Error(), "no user") {
		t.Errorf("error = %q, want it to mention the missing user", err)
	}
}

func TestNewDialerRejectsMissingKnownHosts(t *testing.T) {
	_, err := sshx.NewDialer(sshx.Options{KnownHostsFile: filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("NewDialer: want error for missing known_hosts, got nil")
	}
	if !strings.Contains(err.Error(), "--insecure-host-key") {
		t.Errorf("error = %q, want it to suggest the escape hatch", err)
	}
}

func TestNewDialerRequiresHostKeyPolicy(t *testing.T) {
	// Neither a known_hosts file nor the insecure opt-out: must not silently
	// fall back to trusting everything.
	if _, err := sshx.NewDialer(sshx.Options{}); err == nil {
		t.Fatal("NewDialer: want error when no host key policy is configured")
	}
}
