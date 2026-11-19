package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	plabconfig "github.com/cl-tim/hlab/internal/printerlab/config"
)

type renderConfigFixture struct {
	configPath   string
	toolPath     string
	profileDir   string
	beaconSerial string
	outputPath   string
}

func newRenderConfigFixture(t *testing.T) renderConfigFixture {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "qidi-plus4-firmware")
	toolPath := "--internal-migration-backend"
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(repo, "tools", "migration", "qidi-plus4-remote.sh")
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("# fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Join(t.TempDir(), "qidi-plus4-configs", "printers", "mainline-klipper-beacon")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "printer.cfg"), []byte("[printer]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beaconSerial := "/dev/serial/by-id/usb-Beacon_Beacon_RevH_TEST-if00"
	outputPath := filepath.Join(t.TempDir(), "rendered", "fixturePrinterB")
	payload := map[string]any{
		"defaults": map[string]any{
			"firmwareRepo": repo,
			"migration": map[string]string{
				"profileDir": profileDir,
			},
		},
		"printers": []map[string]any{{
			"name": "fixturePrinterB", "address": "fixturePrinterB.example", "model": "plus4",
			"profile": "bundled-profile-must-not-be-used",
			"migration": map[string]string{
				"beaconSerial": beaconSerial,
				"configOutput": outputPath,
			},
		}},
		"groups": map[string][]string{},
	}
	raw, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "printerlab.yaml")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return renderConfigFixture{
		configPath: configPath, toolPath: toolPath, profileDir: profileDir,
		beaconSerial: beaconSerial, outputPath: outputPath,
	}
}

func executeMainlineCLI(t *testing.T, configPath string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := NewCommand(strings.NewReader(""), &stdout, &stderr)
	command.SetArgs(append([]string{"--config", configPath, "firmware", "advanced"}, args...))
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func helperArgsLine(t *testing.T, args ...string) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return "args=" + string(raw)
}

func TestMainlineRenderConfigForwardsInventoryProfileDirectory(t *testing.T) {
	fixture := newRenderConfigFixture(t)
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	t.Setenv(plabconfig.EnvHome, t.TempDir())

	stdout, stderr, err := executeMainlineCLI(
		t, fixture.configPath, "render-config", "--for", "fixturePrinterB",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	want := helperArgsLine(t,
		fixture.toolPath, "render-config", "--profile-dir", fixture.profileDir,
		"--beacon-serial", fixture.beaconSerial, "--output", fixture.outputPath,
	)
	if !strings.Contains(stdout, want) {
		t.Fatalf("got %q, want %q", stdout, want)
	}
}

func TestExplicitProfileOverridesInventoryProfileDirectory(t *testing.T) {
	fixture := newRenderConfigFixture(t)
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	t.Setenv(plabconfig.EnvHome, t.TempDir())

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "render-config", "--for", "fixturePrinterB",
		"--profile", "compatibility-profile",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := helperArgsLine(t,
		fixture.toolPath, "render-config", "--profile", "compatibility-profile",
		"--beacon-serial", fixture.beaconSerial, "--output", fixture.outputPath,
	)
	if !strings.Contains(stdout, want) {
		t.Fatalf("got %q, want %q", stdout, want)
	}
}

func TestExplicitProfileDirectoryOverridesInventoryProfileDirectory(t *testing.T) {
	fixture := newRenderConfigFixture(t)
	override := filepath.Join(t.TempDir(), "alternate-profile")
	if err := os.MkdirAll(override, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	t.Setenv(plabconfig.EnvHome, t.TempDir())

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "render-config", "--for", "fixturePrinterB",
		"--profile-dir", override,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := helperArgsLine(t,
		fixture.toolPath, "render-config", "--profile-dir", override,
		"--beacon-serial", fixture.beaconSerial, "--output", fixture.outputPath,
	)
	if !strings.Contains(stdout, want) {
		t.Fatalf("got %q, want %q", stdout, want)
	}
}

func TestRenderConfigRejectsConflictingOrEmptyProfileSource(t *testing.T) {
	fixture := newRenderConfigFixture(t)
	tests := [][]string{
		{"render-config", "--for", "fixturePrinterB", "--profile", "compatibility", "--profile-dir", fixture.profileDir},
		{"render-config", "--for", "fixturePrinterB", "--profile-dir="},
	}
	for _, args := range tests {
		if _, _, err := executeMainlineCLI(t, fixture.configPath, args...); err == nil {
			t.Fatalf("expected %q to be rejected", args)
		}
	}
}

func TestResumeExecutorForwardsInventoryProfileDirectory(t *testing.T) {
	fixture := newRenderConfigFixture(t)
	cfg, err := plabconfig.Load(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	printer, _ := cfg.Inventory.Printer("fixturePrinterB")
	var stdout, stderr bytes.Buffer
	a := &app{cfg: cfg, in: strings.NewReader(""), out: &stdout, errOut: &stderr}
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	t.Setenv(plabconfig.EnvHome, t.TempDir())

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)
	if executor.Run == nil {
		t.Fatalf("resume cannot run: %v", executor.Missing)
	}
	if err := executor.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := helperArgsLine(t,
		fixture.toolPath, "render-config", "--commissioning", "--profile-dir", fixture.profileDir,
		"--beacon-serial", fixture.beaconSerial, "--output", fixture.outputPath,
	)
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("got %q, want %q", stdout.String(), want)
	}
}
