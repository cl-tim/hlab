package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func validBoxResetReasonResult(t *testing.T) []byte {
	t.Helper()
	result := map[string]any{
		"schema":     klippeeBoxResetReasonSchema,
		"target_mcu": klippeeBoxResetReasonMCU,
		"reason":     "IndependentWatchdog",
		"reset_flags": map[string]any{
			"register": "RCC_CSR",
			"address":  klippeeBoxResetReasonRCCAddr,
			"raw":      0x22000003,
			"raw_hex":  "0x22000003",
			"asserted": []string{"IndependentWatchdog", "Brownout"},
		},
		"queried_at": "2026-08-06T09:30:00Z",
		"firmware": map[string]any{
			"version": "KLP_MCU_MKS_V1_1.1.1",
			"mcu":     "stm32f401xc",
		},
		"endpoint": map[string]any{
			"configured":   "/dev/serial/by-id/usb-Klipper_QIDI_BOX_V1_1.1.1_4B006E000F51353033353536-if00",
			"resolved":     "/dev/ttyACM3",
			"vendor_id":    "1d50",
			"product_id":   "614e",
			"manufacturer": "Klipper",
			"product":      "QIDI_BOX_V1_1.1.1",
			"serial":       "4B006E000F51353033353536",
		},
		"preconditions": map[string]any{
			"fresh_klippy_state": "shutdown",
			"endpoint_unowned":   true,
			"exclusive_open":     true,
		},
		"query": map[string]any{
			"rcc_csr": map[string]any{
				"command":  "debug_read order=%c addr=%u",
				"response": "debug_result val=%u",
				"address":  klippeeBoxResetReasonRCCAddr,
				"order":    klippeeBoxResetReasonRCCOrd,
			},
			"reset_reason": map[string]any{
				"command":  "reset_reason_get oid=%c",
				"response": "reset_reason_return oid=%c reason=%s",
				"oid":      klippeeBoxResetReasonOID,
			},
		},
		"safety": map[string]any{
			"read_only":       true,
			"reset_requested": false,
			"flags_cleared":   false,
			"config_changed":  false,
			"klipper_action":  "none",
			"may_be_stale":    true,
		},
		"caveat": klippeeBoxResetReasonCaveat,
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mutateBoxResetResult(
	t *testing.T,
	mutate func(map[string]any),
) []byte {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(validBoxResetReasonResult(t), &result); err != nil {
		t.Fatal(err)
	}
	mutate(result)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestKlippeeBoxResetReasonPollsExactOperationAndUsesStructuredResult(t *testing.T) {
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
			if payload.Method != klippeeBoxResetReasonMethod || payload.Arguments != nil {
				t.Errorf("invalid reset-reason request: %#v", payload)
			}
			_, _ = w.Write([]byte(`{
				"result":{"accepted":true,"command":"query-box-reset-reason","operation_id":"box-reason-op"}
			}`))
		case 3:
			if payload.Method != klippeeOperationStatusMethod ||
				payload.Arguments["operation_id"] != "box-reason-op" {
				t.Errorf("invalid exact-operation poll: %#v", payload)
			}
			_, _ = fmt.Fprintf(w, `{
				"result":{
					"running":false,"found":true,"operation_id":"box-reason-op","current":null,
					"last_result":{
						"id":"box-reason-op","method":"klippee_query_box_reset_reason",
						"command":"query-box-reset-reason","status":"succeeded",
						"started_at":"2026-08-06T09:29:59Z","completed_at":"2026-08-06T09:30:00Z",
						"return_code":0,"summary":"PowerOn from untrusted prose","result":%s
					}
				}
			}`, validBoxResetReasonResult(t))
		default:
			t.Errorf("unexpected extra request")
		}
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	stdout, stderr, err := executeKlippeeCLI(
		t, configPath, "box-reset-reason", "printer-a", "--output", "json",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var report klippeeBoxResetReasonReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode output %q: %v", stdout, err)
	}
	if report.Printer != "printer-a" || report.OperationID != "box-reason-op" ||
		report.TargetMCU != klippeeBoxResetReasonMCU ||
		report.Reason != "IndependentWatchdog" || report.Status != "succeeded" {
		t.Fatalf("report = %#v", report)
	}
	if strings.Contains(stdout, "untrusted prose") {
		t.Fatalf("operation summary leaked into structured result: %q", stdout)
	}
	want := []string{
		klippeeOperationStatusMethod,
		klippeeBoxResetReasonMethod,
		klippeeOperationStatusMethod,
	}
	if fmt.Sprint(methods) != fmt.Sprint(want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
}

func TestKlippeeBoxResetReasonResultFailsClosed(t *testing.T) {
	tests := map[string][]byte{
		"missing":    nil,
		"null":       []byte("null"),
		"not object": []byte(`"IndependentWatchdog"`),
		"wrong schema": mutateBoxResetResult(t, func(result map[string]any) {
			result["schema"] = "klippee-box-reset-reason/v1"
		}),
		"wrong target": mutateBoxResetResult(t, func(result map[string]any) {
			result["target_mcu"] = "mcu"
		}),
		"unknown reason": mutateBoxResetResult(t, func(result map[string]any) {
			result["reason"] = "Brownout"
		}),
		"non UTC time": mutateBoxResetResult(t, func(result map[string]any) {
			result["queried_at"] = "2026-08-06T19:30:00+10:00"
		}),
		"wrong endpoint": mutateBoxResetResult(t, func(result map[string]any) {
			result["endpoint"].(map[string]any)["vendor_id"] = "ffff"
		}),
		"wrong raw register": mutateBoxResetResult(t, func(result map[string]any) {
			result["reset_flags"].(map[string]any)["register"] = "RCC_BDCR"
		}),
		"wrong raw hex": mutateBoxResetResult(t, func(result map[string]any) {
			result["reset_flags"].(map[string]any)["raw_hex"] = "0x00000000"
		}),
		"inconsistent asserted flags": mutateBoxResetResult(t, func(result map[string]any) {
			result["reset_flags"].(map[string]any)["asserted"] = []string{"IndependentWatchdog"}
		}),
		"wrong RCC protocol": mutateBoxResetResult(t, func(result map[string]any) {
			result["query"].(map[string]any)["rcc_csr"].(map[string]any)["command"] = "debug_write"
		}),
		"wrong RCC address": mutateBoxResetResult(t, func(result map[string]any) {
			result["query"].(map[string]any)["rcc_csr"].(map[string]any)["address"] = "0x40023870"
		}),
		"wrong reset protocol": mutateBoxResetResult(t, func(result map[string]any) {
			result["query"].(map[string]any)["reset_reason"].(map[string]any)["command"] = "reset"
		}),
		"wrong correlation tag": mutateBoxResetResult(t, func(result map[string]any) {
			result["query"].(map[string]any)["reset_reason"].(map[string]any)["oid"] = 0
		}),
		"missing oid": mutateBoxResetResult(t, func(result map[string]any) {
			delete(result["query"].(map[string]any)["reset_reason"].(map[string]any), "oid")
		}),
		"Klippy not shutdown": mutateBoxResetResult(t, func(result map[string]any) {
			result["preconditions"].(map[string]any)["fresh_klippy_state"] = "error"
		}),
		"endpoint ownership unknown": mutateBoxResetResult(t, func(result map[string]any) {
			delete(result["preconditions"].(map[string]any), "endpoint_unowned")
		}),
		"endpoint owned": mutateBoxResetResult(t, func(result map[string]any) {
			result["preconditions"].(map[string]any)["endpoint_unowned"] = false
		}),
		"exclusive open missing": mutateBoxResetResult(t, func(result map[string]any) {
			delete(result["preconditions"].(map[string]any), "exclusive_open")
		}),
		"exclusive open failed": mutateBoxResetResult(t, func(result map[string]any) {
			result["preconditions"].(map[string]any)["exclusive_open"] = false
		}),
		"missing read only": mutateBoxResetResult(t, func(result map[string]any) {
			delete(result["safety"].(map[string]any), "read_only")
		}),
		"not read only": mutateBoxResetResult(t, func(result map[string]any) {
			result["safety"].(map[string]any)["read_only"] = false
		}),
		"reset requested": mutateBoxResetResult(t, func(result map[string]any) {
			result["safety"].(map[string]any)["reset_requested"] = true
		}),
		"flags cleared": mutateBoxResetResult(t, func(result map[string]any) {
			result["safety"].(map[string]any)["flags_cleared"] = true
		}),
		"config changed": mutateBoxResetResult(t, func(result map[string]any) {
			result["safety"].(map[string]any)["config_changed"] = true
		}),
		"Klipper action": mutateBoxResetResult(t, func(result map[string]any) {
			result["safety"].(map[string]any)["klipper_action"] = "restart"
		}),
		"staleness hidden": mutateBoxResetResult(t, func(result map[string]any) {
			result["safety"].(map[string]any)["may_be_stale"] = false
		}),
		"caveat changed": mutateBoxResetResult(t, func(result map[string]any) {
			result["caveat"] = "none"
		}),
		"unknown field": mutateBoxResetResult(t, func(result map[string]any) {
			result["extra"] = true
		}),
	}
	valid := string(validBoxResetReasonResult(t))
	tests["duplicate nested field"] = []byte(strings.Replace(
		valid, `"read_only":true`, `"read_only":true,"read_only":true`, 1,
	))
	tests["case aliased safety conflict"] = []byte(strings.Replace(
		valid, `"read_only":true`, `"READ_ONLY":false,"read_only":true`, 1,
	))
	tests["case aliased schema conflict"] = []byte(strings.Replace(
		valid, `"schema":"`+klippeeBoxResetReasonSchema+`"`,
		`"Schema":"wrong","schema":"`+klippeeBoxResetReasonSchema+`"`, 1,
	))

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeKlippeeBoxResetReasonResult(raw); err == nil {
				t.Fatalf("accepted invalid result: %s", raw)
			}
		})
	}
}

