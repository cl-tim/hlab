package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostconfig "github.com/cl-tim/hlab/internal/config"
)

// isolate points both homes at empty temp directories and clears every path
// override, so a test can never read the developer's real inventory.
func isolate(t *testing.T) (hostHome, printerHome string) {
	t.Helper()
	hostHome, printerHome = t.TempDir(), t.TempDir()
	t.Setenv(hostconfig.EnvHome, hostHome)
	t.Setenv(hostconfig.EnvConfigPath, "")
	t.Setenv(EnvHome, printerHome)
	t.Setenv(EnvConfigPath, "")
	return hostHome, printerHome
}

func write(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The printer half holds the same line as the host half: nothing is read from
// or defaulted into the directory hlab happens to be run from, which is
// frequently a git checkout.
func TestPrinterDataNeverDefaultsIntoTheWorkingDirectory(t *testing.T) {
	repo := t.TempDir()
	t.Chdir(repo)
	hostHome, _ := isolate(t)

	for _, decoy := range []string{"config.yaml", "printerlab.yaml", "homelab.yaml"} {
		write(t, filepath.Join(repo, decoy), "printers:\n  - name: decoy\n    address: 198.51.100.1\n")
	}

	if got := UnifiedPath(); got != filepath.Join(hostHome, "config.yaml") {
		t.Errorf("UnifiedPath() = %q, want it under the hlab home", got)
	}
	if got := WritePath(""); got != filepath.Join(hostHome, "config.yaml") {
		t.Errorf("WritePath() = %q, want it under the hlab home", got)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Path != "" || cfg.Present || len(cfg.Inventory.Printers()) != 0 {
		t.Errorf("Load picked up %q with %d printer(s) from the working directory",
			cfg.Path, len(cfg.Inventory.Printers()))
	}
	for name, path := range map[string]string{
		"LegacyPath":   LegacyPath(),
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
func TestMissingNamedConfigIsAnError(t *testing.T) {
	isolate(t)
	missing := filepath.Join(t.TempDir(), "nope.yaml")

	if _, err := Load(missing); err == nil {
		t.Error("Load of a missing explicit path did not error")
	}
	for _, env := range []string{EnvConfigPath, hostconfig.EnvConfigPath} {
		t.Setenv(env, missing)
		if _, err := Load(""); err == nil || !strings.Contains(err.Error(), env) {
			t.Errorf("Load with a missing %s: error = %v, want one naming the variable", env, err)
		}
		t.Setenv(env, "")
	}
}

func TestConnectTimeoutAcceptsBareSeconds(t *testing.T) {
	path := write(t, filepath.Join(t.TempDir(), "config.yaml"), "defaults:\n  connectTimeout: 30\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Defaults.ConnectTimeout.Duration().Seconds(); got != 30 {
		t.Errorf("connectTimeout = %vs, want 30s", got)
	}
}
