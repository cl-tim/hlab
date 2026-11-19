package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKlippeeBoxAutoInsertDisableUsesFixedMacroAndVerifies(t *testing.T) {
	guardReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/printer/objects/query" && r.URL.RawQuery == "print_stats":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
		case r.Method == http.MethodGet && r.URL.RequestURI() == "/printer/objects/query?box_output_clock_guard&configfile&save_variables":
			guardReads++
			value := 1
			if guardReads == 2 {
				value = 0
			}
			_, _ = w.Write([]byte(`{"result":{"status":{"box_output_clock_guard":{"state":"corrected","reload_all_guarded":true},"configfile":{"settings":{}},"save_variables":{"variables":{"qidi_box_auto_insert":` + string(rune('0'+value)) + `}}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/printer/gcode/script":
			var payload struct {
				Script string `json:"script"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Script != "QIDI_BOX_AUTO_INSERT_DISABLE" {
				t.Fatalf("script = %q", payload.Script)
			}
			_, _ = w.Write([]byte(`{"result":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stdout, stderr, err := executeKlippeeCLI(
		t, klippeePhysicalConfig(t, server.URL), "box-auto-insert-disable", "printer-a", "-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || guardReads != 2 || !strings.Contains(stdout, "disabled and verified") {
		t.Fatalf("stdout=%q stderr=%q guardReads=%d", stdout, stderr, guardReads)
	}
}

func TestKlippeeBoxAutoInsertChangeRequiresApprovalBeforeNetwork(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))
	defer server.Close()

	_, _, err := executeKlippeeCLI(
		t, klippeePhysicalConfig(t, server.URL), "box-auto-insert-disable", "printer-a",
	)
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("missing approval made %d requests", requests)
	}
}
