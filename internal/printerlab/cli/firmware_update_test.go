package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	plabconfig "github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func klipperUpdateDocument(t *testing.T, check bool, status string) string {
	t.Helper()
	before, target := strings.Repeat("a", 40), strings.Repeat("b", 40)
	after := target
	if check {
		after = before
	}
	if status == "current" {
		target, after = before, before
	}
	result := migrate.KlipperUpdateResult{
		Schema: 1, OK: true, Status: status, Account: "mks", CheckOnly: check,
		Before: before, Target: target, After: after, Version: "v0.13.0-787-ge2b99a915-dirty",
		Backup: "/home/mks/printer_data/backups/klipper-update-test",
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func executeFirmwareUpdate(t *testing.T, path string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := NewCommand(strings.NewReader(""), &stdout, &stderr)
	command.SetArgs(append([]string{"--config", path, "firmware", "update"}, args...))
	err := command.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestFirmwareUpdateForwardsGuardedHostOnlyOperation(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	if err := plabconfig.Edit(fixture.configPath, "fixturePrinterA", nil, map[string]string{"firmware": "mainline"}); err != nil {
		t.Fatal(err)
	}
	setMigrationHelper(t, fixture, klipperUpdateDocument(t, false, "updated"))
	stdout, _, err := executeFirmwareUpdate(t, fixture.configPath, "fixturePrinterA", "-y", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"status": "updated"`) {
		t.Fatalf("missing result: %s", stdout)
	}
	args := strings.Join(readMigrationArgs(t, fixture), " ")
	for _, want := range []string{"update-klipper", "--printer-user mks", "--moonraker-port 7125", "--wait-seconds 180", "--confirm UPDATE-KLIPPER"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s: %s", want, args)
		}
	}
	if strings.Contains(args, "flash") || strings.Contains(args, "bootstrap-host") {
		t.Fatalf("unexpected firmware action: %s", args)
	}
}

func TestFirmwareUpdateCheckDoesNotRequireApproval(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	if err := plabconfig.Edit(fixture.configPath, "fixturePrinterA", nil, map[string]string{"firmware": "mainline", "apiKeyEnv": "TEST_UPDATE_API_KEY"}); err != nil {
		t.Fatal(err)
	}
	setMigrationHelper(t, fixture, klipperUpdateDocument(t, true, "available"))
	_, _, err := executeFirmwareUpdate(t, fixture.configPath, "fixturePrinterA", "--check")
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(readMigrationArgs(t, fixture), " ")
	if !strings.Contains(args, "--check") || strings.Contains(args, "--confirm") || !strings.Contains(args, "--api-key-env TEST_UPDATE_API_KEY") {
		t.Fatalf("incorrect check-only arguments: %s", args)
	}
}

func TestFirmwareUpdateRejectsMissingApprovalStockAndInvalidTimeoutBeforeSSH(t *testing.T) {
	for _, test := range []struct {
		name, firmware string
		args           []string
		want           string
	}{
		{"approval", "mainline", nil, "-y/--yes"},
		{"stock", "stock", []string{"-y"}, "--version"},
		{"timeout", "mainline", []string{"-y", "--wait-timeout", "1s"}, "whole number"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPLRFixture(t, "http://127.0.0.1:1")
			if err := plabconfig.Edit(fixture.configPath, "fixturePrinterA", nil, map[string]string{"firmware": test.firmware}); err != nil {
				t.Fatal(err)
			}
			setMigrationHelper(t, fixture, `{}`)
			_, _, err := executeFirmwareUpdate(t, fixture.configPath, append([]string{"fixturePrinterA"}, test.args...)...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
			if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
				t.Fatalf("invalid update launched SSH backend: %v", err)
			}
		})
	}
}

func TestFirmwareUpdateSurfacesRollbackFailureReceipt(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	if err := plabconfig.Edit(fixture.configPath, "fixturePrinterA", nil, map[string]string{"firmware": "mainline"}); err != nil {
		t.Fatal(err)
	}
	var result migrate.KlipperUpdateResult
	json.Unmarshal([]byte(klipperUpdateDocument(t, false, "updated")), &result)
	result.OK, result.Status, result.Error, result.RollbackError = false, "rollback_failed", "new host failed", "old host failed"
	raw, _ := json.Marshal(result)
	setMigrationHelper(t, fixture, string(raw))
	stdout, _, err := executeFirmwareUpdate(t, fixture.configPath, "fixturePrinterA", "-y", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "old host failed") || !strings.Contains(stdout, result.Backup) {
		t.Fatalf("failure receipt lost: stdout=%s error=%v", stdout, err)
	}
}
