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

func validKlippeeAgentUpgradeJSON(t *testing.T) string {
	t.Helper()
	document := map[string]any{
		"schema":     migrate.KlippeeAgentUpgradeSchema,
		"ok":         true,
		"changed":    true,
		"account":    migrate.KlippeeAgentAccount,
		"cli_path":   migrate.KlippeeAgentCLIPath,
		"source_url": migrate.KlippeeAgentOfficialSource,
		"before": map[string]any{
			"version": "2026.08.06.5", "sha256": strings.Repeat("a", 64),
		},
		"after": map[string]any{
			"version": "2026.08.06.6", "sha256": strings.Repeat("b", 64),
		},
		"service":                     migrate.KlippeeAgentService,
		"scope":                       "user",
		"observed_state":              "active",
		"before_pid":                  1234,
		"after_pid":                   5678,
		"upgrade_exit":                0,
		"state_exit":                  0,
		"upgrade_reported_success":    true,
		"agent_restart_performed":     true,
		"klipper_actions_performed":   false,
		"config_deployment_performed": false,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mutateKlippeeAgentUpgradeJSON(
	t *testing.T,
	mutate func(map[string]any),
) string {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(validKlippeeAgentUpgradeJSON(t)), &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestKlippeeUpgradeAgentUsesOnlyFixedFirmwareOperation(t *testing.T) {
	calls := 0
	server := quiescentKlippeeServer(t, &calls)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, validKlippeeAgentUpgradeJSON(t))

	stdout, stderr, err := executeKlippeeCLI(
		t, fixture.configPath, "upgrade-agent", "fixturePrinterA",
		"-y", "--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || calls != 1 {
		t.Fatalf("stderr = %q; operation-status calls = %d", stderr, calls)
	}
	var result migrate.KlippeeAgentUpgradeResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("output = %q: %v", stdout, err)
	}
	if !result.Succeeded() || !result.DidChange() ||
		result.Before.Version != "2026.08.06.5" || result.After.Version != "2026.08.06.6" {
		t.Fatalf("result = %#v", result)
	}
	want := []string{
		fixture.toolPath, "upgrade-klippee-agent",
		"--printer", "fixturePrinterA.example", "--user", "plab", "--port", "22",
		"--printer-user", "mks", "--confirm", migrate.KlippeeAgentUpgradeAction,
	}
	got := readMigrationArgs(t, fixture)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	joined := strings.Join(got, " ")
	for _, forbidden := range []string{"--source", "--cli-path", "--service", "restart-klipper"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("fixed-purpose wrapper exposed forbidden argument %q: %#v", forbidden, got)
		}
	}
}

func TestKlippeeUpgradeAgentBlocksRunningOperationBeforeFirmwareTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":{"running":true,"found":true,"operation_id":"busy",
			"current":{"id":"busy","method":"klippee_apply","command":"apply","status":"running","started_at":"2026-08-06T00:00:00Z"},
			"last_result":null}
		}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, validKlippeeAgentUpgradeJSON(t))

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "upgrade-agent", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "operation busy is still running") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("running operation spawned firmware tool: %v", err)
	}
}

func TestKlippeeUpgradeAgentApprovalPrecedesNetworkAndTool(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	setMigrationHelper(t, fixture, validKlippeeAgentUpgradeJSON(t))

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "upgrade-agent", "fixturePrinterA",
	)
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("missing approval spawned firmware tool: %v", err)
	}
}

func TestKlippeeUpgradeAgentRequiresInventoryMKSAccountBeforeNetwork(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	raw, err := os.ReadFile(fixture.configPath)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(raw), "printerUser: mks", "printerUser: root", 1)
	if updated == string(raw) {
		t.Fatalf("fixture did not contain printerUser: %s", raw)
	}
	if err := os.WriteFile(fixture.configPath, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	setMigrationHelper(t, fixture, validKlippeeAgentUpgradeJSON(t))

	_, _, err = executeKlippeeCLI(
		t, fixture.configPath, "upgrade-agent", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), `printer account "mks"`) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("wrong inventory account spawned firmware tool: %v", err)
	}
}

func TestKlippeeUpgradeAgentSurfacesStructuredFailureWithoutRetry(t *testing.T) {
	calls := 0
	server := quiescentKlippeeServer(t, &calls)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := mutateKlippeeAgentUpgradeJSON(t, func(document map[string]any) {
		document["ok"] = false
		document["changed"] = false
		document["after"] = map[string]any{
			"version": "2026.08.06.5", "sha256": strings.Repeat("a", 64),
		}
		document["after_pid"] = 1234
		document["upgrade_exit"] = 1
		document["upgrade_reported_success"] = false
		document["agent_restart_performed"] = false
		document["error"] = "official upgrade failed"
	})
	setMigrationHelper(t, fixture, document)
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "1")

	stdout, _, err := executeKlippeeCLI(
		t, fixture.configPath, "upgrade-agent", "fixturePrinterA",
		"-y", "--output", "json",
	)
	if err == nil || !strings.Contains(err.Error(), "official upgrade failed") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(stdout, "official upgrade failed") || calls != 1 {
		t.Fatalf("stdout = %q; calls = %d", stdout, calls)
	}
	if got := readMigrationArgs(t, fixture); len(got) == 0 {
		t.Fatal("firmware operation was not invoked exactly once")
	}
}

func TestKlippeeUpgradeAgentRejectsUnsafeResult(t *testing.T) {
	calls := 0
	server := quiescentKlippeeServer(t, &calls)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := mutateKlippeeAgentUpgradeJSON(t, func(document map[string]any) {
		document["klipper_actions_performed"] = true
	})
	setMigrationHelper(t, fixture, document)

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "upgrade-agent", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "zero Klipper actions") {
		t.Fatalf("error = %v", err)
	}
}
