package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func validBoxMotionGuardJSON(t *testing.T) string {
	t.Helper()
	transaction := "20260806T010203Z-0123456789abcdef"
	before := strings.Repeat("1", 64)
	document := map[string]any{
		"schema": migrate.BoxMotionGuardSchema, "ok": true, "changed": true, "verified": true,
		"box_count": 1, "transaction_id": transaction,
		"target": map[string]any{
			"path": "/home/mks/cfg_with_1", "before_sha256": before,
			"after_sha256": strings.Repeat("2", 64), "mode": "0644", "uid": 0, "gid": 0,
		},
		"backup": map[string]any{
			"path":   "/home/mks/.qidi-plus4-backups/box-motion-guard-" + transaction + "/cfg_with_1",
			"sha256": before,
		},
		"use_irq": false, "debounce_us_present": false,
		"active_config_changed": false, "klipper_service_action": "none",
		"klipper_restarted": false, "activation_required": true,
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func validBoxMotionVendorJSON(t *testing.T) string {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(validBoxMotionGuardJSON(t)), &document); err != nil {
		t.Fatal(err)
	}
	document["use_irq"] = true
	document["debounce_us_present"] = true
	backup := document["backup"].(map[string]any)
	backup["path"] = strings.Replace(
		backup["path"].(string), "box-motion-guard-", "box-motion-vendor-", 1,
	)
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestKlippeeHardenBoxMotionTemplateUsesDetectedCountAndFixedOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/printer/objects/query?print_stats":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/server/extensions/request":
			_, _ = w.Write([]byte(`{
				"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
			}`))
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/printer/objects/query?box_output_clock_guard&configfile&save_variables":
			_, _ = w.Write([]byte(`{
				"result":{"status":{
					"box_output_clock_guard":{"state":"corrected"},
					"configfile":{"settings":{
						"filament_motion_sensor box_motion_sensor":{"use_irq":true},
						"mcu mcu_box1":{}
					}},
					"save_variables":{"variables":{}}
				}}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, validBoxMotionGuardJSON(t))

	stdout, stderr, err := executeKlippeeCLI(
		t, fixture.configPath, "harden-box-motion-template", "fixturePrinterA",
		"-y", "--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, `"box_count": 1`) {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	want := []string{
		fixture.toolPath, "harden-box-motion-template",
		"--printer", "fixturePrinterA.example", "--user", "plab", "--port", "22",
		"--printer-user", "mks", "--box-count", "1",
		"--confirm", migrate.BoxMotionGuardAction,
	}
	got := readMigrationArgs(t, fixture)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{"--target", "--remote-python", "restart", "RESTART"} {
		if strings.Contains(strings.Join(got, " "), forbidden) {
			t.Fatalf("wrapper exposed forbidden argument %q: %#v", forbidden, got)
		}
	}
}

func TestKlippeeRestoreBoxMotionTemplateUsesFixedVendorOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/printer/objects/query?print_stats":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/server/extensions/request":
			_, _ = w.Write([]byte(`{"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}}`))
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/printer/objects/query?box_output_clock_guard&configfile&save_variables":
			_, _ = w.Write([]byte(`{"result":{"status":{"box_output_clock_guard":{"state":"corrected"},"configfile":{"settings":{"filament_motion_sensor box_motion_sensor":{"use_irq":false},"mcu mcu_box1":{}}},"save_variables":{"variables":{}}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, validBoxMotionVendorJSON(t))

	_, _, err := executeKlippeeCLI(
		t, fixture.configPath, "restore-box-motion-template", "fixturePrinterA", "-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		fixture.toolPath, "restore-box-motion-template",
		"--printer", "fixturePrinterA.example", "--user", "plab", "--port", "22",
		"--printer-user", "mks", "--box-count", "1",
		"--confirm", migrate.BoxMotionVendorAction,
	}
	got := readMigrationArgs(t, fixture)
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}
