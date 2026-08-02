package backup

import (
	"path/filepath"
	"strings"
	"testing"
)

// A restore that unpacks before it verifies has already destroyed the thing it
// would need to go back to.
func TestRestoreVerifiesBeforeItTouchesAnything(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	script, err := BuildRestoreScript(sum, "20260727-120000")
	if err != nil {
		t.Fatalf("BuildRestoreScript: %v", err)
	}

	verify := strings.Index(script, "sha256sum")
	extract := strings.Index(script, "tar -xzf")
	stop := strings.Index(script, "docker compose down")
	aside := strings.Index(script, "cp -a")

	if verify < 0 || extract < 0 || stop < 0 || aside < 0 {
		t.Fatalf("a stage is missing:\n%s", script)
	}
	if verify > stop || verify > extract {
		t.Error("the archive is verified after the machine has already been changed")
	}
	if aside > extract {
		t.Error("the current state is kept aside after it has been overwritten")
	}
	if stop > extract {
		t.Error("the stacks are stopped after their data has moved underneath them")
	}
}

// The checksum is interpolated into a shell script that runs as root, so it is
// validated as a digest rather than trusted to be one.
func TestRestoreRefusesAnythingThatIsNotADigest(t *testing.T) {
	for _, bad := range []string{
		"", "short",
		"'; rm -rf / ;'",
		strings.Repeat("z", 64),
		strings.Repeat("a", 63),
	} {
		if _, err := BuildRestoreScript(bad, "20260727-120000"); err == nil {
			t.Errorf("BuildRestoreScript accepted checksum %q", bad)
		}
	}
	if _, err := BuildRestoreScript(strings.Repeat("ab", 32), "not a stamp"); err == nil {
		t.Error("BuildRestoreScript accepted a stamp that is not a timestamp")
	}
}

// The archive is only useful if what comes back has the same ownership and
// modes: a credential restored world-readable is not what was saved.
func TestArchivePreservesPermissions(t *testing.T) {
	script := BuildArchiveScript()
	if !strings.Contains(script, "tar -czf") {
		t.Error("no archive is produced")
	}
	// GNU tar preserves owner and mode when run as root by default; the point
	// here is that nothing disables it.
	for _, forbidden := range []string{"--no-same-owner", "--no-same-permissions", "--mode="} {
		if strings.Contains(script, forbidden) {
			t.Errorf("the archive discards permissions via %q", forbidden)
		}
	}
	if !strings.Contains(script, "sha256sum") {
		t.Error("the machine does not checksum what it wrote")
	}
}

// Taking a backup must not change the machine.
func TestArchivingOnlyReads(t *testing.T) {
	script := BuildArchiveScript()
	for _, forbidden := range []string{
		"docker compose down", "docker compose up", "rm -rf /opt", "systemctl",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("the archive script contains %q", forbidden)
		}
	}
}

// The archive is for rebuilding a machine, not preserving its history.
// Unfiltered, this fleet's few megabytes of configuration arrived as fourteen
// megabytes of DNS blocklists, printer agent logs and downloaded UI icons.
func TestArchiveLeavesOutWhatRegenerates(t *testing.T) {
	script := BuildArchiveScript()
	for _, pattern := range []string{"*.log", "logs", "blocklists", "cache.bin", ".cache"} {
		if !strings.Contains(script, "--exclude='"+pattern+"'") {
			t.Errorf("the archive does not exclude %q", pattern)
		}
	}
}

// A database copied while its service is running is the dangerous exclusion,
// not merely the large one: the file plus its write-ahead log is an
// inconsistent snapshot, and restoring it can produce a corrupt database
// rather than an old one.
func TestArchiveLeavesOutLiveDatabases(t *testing.T) {
	script := BuildArchiveScript()
	for _, pattern := range []string{"*.db", "*.db-wal", "*.db-shm"} {
		if !strings.Contains(script, "--exclude='"+pattern+"'") {
			t.Errorf("the archive would copy %q from a running service", pattern)
		}
	}
}

// Excluding too much is the other failure. These are what actually rebuilds a
// machine, and none of them may match an exclusion.
func TestExclusionsDoNotSwallowConfiguration(t *testing.T) {
	essential := []string{
		"compose.yaml", "Caddyfile", "configuration.yaml", "secrets.yaml",
		"homepage.env", "admin-password", "lan.zone", "config.json",
		"octoeverywhere.conf", "automations.yaml",
	}
	for _, name := range essential {
		for _, pattern := range Excluded {
			if matched, _ := filepath.Match(pattern, name); matched {
				t.Errorf("%q would be excluded by %q", name, pattern)
			}
		}
	}
}
