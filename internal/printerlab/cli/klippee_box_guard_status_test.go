package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKlippeeBoxGuardStatusReportsSafeActiveMitigations(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/printer/objects/query?box_output_clock_guard&configfile&save_variables" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{
			"result":{"status":{
				"box_output_clock_guard":{
					"state":"corrected","corrected_count":2,
					"reload_all_guarded":true,
					"corrected_outputs":["b_output","e_output"],
					"outputs":{
						"b_output":{"state":"corrected","previous_mcu":"mcu","pin_mcu":"mcu_box1"},
						"e_output":{"state":"corrected","previous_mcu":"mcu","pin_mcu":"mcu_box1"}
					}
				},
				"configfile":{"settings":{
					"filament_motion_sensor box_motion_sensor":{"use_irq":false},
					"mcu mcu_box1":{}
				}},
				"save_variables":{"variables":{}}
			}}
		}`))
	}))
	defer server.Close()

	stdout, stderr, err := executeKlippeeCLI(
		t, klippeePhysicalConfig(t, server.URL), "box-guard-status", "printer-a",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" || !strings.Contains(stdout, "true") ||
		!strings.Contains(stdout, "disabled (default)") || !strings.Contains(stdout, "mcu_box1") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestKlippeeBoxGuardStatusFailsForUnsafeSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":{"status":{
				"box_output_clock_guard":{"state":"error","outputs":{}},
				"configfile":{"settings":{
					"filament_motion_sensor box_motion_sensor":{"use_irq":"True","debounce_us":"50"},
					"mcu mcu_box1":{}
				}},
				"save_variables":{"variables":{"qidi_box_auto_insert":1}}
			}}
		}`))
	}))
	defer server.Close()

	stdout, _, err := executeKlippeeCLI(
		t, klippeePhysicalConfig(t, server.URL), "box-guard-status", "printer-a",
	)
	if err == nil || !strings.Contains(err.Error(), "exit status 1") && !strings.Contains(stdout, "false") {
		t.Fatalf("stdout=%q err=%v, want unsafe exit", stdout, err)
	}
	for _, want := range []string{"RELOAD_ALL automatic-insertion guard is not active", "use_irq is enabled", "debounce_us is configured as 50", "automatic QIDI Box insertion is enabled", "b_output status is missing"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout=%q, want %q", stdout, want)
		}
	}
}
