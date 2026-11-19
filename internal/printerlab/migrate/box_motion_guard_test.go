package migrate

import (
	"encoding/json"
	"strings"
	"testing"
)

func validBoxMotionGuardDocument() map[string]any {
	transaction := "20260806T010203Z-0123456789abcdef"
	return map[string]any{
		"schema": BoxMotionGuardSchema, "ok": true, "changed": true, "verified": true,
		"box_count": 1, "transaction_id": transaction,
		"target": map[string]any{
			"path": "/home/mks/cfg_with_1", "before_sha256": strings.Repeat("1", 64),
			"after_sha256": strings.Repeat("2", 64), "mode": "0644", "uid": 0, "gid": 0,
		},
		"backup": map[string]any{
			"path":   "/home/mks/.qidi-plus4-backups/box-motion-guard-" + transaction + "/cfg_with_1",
			"sha256": strings.Repeat("1", 64),
		},
		"use_irq": false, "debounce_us_present": false,
		"active_config_changed": false, "klipper_service_action": "none",
		"klipper_restarted": false, "activation_required": true,
	}
}

func marshalBoxMotionGuard(t *testing.T, document map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseBoxMotionGuardAcceptsFixedSafeProof(t *testing.T) {
	result, err := ParseBoxMotionGuard(marshalBoxMotionGuard(t, validBoxMotionGuardDocument()))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Succeeded() || !result.DidChange() || result.BoxCount != 1 ||
		result.Target.Path != "/home/mks/cfg_with_1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestParseBoxMotionVendorRestoreAcceptsVendorProof(t *testing.T) {
	document := validBoxMotionGuardDocument()
	document["use_irq"] = true
	document["debounce_us_present"] = true
	backup := document["backup"].(map[string]any)
	backup["path"] = strings.Replace(
		backup["path"].(string), "box-motion-guard-", "box-motion-vendor-", 1,
	)
	result, err := ParseBoxMotionVendorRestore(marshalBoxMotionGuard(t, document))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Succeeded() || result.UseIRQ == nil || !*result.UseIRQ ||
		result.DebounceUSPresent == nil || !*result.DebounceUSPresent {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestParseBoxMotionGuardRejectsUnsafeProof(t *testing.T) {
	for name, mutate := range map[string]func(map[string]any){
		"wrong target": func(document map[string]any) {
			document["target"].(map[string]any)["path"] = "/tmp/cfg"
		},
		"irq enabled": func(document map[string]any) { document["use_irq"] = true },
		"restart":     func(document map[string]any) { document["klipper_restarted"] = true },
	} {
		t.Run(name, func(t *testing.T) {
			document := validBoxMotionGuardDocument()
			mutate(document)
			if _, err := ParseBoxMotionGuard(marshalBoxMotionGuard(t, document)); err == nil {
				t.Fatal("unsafe proof was accepted")
			}
		})
	}
}