func TestKlippeeBoxResetReasonKeepsVendorReasonIndependentFromStickyFlags(t *testing.T) {
	raw := mutateBoxResetResult(t, func(result map[string]any) {
		result["reason"] = "Software"
	})
	result, err := decodeKlippeeBoxResetReasonResult(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reason != "Software" || result.ResetFlags.RawHex != "0x22000003" {
		t.Fatalf("result = %#v", result)
	}
}

func TestKlippeeBoxResetReasonWritesConciseTable(t *testing.T) {
	result, err := decodeKlippeeBoxResetReasonResult(validBoxResetReasonResult(t))
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	a := &app{out: &rendered, format: output.FormatTable}
	report := klippeeBoxResetReasonReport{
		Printer: "fixturePrinterA", OperationID: "box-op", Status: "succeeded",
		Schema: result.Schema, TargetMCU: result.TargetMCU, Reason: result.Reason,
		ResetFlags: result.ResetFlags,
		QueriedAt:  result.QueriedAt, Firmware: result.Firmware,
		Endpoint: result.Endpoint, Preconditions: result.Preconditions,
		Query: result.Query, Safety: result.Safety, Caveat: result.Caveat,
	}
	if err := writeKlippeeBoxResetReason(a, report); err != nil {
		t.Fatal(err)
	}
	text := rendered.String()
	for _, wanted := range []string{
		"PRINTER", "OPERATION", "RESET REASON", "PRECONDITIONS", "ACTIONS",
		"fixturePrinterA", "box-op", "IndependentWatchdog", "0x22000003",
		"IndependentWatchdog,Brownout", "Klippy=shutdown",
		"endpoint-unowned=true", "exclusive-open=true", "reset=false",
		"flags-cleared=false", "config=false", "Klipper=none",
	} {
		if !strings.Contains(text, wanted) {
			t.Fatalf("table omitted %q: %q", wanted, text)
		}
	}
	if strings.Contains(text, result.Endpoint.Serial) || strings.Contains(text, result.Caveat) {
		t.Fatalf("table included verbose structured metadata: %q", text)
	}
}

func TestKlippeeBoxResetReasonRejectsResultOutsideOperationWindow(t *testing.T) {
	result, err := decodeKlippeeBoxResetReasonResult(validBoxResetReasonResult(t))
	if err != nil {
		t.Fatal(err)
	}
	operation := &moonraker.ExtensionOperation{
		StartedAt: "2026-08-06T09:30:01Z", CompletedAt: "2026-08-06T09:30:02Z",
	}
	if err := validateKlippeeBoxResetReasonOperationWindow(operation, result); err == nil {
		t.Fatal("accepted a result timestamp from before the exact operation")
	}
	operation.StartedAt = ""
	if err := validateKlippeeBoxResetReasonOperationWindow(operation, result); err == nil {
		t.Fatal("accepted a completed operation with no start timestamp")
	}
}

func TestKlippeeBoxResetReasonRejectsMissingExactStatusIDWithoutResubmitting(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		methods = append(methods, payload.Method)
		switch len(methods) {
		case 1:
			_, _ = w.Write([]byte(`{
				"result":{"running":false,"found":false,"operation_id":null,"current":null,"last_result":null}
			}`))
		case 2:
			_, _ = w.Write([]byte(`{
				"result":{"accepted":true,"command":"query-box-reset-reason","operation_id":"box-reason-op"}
			}`))
		case 3:
			_, _ = fmt.Fprintf(w, `{
				"result":{"running":false,"found":true,"current":null,"last_result":{
					"id":"box-reason-op","method":"klippee_query_box_reset_reason",
					"command":"query-box-reset-reason","status":"succeeded",
					"started_at":"2026-08-06T09:29:59Z","completed_at":"2026-08-06T09:30:00Z",
					"return_code":0,"result":%s
				}}
			}`, validBoxResetReasonResult(t))
		default:
			t.Errorf("query was unexpectedly resubmitted: %v", methods)
		}
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(
		t, configPath, "box-reset-reason", "printer-a",
	)
	if err == nil || !strings.Contains(err.Error(), "omitted required field \"operation_id\"") {
		t.Fatalf("error = %v", err)
	}
	if len(methods) != 3 || methods[1] != klippeeBoxResetReasonMethod {
		t.Fatalf("methods = %v", methods)
	}
}

func TestKlippeeBoxResetReasonRunningPreflightDoesNotStartQuery(t *testing.T) {
	queries := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Method == klippeeBoxResetReasonMethod {
			queries++
		}
		_, _ = w.Write([]byte(`{
			"result":{"running":true,"found":true,"operation_id":"busy",
			"current":{"id":"busy","method":"klippee_check","command":"check","status":"running","started_at":"2026-08-06T09:29:59Z"},
			"last_result":null}
		}`))
	}))
	defer server.Close()

	configPath := klippeePhysicalConfig(t, server.URL)
	_, _, err := executeKlippeeCLI(
		t, configPath, "box-reset-reason", "printer-a",
	)
	if err == nil || !strings.Contains(err.Error(), "quiescence was not proven") {
		t.Fatalf("error = %v", err)
	}
	if queries != 0 {
		t.Fatalf("busy preflight started %d queries", queries)
	}
}

