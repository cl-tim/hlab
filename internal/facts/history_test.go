package facts

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHistorySurvivesPartialCollection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "facts.yaml")
	host := Facts{Kernel: "old", Services: []Service{{Name: "a.service"}, {Name: "b.service"}}}
	now := time.Now()
	save := func(f Facts) {
		t.Helper()
		now = now.Add(time.Second)
		if err := SaveStore(path, Store{"host": SnapshotOf(f, now)}); err != nil {
			t.Fatal(err)
		}
	}
	save(host)
	save(Facts{Partial: true})
	host.Kernel = "new"
	host.Services[0], host.Services[1] = host.Services[1], host.Services[0]
	save(host)
	save(host)
	store, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	history := store["host"].History
	if len(history) != 1 || history[0].Before != "old" || history[0].After != "new" || history[0].Field != "kernel" {
		t.Fatalf("history = %+v", history)
	}
}
