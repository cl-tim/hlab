package facts_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/facts"
	"github.com/cl-tim/hlab/internal/sshtest"
	"github.com/cl-tim/hlab/internal/sshx"
)

func newCollector(t *testing.T, concurrency int) *facts.Collector {
	t.Helper()

	dialer, err := sshx.NewDialer(sshx.Options{
		InsecureIgnoreHostKey: true,
		ConnectTimeout:        5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	return facts.NewCollector(sshx.NewExecutor(dialer, concurrency))
}

func targetsFor(srv *sshtest.Server, key string, names ...string) []sshx.Target {
	out := make([]sshx.Target, 0, len(names))
	for _, n := range names {
		out = append(out, sshx.Target{
			Name: n, Address: srv.Host, Port: srv.Port, User: "tester", IdentityFile: key,
		})
	}
	return out
}

func loadFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "ubuntu-wsl.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(raw)
}

func TestCollectOverSSH(t *testing.T) {
	fixture := loadFixture(t)

	srv := sshtest.Start(t, func(string) sshtest.Response {
		return sshtest.Response{Stdout: fixture}
	})
	key := sshtest.WriteKey(t)

	snapshots := newCollector(t, 4).Collect(context.Background(), targetsFor(srv, key, "b-host", "a-host"))

	if len(snapshots) != 2 {
		t.Fatalf("got %d snapshots, want 2", len(snapshots))
	}
	// Sorted by host name regardless of completion order.
	if snapshots[0].Host != "a-host" || snapshots[1].Host != "b-host" {
		t.Errorf("hosts = %q, %q; want a-host, b-host", snapshots[0].Host, snapshots[1].Host)
	}

	for _, f := range snapshots {
		if !f.OK() {
			t.Fatalf("host %s failed: %s", f.Host, f.Error)
		}
		if f.OS.Name != "Ubuntu" {
			t.Errorf("host %s OS.Name = %q, want Ubuntu", f.Host, f.OS.Name)
		}
		if f.Hostname != "Office" {
			t.Errorf("host %s Hostname = %q, want Office", f.Host, f.Hostname)
		}
	}

	// The whole snapshot must come from a single round trip per host.
	commands := srv.Commands()
	if len(commands) != 2 {
		t.Errorf("server saw %d commands, want 2 (one per host)", len(commands))
	}
	for _, c := range commands {
		if !strings.Contains(c, facts.SectionPrefix) {
			t.Errorf("command does not look like the collection script: %.60q", c)
		}
	}
}

func TestCollectReportsUnreachableHost(t *testing.T) {
	port := sshtest.ClosedPort(t)
	key := sshtest.WriteKey(t)

	dialer, err := sshx.NewDialer(sshx.Options{
		InsecureIgnoreHostKey: true,
		ConnectTimeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewDialer: %v", err)
	}
	collector := facts.NewCollector(sshx.NewExecutor(dialer, 1))

	snapshots := collector.Collect(context.Background(), []sshx.Target{{
		Name: "dead", Address: "127.0.0.1", Port: port, User: "tester", IdentityFile: key,
	}})

	if len(snapshots) != 1 {
		t.Fatalf("got %d snapshots, want 1", len(snapshots))
	}
	if snapshots[0].OK() {
		t.Error("OK() = true, want false for an unreachable host")
	}
	if snapshots[0].Error == "" {
		t.Error("Error is empty, want a dial failure message")
	}
}

func TestCollectRejectsNonShellOutput(t *testing.T) {
	// A login shell that cannot run the script (a restricted or non-POSIX
	// shell) must be reported as an error, not parsed into empty facts.
	srv := sshtest.Start(t, func(string) sshtest.Response {
		return sshtest.Response{Stderr: "This service allows sftp connections only.\n", Exit: 1}
	})
	key := sshtest.WriteKey(t)

	snapshots := newCollector(t, 1).Collect(context.Background(), targetsFor(srv, key, "sftp-only"))

	if snapshots[0].OK() {
		t.Fatal("OK() = true, want false when the host returns no sections")
	}
	if !strings.Contains(snapshots[0].Error, "sftp connections only") {
		t.Errorf("Error = %q, want it to surface the host's own message", snapshots[0].Error)
	}
}

func TestCollectFlagsTruncatedOutput(t *testing.T) {
	fixture := loadFixture(t)
	// Chop off the trailing end marker to simulate a dropped connection.
	truncated := strings.TrimSuffix(strings.TrimRight(fixture, "\n"), facts.SectionPrefix+"end#")

	srv := sshtest.Start(t, func(string) sshtest.Response {
		return sshtest.Response{Stdout: truncated}
	})
	key := sshtest.WriteKey(t)

	snapshots := newCollector(t, 1).Collect(context.Background(), targetsFor(srv, key, "flaky"))

	if !snapshots[0].OK() {
		t.Fatalf("OK() = false: %s", snapshots[0].Error)
	}
	if !snapshots[0].Partial {
		t.Error("Partial = false, want true for truncated output")
	}
}
