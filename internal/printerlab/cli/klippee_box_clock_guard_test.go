package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func validBoxClockGuardInstallJSON(t *testing.T) string {
	t.Helper()
	digest := strings.Repeat("a", 64)
	transaction := "20260806T010203Z-0123456789abcdef"
	document := map[string]any{
		"schema":         migrate.BoxClockGuardInstallSchema,
		"ok":             true,
		"changed":        true,
		"applied":        true,
		"verified":       true,
		"account":        migrate.BoxClockGuardAccount,
		"transaction_id": transaction,
		"source_sha256":  digest,
		"target": map[string]any{
			"path": migrate.BoxClockGuardTargetPath, "sha256": digest,
			"owner": "mks", "group": "mks", "mode": "0644",
		},
		"backup": map[string]any{
			"path": "/home/mks/klipper/klippy/extras/.qidi-plus4-backups/" +
				"box-output-clock-guard-" + transaction + "/box_output_clock_guard.py",
			"sha256": strings.Repeat("b", 64), "owner": "mks", "group": "mks", "mode": "0644",
		},
		"klipper_service_action":   "none",
		"klipper_restarted":        false,
		"config_changed":           false,
		"activation_required":      true,
		"required_config_section":  migrate.BoxClockGuardConfigSection,
		"staging_cleanup_verified": true,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mutateBoxClockGuardInstallJSON(
	t *testing.T,
	mutate func(map[string]any),
) string {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(validBoxClockGuardInstallJSON(t)), &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func quiescentKlippeeServer(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var request struct {
			Agent  string `json:"agent"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Agent != klippeeAgentName || request.Method != klippeeOperationStatusMethod {
			t.Errorf("unexpected request: %#v", request)
		}
		_, _ = w.Write([]byte(`{
			"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
		}`))
	}))
}

func TestKlippeeInstallBoxClockGuardUsesOnlyReviewedFirmwareOperation(t *testing.T) {
	calls := 0
	server := quiescentKlippeeServer(t, &calls)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, validBoxClockGuardInstallJSON(t))

	stdout, stderr, err := executeKlippeeCLI(
		t, fixture.configPath, "install-box-output-clock-guard", "fixturePrinterA",
		"-y", "--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || calls != 1 {
		t.Fatalf("stderr = %q; operation-status calls = %d", stderr, calls)
	}
	var result migrate.BoxClockGuardInstallResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("output = %q: %v", stdout, err)
	}
	if !result.Succeeded() || result.RequiredConfigSection != migrate.BoxClockGuardConfigSection {
		t.Fatalf("result = %#v", result)
	}
	want := []string{
		fixture.toolPath, "install-box-output-clock-guard",
		"--printer", "fixturePrinterA.example", "--user", "plab", "--port", "22",
		"--printer-user", "mks", "--confirm", migrate.BoxClockGuardInstallAction,
	}
	got := readMigrationArgs(t, fixture)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{"--source", "--target", "--mode", "restart", "RESTART"} {
		if strings.Contains(strings.Join(got, " "), forbidden) {
			t.Fatalf("fixed-purpose wrapper exposed forbidden argument %q: %#v", forbidden, got)
		}
	}
}

func TestKlippeeInstallBoxClockGuardFailsAfterSurfacingCleanupProof(t *testing.T) {
	calls := 0
	server := quiescentKlippeeServer(t, &calls)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := mutateBoxClockGuardInstallJSON(t, func(document map[string]any) {
		document["staging_cleanup_verified"] = false
		document["staging_cleanup_error"] = "staged file remains"
	})
	setMigrationHelper(t, fixture, document)

	stdout, _, err := executeKlippeeCLI(
		t, fixture.configPath, "install-box-output-clock-guard", "fixturePrinterA",
		"-y", "--output", "json",
	)
	if err == nil || !strings.Contains(err.Error(), "staging cleanup was not verified") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stdout, migrate.BoxClockGuardTargetPath) ||
		!strings.Contains(stdout, "box-output-clock-guard-20260806") {
		t.Fatalf("successful install and backup proof were not surfaced: %q", stdout)
	}
}

func TestKlippeeInstallBoxClockGuardBlocksRunningAgentWork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":{"running":true,"found":true,"operation_id":"busy",
			"current":{"id":"busy","method":"klippee_apply","command":"apply","status":"running","started_at":"2026-08-06T00:00:00Z"},
			"last_result":null}
		}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, validBoxClockGuardInstallJSON(t))

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "install-box-output-clock-guard", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "operation busy is still running") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("running operation spawned firmware tool: %v", err)
	}
}

func TestKlippeeInstallBoxClockGuardApprovalPrecedesNetworkAndTool(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	setMigrationHelper(t, fixture, validBoxClockGuardInstallJSON(t))

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "install-box-output-clock-guard", "fixturePrinterA",
	)
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("missing approval spawned firmware tool: %v", err)
	}
}

func TestKlippeeInstallBoxClockGuardRejectsUnsafeFirmwareProof(t *testing.T) {
	calls := 0
	server := quiescentKlippeeServer(t, &calls)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := mutateBoxClockGuardInstallJSON(t, func(document map[string]any) {
		document["klipper_restarted"] = true
	})
	setMigrationHelper(t, fixture, document)

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "install-box-output-clock-guard", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "zero Klipper") {
		t.Fatalf("error = %v", err)
	}
}
