package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfig drops a config file into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "homelab.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := writeConfig(t, `
defaults:
  user: root
  port: 2222
  connectTimeout: 5s
  concurrency: 4
hosts:
  - name: a
    address: 198.51.100.1
  - name: b
    address: 198.51.100.2
    port: 22
    user: youruser
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Defaults.ConnectTimeout.Duration() != 5*time.Second {
		t.Errorf("ConnectTimeout = %v, want 5s", cfg.Defaults.ConnectTimeout)
	}
	if cfg.Defaults.Concurrency != 4 {
		t.Errorf("Concurrency = %d, want 4", cfg.Defaults.Concurrency)
	}

	a, ok := cfg.Inventory.Host("a")
	if !ok {
		t.Fatal("host a missing")
	}
	if a.Port != 2222 {
		t.Errorf("a.Port = %d, want 2222 (inherited)", a.Port)
	}
	if a.User != "root" {
		t.Errorf("a.User = %q, want root (inherited)", a.User)
	}

	b, _ := cfg.Inventory.Host("b")
	if b.Port != 22 {
		t.Errorf("b.Port = %d, want 22 (explicit override)", b.Port)
	}
	if b.User != "youruser" {
		t.Errorf("b.User = %q, want youruser (explicit override)", b.User)
	}
}

func TestLoadBuiltinDefaults(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - name: a
    address: 198.51.100.1
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Defaults.Port != 22 {
		t.Errorf("Port = %d, want 22", cfg.Defaults.Port)
	}
	if cfg.Defaults.Concurrency != 10 {
		t.Errorf("Concurrency = %d, want 10", cfg.Defaults.Concurrency)
	}
	if cfg.Defaults.ConnectTimeout.Duration() != 10*time.Second {
		t.Errorf("ConnectTimeout = %v, want 10s", cfg.Defaults.ConnectTimeout)
	}

	a, _ := cfg.Inventory.Host("a")
	if a.Port != 22 {
		t.Errorf("a.Port = %d, want 22", a.Port)
	}
}

func TestAddressDefaultsToName(t *testing.T) {
	path := writeConfig(t, `
hosts:
  - name: nas.lan
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h, _ := cfg.Inventory.Host("nas.lan")
	if h.Address != "nas.lan" {
		t.Errorf("Address = %q, want nas.lan", h.Address)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	// A typo in the config should fail loudly rather than be ignored.
	path := writeConfig(t, `
hosts:
  - name: a
    address: 198.51.100.1
    tagz: [oops]
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for unknown key, got nil")
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	path := writeConfig(t, `
defaults:
  connectTimeout: "not-a-duration"
hosts:
  - name: a
    address: 198.51.100.1
`)

	if _, err := Load(path); err == nil {
		t.Fatal("Load: want error for bad duration, got nil")
	}
}

func TestDurationAcceptsBareSeconds(t *testing.T) {
	path := writeConfig(t, `
defaults:
  connectTimeout: 30
hosts:
  - name: a
    address: 198.51.100.1
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Defaults.ConnectTimeout.Duration(); got != 30*time.Second {
		t.Errorf("ConnectTimeout = %v, want 30s", got)
	}
}

func TestLoadEmptyFile(t *testing.T) {
	path := writeConfig(t, "")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Inventory.Hosts()) != 0 {
		t.Errorf("Hosts = %d, want 0", len(cfg.Inventory.Hosts()))
	}
}

func TestLoadMissingExplicitPathIsError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("Load: want error for missing explicit path, got nil")
	}
}

func TestLoadNoConfigIsNotAnError(t *testing.T) {
	// Isolate from any real config on the machine.
	t.Setenv(EnvConfigPath, "")
	isolateHome(t)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty", cfg.Path)
	}
	if len(cfg.Inventory.Hosts()) != 0 {
		t.Errorf("Hosts = %d, want 0", len(cfg.Inventory.Hosts()))
	}
}

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got := expandHome("~/.ssh/id_ed25519")
	want := filepath.Join(home, ".ssh", "id_ed25519")
	if got != want {
		t.Errorf("expandHome = %q, want %q", got, want)
	}

	if got := expandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("expandHome(/abs/path) = %q, want unchanged", got)
	}
	if got := expandHome(""); got != "" {
		t.Errorf("expandHome(\"\") = %q, want empty", got)
	}
}

func TestIdentityFileTildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path := writeConfig(t, `
defaults:
  identityFile: ~/.ssh/id_ed25519
hosts:
  - name: a
    address: 198.51.100.1
  - name: b
    address: 198.51.100.2
    identityFile: ~/.ssh/other
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	a, _ := cfg.Inventory.Host("a")
	if want := filepath.Join(home, ".ssh", "id_ed25519"); a.IdentityFile != want {
		t.Errorf("a.IdentityFile = %q, want %q", a.IdentityFile, want)
	}

	b, _ := cfg.Inventory.Host("b")
	if want := filepath.Join(home, ".ssh", "other"); b.IdentityFile != want {
		t.Errorf("b.IdentityFile = %q, want %q", b.IdentityFile, want)
	}
}
