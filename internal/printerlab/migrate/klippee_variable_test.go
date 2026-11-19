package migrate

import (
	"encoding/json"
	"testing"
)

func TestParseKlippeeVariableSetAcceptsFixedVerifiedProof(t *testing.T) {
	value := "/dev/serial/example"
	document := map[string]any{
		"schema": KlippeeVariableSetSchema, "ok": true,
		"account": BoxClockGuardAccount, "cli_path": KlippeeVariableCLIPath,
		"name": "qidi_box_serial", "value_sha256": KlippeeVariableValueSHA256(value),
		"set_return_code": 0, "doctor_return_code": 0, "verified": true,
		"config_deployed": false, "klipper_action": "none", "error": "",
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ParseKlippeeVariableSet(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Succeeded() || result.Name != "qidi_box_serial" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestParseKlippeeVariableSetRejectsUnsafeProof(t *testing.T) {
	document := map[string]any{
		"schema": KlippeeVariableSetSchema, "ok": true,
		"account": BoxClockGuardAccount, "cli_path": "/tmp/klippee",
		"name": "qidi_box_serial", "value_sha256": KlippeeVariableValueSHA256("value"),
		"set_return_code": 0, "doctor_return_code": 0, "verified": true,
		"config_deployed": false, "klipper_action": "none", "error": "",
	}
	raw, _ := json.Marshal(document)
	if _, err := ParseKlippeeVariableSet(raw); err == nil {
		t.Fatal("unsafe CLI path was accepted")
	}
}
