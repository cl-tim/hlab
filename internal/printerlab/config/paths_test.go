package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The printer half holds the same line as the host half: nothing is read from
// or defaulted into the directory hlab happens to be run from, which is
// frequently a git checkout.
func TestPrinterDataNeverDefaultsIntoTheWorkingDirectory(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv(EnvConfigPath, "")

	for _, decoy := range []string{"config.yaml", "printerlab.yaml"} {
		body := "printers:\n  - name: decoy\n    address: 198.51.100.1\n"
		if err := os.WriteFile(filepath.Join(repo, decoy), []byte(body), 0o600); err != nil {
			t.Fatalf("write decoy: %v", err)
		}
	}

	if got := DefaultPath(); got != filepath.Join(home, "config.yaml") {
		t.Errorf("DefaultPath() = %q, want it under %s", got, EnvHome)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "" || len(cfg.Inventory.Printers()) != 0 {
		t.Errorf("Load picked up %q with %d printer(s) from the working directory",
			cfg.Path, len(cfg.Inventory.Printers()))
	}
	for name, path := range map[string]string{
		"ArtifactDir":  cfg.Defaults.Migration.ArtifactDir,
		"ConfigOutput": cfg.Defaults.Migration.ConfigOutput,
	} {
		if !filepath.IsAbs(path) || strings.HasPrefix(path, repo) {
			t.Errorf("%s = %q, want an absolute path outside the working directory", name, path)
		}
	}
}

// An explicit path that is wrong must fail rather than quietly mean "no
// inventory".
func TestMissingExplicitInventoryIsAnError(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	t.Setenv(EnvConfigPath, "")
	if _, err := Load(missing); err == nil {
		t.Error("Load of a missing explicit path did not error")
	}
	t.Setenv(EnvConfigPath, missing)
	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), EnvConfigPath) {
		t.Errorf("Load with a missing %s: error = %v, want one naming the variable", EnvConfigPath, err)
	}
}

func TestConnectTimeoutAcceptsBareSeconds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("defaults:\n  connectTimeout: 30\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Defaults.ConnectTimeout.Duration().Seconds(); got != 30 {
		t.Errorf("connectTimeout = %vs, want 30s", got)
	}
}
