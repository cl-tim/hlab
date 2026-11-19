package moonraker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
)

func TestSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/server/info":
			_, _ = w.Write([]byte(`{"result":{"klippy_connected":true,"klippy_state":"ready","moonraker_version":"v0.9","hostname":"printer-b"}}`))
		case "/printer/info":
			_, _ = w.Write([]byte(`{"result":{"state":"ready","state_message":"  Ready to print\n","software_version":"v0.13.0"}}`))
		case "/printer/objects/query":
			_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"paused"},"virtual_sdcard":{"progress":0},"extruder":{"temperature":25.5,"target":0},"heater_bed":{"temperature":24.2,"target":0},"toolhead":{"homed_axes":"xy","position":[10,300,117.85,42]},"idle_timeout":{"state":"Idle"},"pause_resume":{"is_paused":true}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	printer := inventory.Printer{
		Name: "printer-b", Address: "mainline-plus4.example", Firmware: inventory.FirmwareMainline,
		MoonrakerURL: server.URL,
	}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got := client.Snapshot(context.Background(), printer)
	if !got.Reachable || got.KlippyState != "ready" || got.PrintState != "paused" {
		t.Fatalf("unexpected snapshot: %#v", got)
	}
	if got.HomedAxes != "xy" || len(got.ToolheadPosition) != 4 ||
		got.ToolheadPosition[2] != 117.85 || got.IdleState != "Idle" || !got.Paused {
		t.Fatalf("snapshot omitted recovery state: %#v", got)
	}
	if got.KlippyStateMessage != "Ready to print" {
		t.Fatalf("unexpected Klippy state message: %q", got.KlippyStateMessage)
	}
}

func TestBoundedStateMessageNormalizesAndTruncatesUnicode(t *testing.T) {
	message := "  failure\n" + strings.Repeat("界", 1000) + "tail"
	got := boundedStateMessage(message)
	if !strings.HasPrefix(got, "failure ") || !strings.HasSuffix(got, "...") {
		t.Fatalf("unexpected bounded message: %q", got)
	}
	if len([]rune(strings.TrimSuffix(got, "..."))) != 1000 {
		t.Fatalf("bounded message has %d runes before suffix, want 1000", len([]rune(strings.TrimSuffix(got, "..."))))
	}
}

func TestBoxGuardStatusReadsFixedSafetyEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/printer/objects/query?box_output_clock_guard&configfile&save_variables" {
			t.Fatalf("unexpected request URI: %s", r.URL.RequestURI())
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
					"box_output_clock_guard":{},
					"filament_motion_sensor box_motion_sensor":{"use_irq":"False","debounce_us":"50"},
					"mcu mcu_box1":{}
				}},
				"save_variables":{"variables":{"qidi_box_auto_insert":0}}
			}}
		}`))
	}))
	defer server.Close()

	client, err := New(inventory.Printer{Name: "printer-a", MoonrakerURL: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.BoxGuardStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "corrected" || got.CorrectedCount != 2 || !got.ReloadAllGuarded || !got.ConfigSectionLoaded ||
		!got.MotionSensorSectionLoaded || !got.UseIRQKnown || got.UseIRQ ||
		!got.DebounceUSPresent || !got.DebounceUSKnown || got.DebounceUS != 50 ||
		!got.AutoInsertVariablePresent || got.AutoInsertEnabled ||
		got.BoxCount != 1 ||
		got.Outputs["b_output"].PinMCU != "mcu_box1" || got.Outputs["e_output"].PinMCU != "mcu_box1" {
		t.Fatalf("unexpected Box guard status: %#v", got)
	}
}

func TestEnsureIdleRejectsPrinting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"printing"}}}}`))
	}))
	defer server.Close()

	printer := inventory.Printer{Name: "printer-b", Address: "mainline-plus4.example", MoonrakerURL: server.URL}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureIdle(context.Background()); err == nil {
		t.Fatal("expected printing state to be rejected")
	}
}

