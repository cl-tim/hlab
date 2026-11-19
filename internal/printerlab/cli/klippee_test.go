package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"go.yaml.in/yaml/v3"
)

const cliHelperEnvironment = "PLAB_KLIPPEE_CLI_HELPER"

func TestMain(m *testing.M) {
	if runFirmwareWorkflowHelper() {
		os.Exit(0)
	}
	if os.Getenv(cliHelperEnvironment) == "1" {
		cwd, _ := os.Getwd()
		var encodedArgs bytes.Buffer
		encoder := json.NewEncoder(&encodedArgs)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(os.Args[1:])
		argsLine := strings.TrimSpace(encodedArgs.String())
		if path := os.Getenv("PLAB_CLI_ARGS_FILE"); path != "" {
			_ = os.WriteFile(path, []byte(argsLine+"\n"), 0o600)
		}
		if document := os.Getenv("PLAB_MIGRATION_JSON"); document != "" {
			fmt.Println(document)
		} else {
			fmt.Printf("cwd=%s\nargs=%s\n", cwd, argsLine)
		}
		if diagnostic := os.Getenv("PLAB_KLIPPEE_CLI_STDERR"); diagnostic != "" {
			fmt.Fprintln(os.Stderr, diagnostic)
		}
		code, _ := strconv.Atoi(os.Getenv("PLAB_KLIPPEE_CLI_EXIT"))
		os.Exit(code)
	}
	if handled, code := migrate.MaybeRunBackend(context.Background(), os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}
	// The printer inventory defaults to the hlab config. No test may reach the
	// developer's real one, so the hlab home is an empty directory throughout.
	hostHome, err := os.MkdirTemp("", "hlab-printer-cli-test-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Setenv("HLAB_HOME", hostHome)
	os.Unsetenv("HLAB_CONFIG")
	os.Unsetenv("PLAB_CONFIG")
	code := m.Run()
	_ = os.RemoveAll(hostHome)
	os.Exit(code)
}

func writeCLIConnection(t *testing.T, workspace string) {
	t.Helper()
	state := filepath.Join(workspace, ".klippee")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{
		"client_mode": "desktop",
		"config_dir":  workspace,
		"token":       "not-for-cli-output",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "config.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func klippeeCLIConfig(t *testing.T, initialize bool) (string, string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "printer workspace")
	if initialize {
		if err := os.MkdirAll(workspace, 0o700); err != nil {
			t.Fatal(err)
		}
		writeCLIConnection(t, workspace)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"defaults": map[string]any{
			"klippee": map[string]string{
				"executable": executable,
			},
		},
		"printers": []map[string]any{
			{
				"name":    "printer-a",
				"address": "printer-a.example",
				"klippee": map[string]string{"workspace": workspace},
			},
		},
		"groups": map[string][]string{},
	}
	raw, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, workspace
}

func executeKlippeeCLI(t *testing.T, configPath string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	command := NewCommand(strings.NewReader(""), &stdout, &stderr)
	command.SetArgs(append([]string{"--config", configPath, "klippee"}, args...))
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func klippeePhysicalConfig(t *testing.T, moonrakerURL string) string {
	t.Helper()
	payload := map[string]any{
		"printers": []map[string]any{
			{
				"name":         "printer-a",
				"model":        "plus4",
				"address":      "printer-a.example",
				"moonrakerURL": moonrakerURL,
			},
		},
		"groups": map[string][]string{},
	}
	raw, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKlippeePathWorksBeforeConnectionInitialization(t *testing.T) {
	configPath, workspace := klippeeCLIConfig(t, false)
	stdout, _, err := executeKlippeeCLI(t, configPath, "path", "printer-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != workspace {
		t.Fatalf("got %q, want workspace %q", stdout, workspace)
	}
}

func TestKlippeeCommandsForwardExactArguments(t *testing.T) {
	configPath, workspace := klippeeCLIConfig(t, true)
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "target", args: []string{"target", "printer-a"}, want: `["target"]`},
		{name: "check", args: []string{"check", "printer-a"}, want: `["check"]`},
		{name: "check diff", args: []string{"check", "printer-a", "--diff"}, want: `["check","--diff"]`},
		{name: "apply prompts", args: []string{"apply", "printer-a"}, want: `["apply"]`},
		{name: "apply yes", args: []string{"apply", "printer-a", "--yes"}, want: `["apply","--yes"]`},
		{name: "capture", args: []string{"capture", "printer-a"}, want: `["capture"]`},
		{name: "capture note", args: []string{"capture", "printer-a", "--note", "Edited in VS Code & reviewed"}, want: `["capture","--note","Edited in VS Code & reviewed"]`},
		{name: "variable list", args: []string{"variable-list", "printer-a"}, want: `["variables","list"]`},
		{name: "variable set", args: []string{"variable-set", "printer-a", "mcu_serial", "/dev/serial/example"}, want: `["variables","set","mcu_serial","/dev/serial/example"]`},
		{name: "variable unset", args: []string{"variable-unset", "printer-a", "mcu_serial"}, want: `["variables","unset","mcu_serial"]`},
		{name: "variable doctor", args: []string{"variable-doctor", "printer-a"}, want: `["variables","doctor"]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := executeKlippeeCLI(t, configPath, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			if stderr != "" {
				t.Fatalf("unexpected stderr: %q", stderr)
			}
			if !strings.Contains(stdout, "cwd="+workspace) {
				t.Fatalf("wrong command directory: %q", stdout)
			}
			if !strings.Contains(stdout, "args="+test.want) {
				t.Fatalf("got %q, want arguments %s", stdout, test.want)
			}
		})
	}
}

func TestKlippeeApplyNeverConfirmsImplicitly(t *testing.T) {
	configPath, _ := klippeeCLIConfig(t, true)
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	stdout, _, err := executeKlippeeCLI(t, configPath, "apply", "printer-a")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "--yes") {
		t.Fatalf("apply supplied approval implicitly: %q", stdout)
	}
}

func TestGlobalShortYesIsAcceptedBeforeSubcommands(t *testing.T) {
	configPath, _ := klippeeCLIConfig(t, true)
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	var stdout, stderr bytes.Buffer
	command := NewCommand(strings.NewReader(""), &stdout, &stderr)
	command.SetArgs([]string{"--config", configPath, "-y", "klippee", "apply", "printer-a"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `args=["apply","--yes"]`) {
		t.Fatalf("global -y was not forwarded to Klippee apply: %q", stdout.String())
	}
}

func TestKlippeeCommandExitCodeIsPreservedWithoutDuplicateDiagnostic(t *testing.T) {
	configPath, _ := klippeeCLIConfig(t, true)
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "9")
	t.Setenv("PLAB_KLIPPEE_CLI_STDERR", "client diagnostic")
	_, stderr, err := executeKlippeeCLI(t, configPath, "check", "printer-a")
	var exitError *ExitError
	if !errors.As(err, &exitError) || exitError.Code != 9 {
		t.Fatalf("got %v, want exit status 9", err)
	}
	if stderr != "client diagnostic\n" {
		t.Fatalf("plab lost or duplicated the child diagnostic: %q", stderr)
	}
}

func TestKlippeeCommandRejectsMissingFolderConnection(t *testing.T) {
	configPath, workspace := klippeeCLIConfig(t, false)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := executeKlippeeCLI(t, configPath, "target", "printer-a")
	if err == nil || !strings.Contains(err.Error(), "folder-scoped Klippee connection") {
		t.Fatalf("got %v, want folder-scoped connection error", err)
	}
}

func TestKlippeeDeployChecksIdleThenRequestsPhysicalAgentApply(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/printer/objects/query":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/server/extensions/request":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if payload["agent"] != "klippee" || payload["method"] != "klippee_apply" {
				t.Errorf("unexpected extension payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"result":{"accepted":true,"command":"apply"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	stdout, stderr, err := executeKlippeeCLI(t, configPath, "deploy", "printer-a")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "physical Klippee apply accepted") ||
		!strings.Contains(stdout, "asynchronously") ||
		!strings.Contains(stdout, "does not confirm completion") {
		t.Fatalf("deployment acknowledgement is unclear: %q", stdout)
	}
	wantRequests := []string{
		"GET /printer/objects/query",
		"POST /server/extensions/request",
	}
	if fmt.Sprint(requests) != fmt.Sprint(wantRequests) {
		t.Fatalf("request order = %v, want %v", requests, wantRequests)
	}
}

func TestKlippeeDeployFailsClosedWhenPrinterIsPrinting(t *testing.T) {
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			postCalls++
		}
		_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"printing"}}}}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(t, configPath, "deploy", "printer-a")
	if err == nil || !strings.Contains(err.Error(), "deploy blocked") ||
		!strings.Contains(err.Error(), `state is "printing"`) {
		t.Fatalf("got %v, want idle-state rejection", err)
	}
	if postCalls != 0 {
		t.Fatalf("agent request was sent %d times after idle check failed", postCalls)
	}
}

func TestKlippeeDeployRejectsAgentRPCError(t *testing.T) {
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		postCalls++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["method"] != "klippee_apply" {
			t.Errorf("method = %v, want klippee_apply", payload["method"])
		}
		_, _ = w.Write([]byte(`{"error":{"code":-32000,"message":"another Klippee command is already running"}}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(t, configPath, "deploy", "printer-a")
	if err == nil || !strings.Contains(err.Error(), "request failed") ||
		!strings.Contains(err.Error(), "already running") {
		t.Fatalf("got %v, want agent RPC rejection", err)
	}
	if postCalls != 1 {
		t.Fatalf("current method failure made %d POSTs; plab must not auto-fallback", postCalls)
	}
}

func TestKlippeeDeployValidatesAgentAcknowledgement(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
	}{
		{name: "not accepted", response: `{"result":{"accepted":false,"command":"apply"}}`, want: "did not accept"},
		{name: "wrong command", response: `{"result":{"accepted":true,"command":"capture"}}`, want: `acknowledged command "capture"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
					return
				}
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			configPath := klippeePhysicalConfig(t, server.URL)
			_, _, err := executeKlippeeCLI(t, configPath, "deploy", "printer-a")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
}

func TestKlippeePrinterCheckCallsPhysicalAgentWithoutMutationGate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/server/extensions/request" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["agent"] != "klippee" || payload["method"] != "klippee_check" {
			t.Errorf("unexpected extension payload: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"result":{"accepted":true,"command":"check"}}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	stdout, stderr, err := executeKlippeeCLI(t, configPath, "printer-check", "printer-a")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "physical Klippee check accepted") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestKlippeeLegacyFlagsSelectOnlyLegacyAgentMethods(t *testing.T) {
	tests := []struct {
		name            string
		args            []string
		wantMethod      string
		responseCommand string
		wantAction      string
	}{
		{
			name:            "original deploy agent",
			args:            []string{"deploy", "printer-a", "--legacy"},
			wantMethod:      "klippee_pull",
			responseCommand: "pull",
			wantAction:      "apply",
		},
		{
			name:            "original check agent",
			args:            []string{"printer-check", "printer-a", "--legacy"},
			wantMethod:      "klippee_status",
			responseCommand: "status",
			wantAction:      "check",
		},
		{
			name:            "current deploy compatibility alias",
			args:            []string{"deploy", "printer-a", "--legacy"},
			wantMethod:      "klippee_pull",
			responseCommand: "apply",
			wantAction:      "apply",
		},
		{
			name:            "current check compatibility alias",
			args:            []string{"printer-check", "printer-a", "--legacy"},
			wantMethod:      "klippee_status",
			responseCommand: "check",
			wantAction:      "check",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			postCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
					return
				}
				postCalls++
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["method"] != test.wantMethod {
					t.Errorf("method = %v, want %s", payload["method"], test.wantMethod)
				}
				_, _ = fmt.Fprintf(
					w,
					`{"result":{"accepted":true,"command":%q}}`,
					test.responseCommand,
				)
			}))
			defer server.Close()

			configPath := klippeePhysicalConfig(t, server.URL)
			stdout, stderr, err := executeKlippeeCLI(t, configPath, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			if stderr != "" || !strings.Contains(stdout, test.wantAction+" accepted") {
				t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
			}
			if postCalls != 1 {
				t.Fatalf("legacy method called %d times, want exactly once", postCalls)
			}
		})
	}
}

func TestKlippeeOperationStatusRequestsExactOperation(t *testing.T) {
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
		if payload.Agent != "klippee" || payload.Method != "klippee_operation_status" ||
			payload.Arguments["operation_id"] != "op-running" {
			t.Errorf("unexpected operation request: %#v", payload)
		}
		_, _ = w.Write([]byte(`{
			"result": {
				"running": true,
				"found": true,
				"operation_id": "op-running",
				"current": {
					"id": "op-running",
					"method": "klippee_apply",
					"command": "apply",
					"status": "running",
					"started_at": "2026-08-06T00:00:00Z"
				},
				"last_result": null
			}
		}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	stdout, stderr, err := executeKlippeeCLI(
		t, configPath, "operation-status", "printer-a", "op-running",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "operation op-running is running") ||
		!strings.Contains(stdout, "apply") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestKlippeeOperationStatusRejectsMissingRequestedOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"result": {
				"running": false,
				"found": false,
				"operation_id": "old-op",
				"current": null,
				"last_result": null
			}
		}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(t, configPath, "operation-status", "printer-a", "old-op")
	if err == nil || !strings.Contains(err.Error(), "was not found") ||
		!strings.Contains(err.Error(), "current and last") {
		t.Fatalf("got %v, want bounded-retention not-found error", err)
	}
}

func TestKlippeeDeployWaitPreflightsAndReportsSuccessfulOperation(t *testing.T) {
	var extensionMethods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		var payload struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		extensionMethods = append(extensionMethods, payload.Method)
		switch payload.Method {
		case "klippee_apply":
			_, _ = w.Write([]byte(`{
				"result":{"accepted":true,"command":"apply","operation_id":"op-success"}
			}`))
		case "klippee_operation_status":
			if len(extensionMethods) == 1 {
				if payload.Arguments != nil {
					t.Errorf("preflight unexpectedly supplied arguments: %#v", payload.Arguments)
				}
				_, _ = w.Write([]byte(`{
					"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
				}`))
				return
			}
			if payload.Arguments["operation_id"] != "op-success" {
				t.Errorf("poll operation = %v, want op-success", payload.Arguments["operation_id"])
			}
			_, _ = w.Write([]byte(`{
				"result": {
					"running": false,
					"found": true,
					"operation_id": "op-success",
					"current": null,
					"last_result": {
						"id":"op-success",
						"method":"klippee_apply",
						"command":"apply",
						"status":"succeeded",
						"started_at":"2026-08-06T00:00:00Z",
						"completed_at":"2026-08-06T00:00:02Z",
						"return_code":0,
						"summary":"configuration is current"
					}
				}
			}`))
		default:
			t.Errorf("unexpected extension method %q", payload.Method)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	stdout, stderr, err := executeKlippeeCLI(t, configPath, "deploy", "printer-a", "--wait")
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "accepted as operation op-success") ||
		!strings.Contains(stdout, "operation op-success succeeded (return code 0)") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	want := []string{"klippee_operation_status", "klippee_apply", "klippee_operation_status"}
	if fmt.Sprint(extensionMethods) != fmt.Sprint(want) {
		t.Fatalf("extension methods = %v, want %v", extensionMethods, want)
	}
}

func TestKlippeePrinterCaptureRequiresApprovalAndWaitsForExactOperation(t *testing.T) {
	var extensionMethods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		var payload struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		extensionMethods = append(extensionMethods, payload.Method)
		switch payload.Method {
		case "klippee_capture":
			_, _ = w.Write([]byte(`{
				"result":{"accepted":true,"command":"capture","operation_id":"capture-success"}
			}`))
		case "klippee_operation_status":
			if len(extensionMethods) == 1 {
				_, _ = w.Write([]byte(`{
					"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
				}`))
				return
			}
			if payload.Arguments["operation_id"] != "capture-success" {
				t.Errorf("poll operation = %v", payload.Arguments["operation_id"])
			}
			_, _ = w.Write([]byte(`{
				"result":{
					"running":false,"found":true,"operation_id":"capture-success","current":null,
					"last_result":{
						"id":"capture-success","method":"klippee_capture","command":"capture",
						"status":"succeeded","started_at":"2026-08-06T00:00:00Z",
						"completed_at":"2026-08-06T00:00:01Z","return_code":0,
						"summary":"captured generated configuration"
					}
				}
			}`))
		default:
			t.Errorf("unexpected extension method %q", payload.Method)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := klippeePhysicalConfig(t, server.URL)

	_, _, err := executeKlippeeCLI(t, configPath, "printer-capture", "printer-a")
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("got %v, want approval rejection", err)
	}
	if len(extensionMethods) != 0 {
		t.Fatalf("unconfirmed capture made requests: %v", extensionMethods)
	}

	stdout, stderr, err := executeKlippeeCLI(
		t, configPath, "printer-capture", "printer-a",
		"-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "capture capture-success succeeded") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	want := []string{"klippee_operation_status", "klippee_capture", "klippee_operation_status"}
	if fmt.Sprint(extensionMethods) != fmt.Sprint(want) {
		t.Fatalf("extension methods = %v, want %v", extensionMethods, want)
	}
}

func TestKlippeeDeployWaitFailsForNonzeroOperation(t *testing.T) {
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		var payload struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Method == "klippee_apply" {
			_, _ = w.Write([]byte(`{"result":{"accepted":true,"command":"apply","operation_id":"op-failed"}}`))
			return
		}
		statusCalls++
		if statusCalls == 1 {
			_, _ = w.Write([]byte(`{"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"result": {
				"running":false,
				"found":true,
				"operation_id":"op-failed",
				"current":null,
				"last_result":{
					"id":"op-failed","method":"klippee_apply","command":"apply",
					"status":"failed","started_at":"2026-08-06T00:00:00Z",
					"completed_at":"2026-08-06T00:00:01Z","return_code":7,
					"summary":"server rejected applied report"
				}
			}
		}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	stdout, _, err := executeKlippeeCLI(t, configPath, "deploy", "printer-a", "--wait")
	if err == nil || !strings.Contains(err.Error(), `status "failed"`) ||
		!strings.Contains(err.Error(), "return code 7") ||
		!strings.Contains(err.Error(), "server rejected applied report") {
		t.Fatalf("got %v, want completed-operation failure", err)
	}
	if !strings.Contains(stdout, "accepted as operation op-failed") {
		t.Fatalf("accepted operation was not reported before failure: %q", stdout)
	}
}

func TestKlippeeDeployWaitRejectsLegacyBeforeNetworkAccess(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(
		t, configPath, "deploy", "printer-a", "--legacy", "--wait",
	)
	if err == nil || !strings.Contains(err.Error(), "legacy agents do not expose operation status") {
		t.Fatalf("got %v, want non-waitable legacy rejection", err)
	}
	if requests != 0 {
		t.Fatalf("legacy --wait made %d requests before rejection", requests)
	}
}

func TestKlippeeDeployWaitPreflightFailureDoesNotApply(t *testing.T) {
	applyCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		var payload struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Method == "klippee_apply" {
			applyCalls++
		}
		_, _ = w.Write([]byte(`{"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(t, configPath, "deploy", "printer-a", "--wait")
	if err == nil || !strings.Contains(err.Error(), "requires a current Klippee agent") ||
		!strings.Contains(err.Error(), "method not found") {
		t.Fatalf("got %v, want operation-status preflight error", err)
	}
	if applyCalls != 0 {
		t.Fatalf("failed status preflight called apply %d times", applyCalls)
	}
}

func TestKlippeeDeployWaitTimesOutWithoutRetryingApply(t *testing.T) {
	applyCalls := 0
	statusCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		var payload struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Method == "klippee_apply" {
			applyCalls++
			_, _ = w.Write([]byte(`{"result":{"accepted":true,"command":"apply","operation_id":"op-slow"}}`))
			return
		}
		statusCalls++
		if statusCalls == 1 {
			_, _ = w.Write([]byte(`{"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"result": {
				"running":true,"found":true,"operation_id":"op-slow",
				"current":{"id":"op-slow","method":"klippee_apply","command":"apply","status":"running","started_at":"2026-08-06T00:00:00Z"},
				"last_result":null
			}
		}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(
		t, configPath, "deploy", "printer-a", "--wait", "--wait-timeout", "40ms",
	)
	if err == nil || !strings.Contains(err.Error(), "timed out after 40ms") ||
		!strings.Contains(err.Error(), "operation-status before retrying") {
		t.Fatalf("got %v, want bounded timeout guidance", err)
	}
	if applyCalls != 1 {
		t.Fatalf("timed-out wait called apply %d times, want exactly once", applyCalls)
	}
}

func TestKlippeeRestartRequiresApprovalThenChecksIdle(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/printer/objects/query":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/printer/gcode/script":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["script"] != "RESTART" {
				t.Fatalf("unexpected restart payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := klippeePhysicalConfig(t, server.URL)

	_, _, err := executeKlippeeCLI(t, configPath, "restart", "printer-a")
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("got %v, want approval rejection", err)
	}
	if len(requests) != 0 {
		t.Fatalf("unconfirmed restart made requests: %v", requests)
	}

	stdout, stderr, err := executeKlippeeCLI(
		t, configPath, "restart", "printer-a", "-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "Klipper restart requested through Moonraker") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	want := []string{"GET /printer/objects/query", "POST /printer/gcode/script"}
	if fmt.Sprint(requests) != fmt.Sprint(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestKlippeeFirmwareRestartRequiresApprovalThenChecksIdle(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/printer/objects/query":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/printer/gcode/script":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["script"] != "FIRMWARE_RESTART" {
				t.Fatalf("unexpected firmware restart payload: %#v", payload)
			}
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := klippeePhysicalConfig(t, server.URL)

	_, _, err := executeKlippeeCLI(t, configPath, "firmware-restart", "printer-a")
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("got %v, want approval rejection", err)
	}
	if len(requests) != 0 {
		t.Fatalf("unconfirmed firmware restart made requests: %v", requests)
	}

	stdout, stderr, err := executeKlippeeCLI(
		t,
		configPath,
		"firmware-restart",
		"printer-a",
		"-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "Klipper firmware restart requested through Moonraker") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	want := []string{"GET /printer/objects/query", "POST /printer/gcode/script"}
	if fmt.Sprint(requests) != fmt.Sprint(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
}

func TestKlippeeFirmwareRestartBlocksBusyPrinter(t *testing.T) {
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/printer/objects/query" {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"paused"}}}}`))
			return
		}
		if r.Method == http.MethodPost {
			postCalls++
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	_, _, err := executeKlippeeCLI(
		t,
		klippeePhysicalConfig(t, server.URL),
		"firmware-restart",
		"printer-a",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "printer state is \"paused\"") {
		t.Fatalf("got %v, want paused-state rejection", err)
	}
	if postCalls != 0 {
		t.Fatalf("busy firmware restart made %d POST requests", postCalls)
	}
}

func TestKlippeeFirmwareRestartTimeoutIsAmbiguousAndNeverRetried(t *testing.T) {
	postCalls := 0
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/printer/objects/query" {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/printer/gcode/script" {
			postCalls++
			<-release
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	defer close(release)

	_, _, err := executeKlippeeCLI(
		t,
		klippeePhysicalConfig(t, server.URL),
		"firmware-restart",
		"printer-a",
		"-y",
		"--connect-timeout",
		"30ms",
	)
	if err == nil || !strings.Contains(err.Error(), "outcome is ambiguous") ||
		!strings.Contains(err.Error(), "must not be retried") ||
		!strings.Contains(err.Error(), "hlab printer status printer-a") {
		t.Fatalf("got %v, want no-retry ambiguity guidance", err)
	}
	if postCalls != 1 {
		t.Fatalf("firmware restart made %d POST requests, want exactly one", postCalls)
	}
}