func TestKlippeeBoxResetReasonRejectsAmbiguousExactOperationStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"result":{
				"running":true,"found":true,"operation_id":"box-op",
				"current":{"id":"box-op","method":"klippee_query_box_reset_reason","command":"query-box-reset-reason","status":"running","started_at":"2026-08-06T09:29:59Z"},
				"last_result":{"id":"box-op","method":"klippee_query_box_reset_reason","command":"query-box-reset-reason","status":"succeeded","started_at":"2026-08-06T09:29:59Z","completed_at":"2026-08-06T09:30:00Z","return_code":0}
			}
		}`))
	}))
	defer server.Close()
	client, err := moonraker.New(
		inventory.Printer{MoonrakerURL: server.URL}, time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = waitForKlippeeOperation(
		context.Background(), client, "box-op", time.Second,
	)
	if err == nil || !strings.Contains(err.Error(), "ambiguous running and completed records") {
		t.Fatalf("error = %v", err)
	}
}

func TestKlippeeQuiescenceRejectsUnknownOperationState(t *testing.T) {
	status := moonraker.ExtensionOperationStatus{
		Found: true,
		LastResult: &moonraker.ExtensionOperation{
			ID: "box-op", Status: "cancelling",
		},
	}
	err := requireKlippeeOperationQuiescent(status)
	if err == nil || !strings.Contains(err.Error(), "unknown or non-terminal") {
		t.Fatalf("error = %v", err)
	}
}