func TestIdleTimeoutReadsOnlyResolvedSetting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/printer/objects/query?configfile" {
			t.Fatalf("request URI = %s", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"result":{"status":{"configfile":{"settings":{"idle_timeout":{"timeout":7200}}}}}}`))
	}))
	defer server.Close()
	client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	seconds, err := client.IdleTimeout(context.Background())
	if err != nil || seconds != 7200 {
		t.Fatalf("seconds=%v err=%v", seconds, err)
	}
}

func TestKlippeeEventsFiltersUnrelatedConsoleHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/server/gcode_store?count=1000" {
			t.Fatalf("request URI = %s", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"result":{"gcode_store":[{"time":1,"type":"command","message":"G28"},{"time":2,"type":"response","message":"Klippee: apply complete"}]}}`))
	}))
	defer server.Close()
	client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	events, err := client.KlippeeEvents(context.Background())
	if err != nil || len(events) != 1 || events[0].Message != "Klippee: apply complete" {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestBoxRuntimeStatusReadsOnlyWhitelistedState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RequestURI(), "box_stepper%20slot3") ||
			!strings.Contains(r.URL.RequestURI(), "gcode_macro%20QIDI_BOX_LOAD") ||
			!strings.Contains(r.URL.RequestURI(), "gcode_macro%20QIDI_BOX_UNLOAD_LOADED") {
			t.Fatalf("request URI = %s", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"result":{"status":{
			"save_variables":{"variables":{"enable_box":0,"box_count":1,"extrude_state":-1,"slot_sync":"slot-1","last_load_slot":"slot2","retry_step":null,"value_t0":"slot0","value_t1":"slot1","value_t2":"slot2","value_t3":"slot3","slot0":0,"slot1":1,"slot2":2,"slot3":0}},
			"box_extras":{"b_endstop_state":false,"e_endstop_state":true},
			"hall_filament_width_sensor":{"Diameter":1.87},
			"print_stats":{"state":"standby"},"toolhead":{"homed_axes":"xyz"},
			"gcode_macro QIDI_BOX_LOAD":{},
			"gcode_macro QIDI_BOX_UNLOAD_LOADED":{},
			"box_stepper slot0":{"runout_button":true},"box_stepper slot1":{"runout_button":false},"box_stepper slot2":{"runout_button":false},"box_stepper slot3":{"runout_button":true}
		}}}`))
	}))
	defer server.Close()
	client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.BoxRuntimeStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !state.LoadMacroLoaded || !state.UnloadMacroLoaded || !state.UnloadReady || state.UnloadSlot != "slot2" || state.UnloadSlotSource != "last_load_slot" ||
		state.Enabled || state.SlotSync != "slot-1" || state.LastLoadSlot != "slot2" ||
		!state.EEndstop || state.BEndstop || state.FilamentWidth != 1.87 ||
		state.SlotState[2] != 2 || state.SlotRunout[2] {
		t.Fatalf("state = %#v", state)
	}
}

func TestPrintStateMarksMissingStateMalformed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{}}}}`))
	}))
	defer server.Close()
	client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.PrintState(context.Background()); err == nil || !errors.Is(err, ErrMalformedState) {
		t.Fatalf("error = %v, want malformed state", err)
	}
}

