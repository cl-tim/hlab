package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// hostFor is a small constructor shared by the config tests.
func hostFor(name, address string) inventory.Host {
	return inventory.Host{Name: name, Address: address, Managed: true, User: "hlab"}
}

func TestEnsureExistsCreatesAUsableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")

	created, err := EnsureExists(path)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if !created {
		t.Error("created = false, want true")
	}

	cfg, err := Load(path)
	if err != nil {
		raw, _ := os.ReadFile(path)
		t.Fatalf("the created config does not load: %v\n%s", err, raw)
	}

	// A config conjured as a side effect must not invent machines.
	if got := len(cfg.Inventory.Hosts()); got != 0 {
		t.Errorf("inventory has %d hosts, want 0: an auto-created config must be empty", got)
	}
	if got := len(cfg.Inventory.Unmanaged()); got != 0 {
		t.Errorf("discovered = %d, want 0", got)
	}

	// It must be immediately usable by both writers.
	if err := AddHost(path, hostFor("box", "198.51.100.1")); err != nil {
		t.Fatalf("AddHost on a scaffolded config: %v", err)
	}
	if _, err := MergeObservations(path, []Observation{{Address: "198.51.100.2"}}, scanOne); err != nil {
		t.Fatalf("MergeObservations on a scaffolded config: %v", err)
	}

	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load after writes: %v", err)
	}
	if len(cfg.Inventory.Managed()) != 1 || len(cfg.Inventory.Unmanaged()) != 1 {
		t.Errorf("hosts = %d, discovered = %d; want 1 and 1",
			len(cfg.Inventory.Managed()), len(cfg.Inventory.Unmanaged()))
	}
}

func TestEnsureExistsLeavesAnExistingConfigAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "# hand written\nhosts:\n  - name: a\n    address: 198.51.100.1\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	created, err := EnsureExists(path)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if created {
		t.Error("created = true, want false for an existing file")
	}

	raw, _ := os.ReadFile(path)
	if string(raw) != original {
		t.Errorf("existing config was modified:\n%s", raw)
	}
}

func TestEnsureExistsUsesTheDefaultLocation(t *testing.T) {
	dir := isolateHome(t)

	created, err := EnsureExists(DefaultPath())
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if !created {
		t.Error("created = false, want true")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.yaml")); err != nil {
		t.Errorf("config not created in hlab's home: %v", err)
	}
}

func TestEnsureExistsRequiresAPath(t *testing.T) {
	if _, err := EnsureExists(""); err == nil {
		t.Fatal("want an error for an empty path")
	}
}
