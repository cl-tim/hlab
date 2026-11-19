package journal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/stages"
)

func load(t *testing.T, home, printer string) *Journal {
	t.Helper()
	book, err := Load(Path(home, printer), printer, "plus4-mainline", "digest-a")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return book
}

func TestMissingJournalLoadsEmpty(t *testing.T) {
	book := load(t, t.TempDir(), "printer-a")

	if len(book.Entries) != 0 {
		t.Fatalf("got %d entries, want none", len(book.Entries))
	}
	if got := book.Attempts()["backup"]; got != "" {
		t.Fatalf("got %q, want no recorded attempt", got)
	}
}

func TestAttemptsRoundTrip(t *testing.T) {
	home := t.TempDir()
	book := load(t, home, "printer-a")
	book.Start("backup")
	book.Complete("backup")
	book.Start("bootstrap-host")
	book.Fail("bootstrap-host", errors.New("apt-get failed"))
	if err := book.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded := load(t, home, "printer-a")

	attempts := reloaded.Attempts()
	if attempts["backup"] != stages.AttemptCompleted {
		t.Errorf("backup: got %q, want completed", attempts["backup"])
	}
	if attempts["bootstrap-host"] != stages.AttemptFailed {
		t.Errorf("bootstrap-host: got %q, want failed", attempts["bootstrap-host"])
	}
	entry, _ := reloaded.Entry("bootstrap-host")
	if !strings.Contains(entry.Error, "apt-get failed") {
		t.Errorf("failure reason was not retained: %q", entry.Error)
	}
}

// A stage that started and never finished must survive as `started`. This is
// the record that makes reconciliation refuse to step over a dead flash.
func TestStartedSurvivesWithoutCompletion(t *testing.T) {
	home := t.TempDir()
	book := load(t, home, "printer-a")
	book.Start("flash-toolhead-katapult")
	if err := book.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded := load(t, home, "printer-a")

	if got := reloaded.Attempts()["flash-toolhead-katapult"]; got != stages.AttemptStarted {
		t.Fatalf("got %q, want started", got)
	}
}

func TestCompletingClearsAnEarlierFailure(t *testing.T) {
	book := load(t, t.TempDir(), "printer-a")
	book.Start("backup")
	book.Fail("backup", errors.New("network down"))
	book.Start("backup")
	book.Complete("backup")

	entry, _ := book.Entry("backup")
	if entry.Error != "" {
		t.Fatalf("stale failure retained: %q", entry.Error)
	}
	if entry.Attempt != stages.AttemptCompleted {
		t.Fatalf("got %q, want completed", entry.Attempt)
	}
}

func TestStaleDetectsAChangedSequence(t *testing.T) {
	home := t.TempDir()
	book := load(t, home, "printer-a")
	book.Start("backup")
	if err := book.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	reloaded, err := Load(Path(home, "printer-a"), "printer-a", "plus4-mainline", "digest-b")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reloaded.Stale("digest-b") {
		t.Fatal("a changed graph digest should be reported as stale")
	}
	reloaded.Adopt("digest-b")
	if reloaded.Stale("digest-b") {
		t.Fatal("adopting the new digest should clear staleness")
	}
}

func TestSaveIsAtomic(t *testing.T) {
	home := t.TempDir()
	book := load(t, home, "printer-a")
	book.Start("backup")
	if err := book.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(home, "runs"))
	if err != nil {
		t.Fatalf("read runs: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files, want exactly the journal", len(entries))
	}
}

func TestUnreadableJournalIsAnErrorNotAnEmptyRun(t *testing.T) {
	home := t.TempDir()
	path := Path(home, "printer-a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Silently treating a corrupt journal as "nothing has run" would re-run
	// every destructive stage.
	if _, err := Load(path, "printer-a", "plus4-mainline", "digest-a"); err == nil {
		t.Fatal("expected a parse error for a corrupt journal")
	}
}

func TestUnsupportedSchemaIsRejected(t *testing.T) {
	home := t.TempDir()
	path := Path(home, "printer-a")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":99,"entries":{}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Load(path, "printer-a", "plus4-mainline", "digest-a"); err == nil {
		t.Fatal("expected an unsupported schema error")
	}
}

func TestPathKeepsInventoryNamesInsideTheRunsDirectory(t *testing.T) {
	home := t.TempDir()
	runs := filepath.Join(home, "runs")

	for _, name := range []string{"../escape", "a/b", `..\escape`, "..", "."} {
		path := Path(home, name)
		if filepath.Dir(path) != runs {
			t.Errorf("name %q escaped the runs directory: %s", name, path)
		}
	}
}

func TestPathIsStableForOrdinaryNames(t *testing.T) {
	home := t.TempDir()

	if got := filepath.Base(Path(home, "printer-a")); got != "printer-a.json" {
		t.Fatalf("got %q, want printer-a.json", got)
	}
}
