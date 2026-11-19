package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

const testDiagnosticsURL = "https://klippee.xyz/log-diagnostics?upload=123e4567-e89b-42d3-a456-426614174000"

func successfulDiagnosticsSummary(url string) string {
	return "klippee: collected 8 diagnostic artifacts | " +
		klippeeDiagnosticsPrivacyMarker + " | " +
		klippeeDiagnosticsURLMarker + url + " | " +
		klippeeDiagnosticsSuccessMarker
}

func TestKlippeeDiagnosticsPollsExactOperationAndPrintsValidatedPrivateURL(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/server/extensions/request" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Agent     string         `json:"agent"`
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Agent != klippeeAgentName {
			t.Errorf("agent = %q", payload.Agent)
		}
		methods = append(methods, payload.Method)
		switch len(methods) {
		case 1:
			if payload.Method != klippeeOperationStatusMethod || payload.Arguments != nil {
				t.Errorf("invalid preflight: %#v", payload)
			}
			_, _ = w.Write([]byte(`{
				"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
			}`))
		case 2:
			if payload.Method != klippeeDiagnosticsMethod || payload.Arguments != nil {
				t.Errorf("invalid upload request: %#v", payload)
			}
			_, _ = w.Write([]byte(`{
				"result":{"accepted":true,"command":"upload-diagnostics","operation_id":"diag-op"}
			}`))
		case 3:
			if payload.Method != klippeeOperationStatusMethod ||
				payload.Arguments["operation_id"] != "diag-op" {
				t.Errorf("invalid exact-operation poll: %#v", payload)
			}
			_, _ = fmt.Fprintf(w, `{
				"result":{
					"running":false,"found":true,"operation_id":"diag-op","current":null,
					"last_result":{
						"id":"diag-op","method":"klippee_upload_diagnostics",
						"command":"upload-diagnostics","status":"succeeded",
						"started_at":"2026-08-06T00:00:00Z","completed_at":"2026-08-06T00:00:02Z",
						"return_code":0,"summary":%q
					}
				}
			}`, successfulDiagnosticsSummary(testDiagnosticsURL))
		default:
			t.Errorf("unexpected extra request")
		}
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	stdout, stderr, err := executeKlippeeCLI(t, configPath, "diagnostics", "printer-a")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	if !strings.Contains(stdout, testDiagnosticsURL) ||
		!strings.Contains(stdout, "moonraker-extension") ||
		!strings.Contains(stdout, "48 hours") {
		t.Fatalf("diagnostics result = %q", stdout)
	}
	if strings.Count(stdout, testDiagnosticsURL) != 1 {
		t.Fatalf("private URL was not rendered exactly once: %q", stdout)
	}
	want := []string{
		klippeeOperationStatusMethod, klippeeDiagnosticsMethod,
		klippeeOperationStatusMethod,
	}
	if fmt.Sprint(methods) != fmt.Sprint(want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
}

func TestKlippeeDiagnosticsRejectsUnsafeOrAmbiguousURLs(t *testing.T) {
	tests := map[string]string{
		"http":           "http://klippee.xyz/log-diagnostics?upload=123e4567-e89b-42d3-a456-426614174000",
		"credentials":    "https://token@klippee.xyz/log-diagnostics?upload=123e4567-e89b-42d3-a456-426614174000",
		"wrong path":     "https://klippee.xyz/other?upload=123e4567-e89b-42d3-a456-426614174000",
		"extra query":    testDiagnosticsURL + "&next=https://example.invalid",
		"invalid id":     "https://klippee.xyz/log-diagnostics?upload=not-an-id",
		"fragment":       testDiagnosticsURL + "#secret",
		"javascript URL": "javascript:alert(1)",
	}
	for name, candidate := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := extractKlippeeDiagnosticsURL(
				successfulDiagnosticsSummary(candidate),
			); err == nil {
				t.Fatal("expected unsafe URL rejection")
			}
		})
	}
	if _, err := extractKlippeeDiagnosticsURL(
		successfulDiagnosticsSummary(testDiagnosticsURL) +
			" | " + klippeeDiagnosticsURLMarker + testDiagnosticsURL,
	); err == nil {
		t.Fatal("expected ambiguous URL rejection")
	}
}

func TestKlippeeDiagnosticsSSHResultRequiresSuccessProofAndRedactsDuplicateURL(t *testing.T) {
	report, err := klippeeDiagnosticsReportFromResult(
		"fixturePrinterA", "ssh", "", strings.ReplaceAll(
			successfulDiagnosticsSummary(testDiagnosticsURL), " | ", "\n",
		)+"\nreported again: "+testDiagnosticsURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.PrivateURL != testDiagnosticsURL || strings.Contains(report.Result, testDiagnosticsURL) {
		t.Fatalf("report = %#v", report)
	}
	if _, err := klippeeDiagnosticsReportFromResult(
		"fixturePrinterA", "ssh", "", klippeeDiagnosticsURLMarker+testDiagnosticsURL,
	); err == nil {
		t.Fatal("expected missing success proof rejection")
	}
}

func TestKlippeeDiagnosticsRunningPreflightDoesNotStartUpload(t *testing.T) {
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Method == klippeeDiagnosticsMethod {
			uploads++
		}
		_, _ = w.Write([]byte(`{
			"result":{"running":true,"found":true,"operation_id":"busy",
			"current":{"id":"busy","method":"klippee_check","command":"check","status":"running","started_at":"2026-08-06T00:00:00Z"},
			"last_result":null}
		}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(t, configPath, "diagnostics", "printer-a")
	if err == nil || !strings.Contains(err.Error(), "quiescence was not proven") {
		t.Fatalf("error = %v", err)
	}
	if uploads != 0 {
		t.Fatalf("busy preflight started %d uploads", uploads)
	}
}

func TestWaitForKlippeeOperationRetriesOnlyTransientStatusTimeout(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			time.Sleep(40 * time.Millisecond)
			return
		}
		_, _ = w.Write([]byte(`{
			"result":{
				"running":false,"found":true,"operation_id":"diag-op","current":null,
				"last_result":{
					"id":"diag-op","method":"klippee_upload_diagnostics",
					"command":"upload-diagnostics","status":"succeeded",
					"started_at":"2026-08-06T00:00:00Z",
					"completed_at":"2026-08-06T00:00:02Z","return_code":0
				}
			}
		}`))
	}))
	defer server.Close()

	client, err := moonraker.New(
		inventory.Printer{MoonrakerURL: server.URL}, 10*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := waitForKlippeeOperation(
		context.Background(), client, "diag-op", 750*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if operation.ID != "diag-op" || calls.Load() != 2 {
		t.Fatalf("operation = %#v; status calls = %d", operation, calls.Load())
	}
}
