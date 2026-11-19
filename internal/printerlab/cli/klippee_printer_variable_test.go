package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
)

func TestKlippeePrinterVariableSetUsesFixedVerifiedOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/printer/objects/query" {
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/server/extensions/request" {
			_, _ = w.Write([]byte(`{
				"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
			}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	value := "/dev/serial/by-id/usb-QIDI_TEST"
	document := map[string]any{
		"schema": migrate.KlippeeVariableSetSchema, "ok": true,
		"account": "mks", "cli_path": migrate.KlippeeVariableCLIPath,
		"name": "qidi_box_serial", "value_sha256": migrate.KlippeeVariableValueSHA256(value),
		"set_return_code": 0, "doctor_return_code": 0, "verified": true,
		"config_deployed": false, "klipper_action": "none", "error": "",
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	setMigrationHelper(t, fixture, string(raw))

	stdout, stderr, err := executeKlippeeCLI(
		t, fixture.configPath, "printer-variable-set", "fixturePrinterA", "qidi_box_serial", value,
		"-y", "--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, migrate.KlippeeVariableValueSHA256(value)) {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	want := []string{
		fixture.toolPath, "set-klippee-variable",
		"--printer", "fixturePrinterA.example", "--user", "plab", "--port", "22",
		"--printer-user", "mks", "--name", "qidi_box_serial", "--value", value,
		"--confirm", migrate.KlippeeVariableSetAction,
	}
	got := readMigrationArgs(t, fixture)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
