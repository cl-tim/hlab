package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome points hlab's storage at a temp directory and returns it.
func isolateHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	return dir
}

func TestHomeHonoursOverride(t *testing.T) {
	dir := isolateHome(t)

	if got := Home(); got != dir {
		t.Errorf("Home() = %q, want %q", got, dir)
	}
	if got := DefaultPath(); got != filepath.Join(dir, "config.yaml") {
		t.Errorf("DefaultPath() = %q", got)
	}
	if got := ManagementKeyPath(); got != filepath.Join(dir, "id_hlab") {
		t.Errorf("ManagementKeyPath() = %q", got)
	}
}

func TestHomeDefaultsUnderUserHome(t *testing.T) {
	t.Setenv(EnvHome, "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no user home directory available: %v", err)
	}

	got := Home()
	if !strings.HasPrefix(got, home) {
		t.Errorf("Home() = %q, want it under the user's home directory %q", got, home)
	}
}

// TestStoredDataNeverDefaultsIntoTheWorkingDirectory is the regression guard
// for the whole point of this layout: nothing hlab stores may default into the
// directory it happens to be run from, because that is frequently a git
// checkout and one of those files is a root-equivalent private key.
func TestStoredDataNeverDefaultsIntoTheWorkingDirectory(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	isolateHome(t)

	// A config file sitting in the working directory must not be picked up.
	decoy := filepath.Join(repo, "homelab.yaml")
	if err := os.WriteFile(decoy, []byte("hosts:\n  - name: decoy\n    address: 198.51.100.1\n"), 0o600); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	for _, p := range SearchPaths() {
		if !filepath.IsAbs(p) {
			t.Errorf("SearchPaths() contains a relative path %q", p)
		}
		if strings.HasPrefix(p, repo) {
			t.Errorf("SearchPaths() would search the working directory: %q", p)
		}
	}

	for name, path := range map[string]string{
		"DefaultPath":       DefaultPath(),
		"ManagementKeyPath": ManagementKeyPath(),
	} {
		if !filepath.IsAbs(path) {
			t.Errorf("%s() = %q, want an absolute path", name, path)
		}
		if strings.HasPrefix(path, repo) {
			t.Errorf("%s() = %q, which is inside the working directory", name, path)
		}
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "" {
		t.Errorf("Load picked up %q from the working directory", cfg.Path)
	}
	if _, found := cfg.Inventory.Host("decoy"); found {
		t.Error("the decoy config in the working directory was loaded")
	}
}

func TestExplicitConfigInWorkingDirectoryStillWorks(t *testing.T) {
	// Per-project inventories remain possible — they just have to be asked for.
	repo := t.TempDir()
	t.Chdir(repo)
	isolateHome(t)

	path := filepath.Join(repo, "homelab.yaml")
	if err := os.WriteFile(path, []byte("hosts:\n  - name: proj\n    address: 198.51.100.1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Inventory.Host("proj"); !ok {
		t.Error("explicit --config was not honoured")
	}

	// Even then, the key stays out of the checkout.
	if strings.HasPrefix(ManagementKeyPath(), repo) {
		t.Errorf("ManagementKeyPath() = %q, inside the project", ManagementKeyPath())
	}
}

func TestEnvConfigPathIsHonoured(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()

	path := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(path, []byte("hosts:\n  - name: env\n    address: 198.51.100.1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv(EnvConfigPath, path)

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
}

func TestLoadFindsConfigInHome(t *testing.T) {
	dir := isolateHome(t)
	t.Setenv(EnvConfigPath, "")

	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("hosts:\n  - name: home\n    address: 198.51.100.1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != path {
		t.Errorf("Path = %q, want %q", cfg.Path, path)
	}
	if _, ok := cfg.Inventory.Host("home"); !ok {
		t.Error("host from the home config missing")
	}
}
