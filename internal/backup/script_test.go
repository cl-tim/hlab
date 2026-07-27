package backup

import (
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