func TestKlippyConnectionRequiresExplicitServerEvidence(t *testing.T) {
	for name, response := range map[string]string{
		"missing connected": `{"result":{"klippy_state":"disconnected"}}`,
		"missing state":     `{"result":{"klippy_connected":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.KlippyConnection(context.Background()); err == nil || !errors.Is(err, ErrMalformedState) {
				t.Fatalf("error = %v, want malformed state", err)
			}
		})
	}
}

func TestWaitForKlippyReadyObservesStartupThenReady(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		if requests == 1 {
			_, _ = w.Write([]byte(`{"result":{"klippy_connected":false,"klippy_state":"startup"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"result":{"klippy_connected":true,"klippy_state":"ready"}}`))
	}))
	defer server.Close()
	client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	state, err := client.WaitForKlippyReady(context.Background(), time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Connected || state.State != "ready" || requests != 2 {
		t.Fatalf("state=%#v requests=%d", state, requests)
	}
}

func TestWaitForKlippyReadyRejectsTerminalAndContradictoryState(t *testing.T) {
	for name, response := range map[string]string{
		"terminal":      `{"result":{"klippy_connected":true,"klippy_state":"error"}}`,
		"contradictory": `{"result":{"klippy_connected":false,"klippy_state":"ready"}}`,
		"unknown":       `{"result":{"klippy_connected":true,"klippy_state":"mystery"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.WaitForKlippyReady(
				context.Background(), time.Second, time.Millisecond,
			); err == nil {
				t.Fatal("expected readiness rejection")
			}
		})
	}
}

func TestRequestExtensionPostsOfficialPayloadAndDecodesWrappedResult(t *testing.T) {
	t.Setenv("TEST_MOONRAKER_API_KEY", "secret-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/server/extensions/request" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "secret-key" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["agent"] != "klippee" || request["method"] != "klippee_apply" {
			t.Fatalf("unexpected extension request: %#v", request)
		}
		if _, present := request["arguments"]; present {
			t.Fatalf("nil arguments should be omitted: %#v", request)
		}
		_, _ = w.Write([]byte(`{"result":{"accepted":true,"command":"apply","operation_id":"op-123"}}`))
	}))
	defer server.Close()

	printer := inventory.Printer{
		Name: "printer-a", Address: "printer-a.example", MoonrakerURL: server.URL,
		APIKeyEnv: "TEST_MOONRAKER_API_KEY",
	}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var response ExtensionResponse
	if err := client.RequestExtension(
		context.Background(), "klippee", "klippee_apply", nil, &response,
	); err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.Command != "apply" || response.OperationID != "op-123" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestRequestExtensionDecodesOperationStatusAndArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Agent     string         `json:"agent"`
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Agent != "klippee" || request.Method != "klippee_operation_status" ||
			request.Arguments["operation_id"] != "op-123" {
			t.Fatalf("unexpected request: %#v", request)
		}
		_, _ = w.Write([]byte(`{
			"result": {
				"running": false,
				"found": true,
				"operation_id": "op-123",
				"current": null,
				"last_result": {
					"id": "op-123",
					"method": "klippee_apply",
					"command": "apply",
					"status": "failed",
					"started_at": "2026-08-06T00:00:00Z",
					"completed_at": "2026-08-06T00:00:03Z",
					"return_code": 7,
					"summary": "apply failed"
				}
			}
		}`))
	}))
	defer server.Close()

	printer := inventory.Printer{Name: "printer-a", Address: "printer-a.example", MoonrakerURL: server.URL}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var status ExtensionOperationStatus
	if err := client.RequestExtension(
		context.Background(),
		"klippee",
		"klippee_operation_status",
		map[string]string{"operation_id": "op-123"},
		&status,
	); err != nil {
		t.Fatal(err)
	}
	if !status.Found || status.Running || status.LastResult == nil ||
		status.LastResult.ReturnCode == nil || *status.LastResult.ReturnCode != 7 {
		t.Fatalf("unexpected operation status: %#v", status)
	}
}

func TestMoonrakerRedirectIsRejectedWithoutLeakingAPIKey(t *testing.T) {
	const secret = "redirect-must-not-see-this"
	t.Setenv("TEST_MOONRAKER_REDIRECT_KEY", secret)
	received := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"result":{"accepted":true,"command":"apply"}}`))
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Api-Key"); got != secret {
			t.Errorf("origin API key = %q", got)
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	client, err := New(inventory.Printer{
		Name: "printer-a", MoonrakerURL: origin.URL,
		APIKeyEnv: "TEST_MOONRAKER_REDIRECT_KEY",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var response ExtensionResponse
	err = client.RequestExtension(
		context.Background(), "klippee", "klippee_apply", nil, &response,
	)
	if err == nil || !strings.Contains(err.Error(), "307 Temporary Redirect") {
		t.Fatalf("error = %v", err)
	}
	select {
	case leaked := <-received:
		t.Fatalf("redirect target was contacted with X-Api-Key %q", leaked)
	default:
	}
}

func TestRequestExtensionAcceptsDirectRelayedResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accepted":true,"command":"check"}`))
	}))
	defer server.Close()

	printer := inventory.Printer{Name: "printer-a", Address: "printer-a.example", MoonrakerURL: server.URL}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var response ExtensionResponse
	if err := client.RequestExtension(
		context.Background(), "klippee", "klippee_check", nil, &response,
	); err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.Command != "check" {
		t.Fatalf("unexpected response: %#v", response)
	}
}

func TestRequestExtensionRejectsJSONRPCErrorOnSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"code":-32601,"message":"method not found"}}`))
	}))
	defer server.Close()

	printer := inventory.Printer{Name: "printer-a", Address: "printer-a.example", MoonrakerURL: server.URL}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var response ExtensionResponse
	err = client.RequestExtension(context.Background(), "klippee", "klippee_apply", nil, &response)
	if err == nil || !strings.Contains(err.Error(), "-32601") || !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("got %v, want structured JSON-RPC error", err)
	}
}

func TestRequestExtensionRejectsStructuredHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":503,"message":"klippee agent is not connected"}}`))
	}))
	defer server.Close()

	printer := inventory.Printer{Name: "printer-a", Address: "printer-a.example", MoonrakerURL: server.URL}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var response ExtensionResponse
	err = client.RequestExtension(context.Background(), "klippee", "klippee_apply", nil, &response)
	if err == nil || !strings.Contains(err.Error(), "503 Service Unavailable") ||
		!strings.Contains(err.Error(), "agent is not connected") {
		t.Fatalf("got %v, want structured HTTP error", err)
	}
}

func TestRequestExtensionRejectsMissingResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":null}`))
	}))
	defer server.Close()

	printer := inventory.Printer{Name: "printer-a", Address: "printer-a.example", MoonrakerURL: server.URL}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var response ExtensionResponse
	err = client.RequestExtension(context.Background(), "klippee", "klippee_apply", nil, &response)
	if err == nil || !strings.Contains(err.Error(), "no result") {
		t.Fatalf("got %v, want missing-result error", err)
	}
}

func TestRequestExtensionRejectsAmbiguousOperationProofs(t *testing.T) {
	validStatus := `{
		"running":false,"found":true,"operation_id":"op","current":null,
		"last_result":{"id":"op","method":"klippee_query_box_reset_reason",
		"command":"query-box-reset-reason","status":"succeeded",
		"started_at":"2026-08-06T00:00:00Z","completed_at":"2026-08-06T00:00:01Z",
		"return_code":0,"result":{}}
	}`
	tests := map[string]struct {
		body   string
		target func() any
		want   string
	}{
		"duplicate envelope result": {
			body:   `{"result":` + validStatus + `,"result":` + validStatus + `}`,
			target: func() any { return &ExtensionOperationStatus{} },
			want:   "duplicate field \"result\"",
		},
		"unknown envelope field": {
			body:   `{"result":` + validStatus + `,"Result":` + validStatus + `}`,
			target: func() any { return &ExtensionOperationStatus{} },
			want:   "unexpected exact-case field \"Result\"",
		},
		"duplicate acknowledgement command": {
			body:   `{"result":{"accepted":true,"command":"check","command":"apply"}}`,
			target: func() any { return &ExtensionResponse{} },
			want:   "duplicate field \"command\"",
		},
		"case aliased status proof": {
			body:   `{"result":{"Running":true,` + strings.TrimPrefix(validStatus, "{") + `}`,
			target: func() any { return &ExtensionOperationStatus{} },
			want:   "unexpected exact-case field \"Running\"",
		},
		"duplicate operation id": {
			body: `{"result":` + strings.Replace(
				validStatus, `"operation_id":"op"`, `"operation_id":"other","operation_id":"op"`, 1,
			) + `}`,
			target: func() any { return &ExtensionOperationStatus{} },
			want:   "duplicate field \"operation_id\"",
		},
		"duplicate terminal status": {
			body: `{"result":` + strings.Replace(
				validStatus, `"status":"succeeded"`, `"status":"failed","status":"succeeded"`, 1,
			) + `}`,
			target: func() any { return &ExtensionOperationStatus{} },
			want:   "duplicate field \"status\"",
		},
		"duplicate return code": {
			body: `{"result":` + strings.Replace(
				validStatus, `"return_code":0`, `"return_code":1,"return_code":0`, 1,
			) + `}`,
			target: func() any { return &ExtensionOperationStatus{} },
			want:   "duplicate field \"return_code\"",
		},
		"duplicate structured result": {
			body: `{"result":` + strings.Replace(
				validStatus, `"result":{}`, `"result":{"reason":"PowerOn"},"result":{"reason":"IndependentWatchdog"}`, 1,
			) + `}`,
			target: func() any { return &ExtensionOperationStatus{} },
			want:   "duplicate field \"result\"",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			client, err := New(inventory.Printer{MoonrakerURL: server.URL}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			err = client.RequestExtension(
				context.Background(), "klippee", "klippee_operation_status", nil, test.target(),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunGcodePostsFixedScriptAndForwardsAPIKey(t *testing.T) {
	t.Setenv("TEST_MOONRAKER_API_KEY", "secret-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/printer/gcode/script" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Api-Key"); got != "secret-key" {
			t.Fatalf("X-Api-Key = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["script"] != "RESTART" {
			t.Fatalf("unexpected G-code payload: %#v", payload)
		}
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer server.Close()

	printer := inventory.Printer{
		Name: "printer-a", Address: "printer-a.example", MoonrakerURL: server.URL,
		APIKeyEnv: "TEST_MOONRAKER_API_KEY",
	}
	client, err := New(printer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.RunGcode(context.Background(), "RESTART"); err != nil {
		t.Fatal(err)
	}
}

func TestRunGcodeRejectsEmptyScript(t *testing.T) {
	client := &Client{}
	if err := client.RunGcode(context.Background(), "  "); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Fatalf("got %v, want empty-script rejection", err)
	}
}
