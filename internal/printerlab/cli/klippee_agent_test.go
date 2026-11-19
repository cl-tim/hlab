package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestKlippeeRefreshAgentUsesReviewedFixedServiceOperation(t *testing.T) {
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		statusCalls++
		var request struct {
			Agent  string `json:"agent"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.Agent != "klippee" || request.Method != klippeeOperationStatusMethod {
			t.Errorf("unexpected quiescence request: %#v", request)
		}
		_, _ = w.Write([]byte(`{
			"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
		}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, `{
		"schema":"qidi-plus4-klippee-agent-refresh/v1","ok":true,
		"service":"klippee-moonraker.service","scope":"user","account":"mks",
		"observed_state":"active","restart_exit":0,"state_exit":0,
		"klipper_actions_performed":false,"config_deployment_performed":false
	}`)

	stdout, stderr, err := executeKlippeeCLI(
		t, fixture.configPath, "refresh-agent", "fixturePrinterA",
		"-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if statusCalls != 1 {
		t.Fatalf("operation-status calls = %d, want 1", statusCalls)
	}
	if !strings.Contains(stdout, "klippee-moonraker.service") ||
		!strings.Contains(stdout, "active") {
		t.Fatalf("result = %q", stdout)
	}
	want := []string{
		fixture.toolPath, "refresh-klippee-agent", "--printer", "fixturePrinterA.example",
		"--user", "plab", "--port", "22", "--printer-user", "mks",
		"--confirm", "REFRESH-KLIPPEE-AGENT",
	}
	got := readMigrationArgs(t, fixture)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestKlippeeRefreshAgentBlocksRunningOperationBeforeFirmwareTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":{
				"running":true,"found":true,"operation_id":"op-active",
				"current":{
					"id":"op-active","method":"klippee_apply","command":"apply",
					"status":"running","started_at":"2026-08-06T00:00:00Z"
				},
				"last_result":null
			}
		}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, `{}`)

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "refresh-agent", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "operation op-active is still running") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("running operation spawned firmware refresh: %v", err)
	}
}

func TestKlippeeRefreshAgentRequiresOperationStatusSupport(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{
			"error":{"code":-32601,"message":"method not found"}
		}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, `{}`)

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "refresh-agent", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "requires current klippee_operation_status support") {
		t.Fatalf("error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("status requests = %d, want exactly 1 with no fallback", requests)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("unsupported status spawned firmware refresh: %v", err)
	}
}

func TestKlippeeRefreshAgentRecoversUnresponsiveAgentOnlyWhenIdle(t *testing.T) {
	statusRequests := 0
	idleReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/server/extensions/request":
			statusRequests++
			_, _ = w.Write([]byte(`{"error":{"code":-32601,"message":"method not found"}}`))
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/printer/objects/query?print_stats":
			idleReads++
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, `{
		"schema":"qidi-plus4-klippee-agent-refresh/v1","ok":true,
		"service":"klippee-moonraker.service","scope":"user","account":"mks",
		"observed_state":"active","restart_exit":0,"state_exit":0,
		"klipper_actions_performed":false,"config_deployment_performed":false
	}`)

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "refresh-agent", "fixturePrinterA", "-y", "--recover-unresponsive",
	)
	if err != nil {
		t.Fatal(err)
	}
	if statusRequests != 1 || idleReads != 1 {
		t.Fatalf("status requests=%d idle reads=%d", statusRequests, idleReads)
	}
}

func TestKlippeeRefreshAgentRejectsStatusWithoutQuiescenceProof(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, `{}`)

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "refresh-agent", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "omitted required field \"running\"") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("unproven status spawned firmware refresh: %v", err)
	}
}

func TestKlippeeRefreshAgentApprovalPrecedesFirmwareTool(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	setMigrationHelper(t, fixture, `{}`)

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "refresh-agent", "fixturePrinterA",
	)
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("missing approval spawned firmware tool: %v", err)
	}
}
