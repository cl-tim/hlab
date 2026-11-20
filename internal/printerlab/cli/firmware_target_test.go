package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func directFirmwareFixture(t *testing.T, check bool) (plrFixture, string) {
	t.Helper()
	fixture := plrFixture{argsPath: filepath.Join(t.TempDir(), "args.json")}
	result := migrate.QIDIUpdateResult{Schema: 1, OK: true, Status: "checked", Model: "plus4", Target: "2.1.1", Before: "1.8.2", After: "1.8.2", CheckOnly: check, ConfigPolicy: "preserve", ScreenStatus: "pending"}
	if !check {
		result.Status, result.After, result.ScreenStatus = "updated", "2.1.1", "acknowledged"
		result.Backup = "/var/lib/hlab/qidi-updates/" + strings.Repeat("a", 32)
		result.BackupSHA256 = strings.Repeat("b", 64)
		result.LocalBackup = filepath.Join(t.TempDir(), "recovery.tar.gz")
		result.ConfigVerified, result.StartupVerified = true, true
	}
	raw, _ := json.Marshal(result)
	setMigrationHelper(t, fixture, string(raw))
	hostHome := t.TempDir()
	t.Setenv("HLAB_HOME", hostHome)
	broken := filepath.Join(t.TempDir(), "unrelated-config.yaml")
	if err := os.WriteFile(broken, []byte("not: [valid YAML"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, broken)
	return fixture, hostHome
}

func executeDirectFirmware(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := NewCommand(strings.NewReader(""), &stdout, &stderr)
	cmd.SetArgs(append([]string{"firmware", "update"}, args...))
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestDirectQIDIUpdateWorksWithTargetOnlyWithoutReadingOrSavingInventory(t *testing.T) {
	for _, check := range []bool{true, false} {
		name := "apply"
		if check {
			name = "check"
		}
		t.Run(name, func(t *testing.T) {
			fixture, hostHome := directFirmwareFixture(t, check)
			args := []string{"--target", "192.0.2.20", "-o", "json"}
			if check {
				args = append(args, "--check")
			} else {
				args = append(args, "-y")
			}
			stdout, _, err := executeDirectFirmware(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(stdout)) {
				t.Fatalf("invalid output: %s", stdout)
			}
			actual := readMigrationArgs(t, fixture)
			joined := strings.Join(actual, " ")
			for _, want := range []string{"update-qidi", "--printer 192.0.2.20", "--user mks", "--port 22", "--model plus4", "--firmware-version 2.1.1", "--moonraker-port 7125"} {
				if !strings.Contains(joined, want) {
					t.Fatalf("missing %s in %s", want, joined)
				}
			}
			if strings.Contains(joined, "setup-") || strings.Contains(joined, "bootstrap-") {
				t.Fatalf("direct update tried to enrol: %s", joined)
			}
			if _, err := os.Stat(filepath.Join(hostHome, "config.yaml")); !os.IsNotExist(err) {
				t.Fatalf("created host inventory: %v", err)
			}
			if _, err := os.Stat(filepath.Join(config.Home(), "config.yaml")); !os.IsNotExist(err) {
				t.Fatalf("created printer inventory: %v", err)
			}
			if check && strings.Contains(joined, "--confirm") {
				t.Fatal("check forwarded approval")
			}
			if !check && !strings.Contains(joined, "--confirm "+migrate.QIDIUpdateAction) {
				t.Fatal("apply omitted backend approval")
			}
		})
	}
}

func TestDirectQIDITargetForwardsOnlyExplicitConnectionOptions(t *testing.T) {
	fixture, _ := directFirmwareFixture(t, true)
	_, _, err := executeDirectFirmware(t, "--target", "printer.example", "--model", "plus4", "--version", "2.1.1", "--user", "ops", "--ssh-port", "2222", "--identity", "/fixture/ssh key", "--moonraker-port", "17125", "--api-key-env", "FIXTURE_API_KEY", "--firmware-dir", "/fixture/firmware files", "--check", "-o", "json", "--config", "does-not-exist.yaml")
	if err != nil {
		t.Fatal(err)
	}
	actual := readMigrationArgs(t, fixture)
	for flag, want := range map[string]string{"--printer": "printer.example", "--user": "ops", "--printer-user": "mks", "--port": "2222", "--identity": "/fixture/ssh key", "--moonraker-port": "17125", "--api-key-env": "FIXTURE_API_KEY", "--firmware-dir": "/fixture/firmware files"} {
		got := ""
		for i, arg := range actual {
			if arg == flag && i+1 < len(actual) {
				got = actual[i+1]
				break
			}
		}
		if got != want {
			t.Errorf("%s=%q, want %q", flag, got, want)
		}
	}
}

func TestDirectQIDIInvalidTargetsStopBeforeBackend(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"empty", []string{"--target", "", "--check"}, "invalid SSH host"},
		{"url", []string{"--target", "http://printer.example", "--check"}, "invalid SSH host"},
		{"selector", []string{"--target", "@farm", "--check"}, "invalid SSH host"},
		{"options", []string{"--target", "-oProxyCommand=fixture", "--check"}, "invalid SSH host"},
		{"username", []string{"--target", "printer.example", "--user", "bad user", "--check"}, "invalid SSH account"},
		{"ssh-port", []string{"--target", "printer.example", "--ssh-port", "0", "--check"}, "SSH port"},
		{"api-port", []string{"--target", "printer.example", "--moonraker-port", "65536", "--check"}, "--moonraker-port"},
		{"unsupported-model", []string{"--target", "printer.example", "--model", "unknown-qidi", "--check"}, "no reviewed"},
		{"unsupported-version", []string{"--target", "printer.example", "--version", "99.0.0", "--check"}, "no reviewed"},
		{"approval", []string{"--target", "printer.example"}, "-y/--yes"},
		{"mixed", []string{"saved-printer", "--target", "printer.example", "--check"}, "either a saved PRINTER or --target"},
		{"identity-controls", []string{"--target", "printer.example", "--identity", "bad\nkey", "--check"}, "control character"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, _ := directFirmwareFixture(t, true)
			_, _, err := executeDirectFirmware(t, test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %s", err, test.want)
			}
			if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
				t.Fatalf("invalid target reached backend: %v", err)
			}
		})
	}
}

func TestSavedFirmwarePrinterDoesNotAcceptDirectConnectionOverrides(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	setMigrationHelper(t, fixture, `{}`)
	_, _, err := executeFirmwareUpdate(t, fixture.configPath, "fixturePrinterA", "--user", "other", "--check")
	if err == nil || !strings.Contains(err.Error(), "--user requires --target") {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatal("override reached backend")
	}
}
