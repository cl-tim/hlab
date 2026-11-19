package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func executeCancelPrintCLI(t *testing.T, configPath string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := NewCommand(strings.NewReader(""), &stdout, &stderr)
	command.SetArgs(append([]string{"--config", configPath}, args...))
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func TestCancelPrintUsesFixedMacroAndVerifiesTerminalState(t *testing.T) {
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/printer/objects/query?print_stats":
			reads++
			state := "paused"
			if reads == 2 {
				state = "cancelled"
			}
			_, _ = fmt.Fprintf(w, `{"result":{"status":{"print_stats":{"state":%q}}}}`, state)
		case r.Method == http.MethodPost && r.URL.Path == "/printer/gcode/script":
			var payload struct {
				Script string `json:"script"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Script != "CANCEL_PRINT" {
				t.Fatalf("script = %q", payload.Script)
			}
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := executeCancelPrintCLI(
		t, klippeePhysicalConfig(t, server.URL), "cancel-print", "printer-a", "-y", "-o", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || reads != 2 || !strings.Contains(stdout, `"result": "cancelled"`) {
		t.Fatalf("stdout=%q stderr=%q reads=%d", stdout, stderr, reads)
	}
}

func TestCancelPrintRequiresApprovalBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))
	defer server.Close()

	_, _, err := executeCancelPrintCLI(
		t, klippeePhysicalConfig(t, server.URL), "cancel-print", "printer-a",
	)
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("missing approval made %d requests", requests)
	}
}
