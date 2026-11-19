package migrate

import (
	"encoding/json"
	"strings"
	"testing"
)

const testBoxClockGuardTransaction = "20260806T010203Z-0123456789abcdef"

func validBoxClockGuardDocument() map[string]any {
	digest := strings.Repeat("a", 64)
	return map[string]any{
		"schema":         BoxClockGuardInstallSchema,
		"ok":             true,
		"changed":        true,
		"applied":        true,
		"verified":       true,
		"account":        BoxClockGuardAccount,
		"transaction_id": testBoxClockGuardTransaction,
		"source_sha256":  digest,
		"target": map[string]any{
			"path": BoxClockGuardTargetPath, "sha256": digest,
			"owner": BoxClockGuardAccount, "group": BoxClockGuardAccount,
			"mode": BoxClockGuardTargetMode,
		},
		"backup": map[string]any{
			"path": "/home/mks/klipper/klippy/extras/.qidi-plus4-backups/" +
				"box-output-clock-guard-" + testBoxClockGuardTransaction +
				"/box_output_clock_guard.py",
			"sha256": strings.Repeat("b", 64), "owner": "mks", "group": "mks", "mode": "0644",
		},
		"klipper_service_action":   "none",
		"klipper_restarted":        false,
		"config_changed":           false,
		"activation_required":      true,
		"required_config_section":  BoxClockGuardConfigSection,
		"staging_cleanup_verified": true,
	}
}

func marshalBoxClockGuardDocument(t *testing.T, document map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseBoxClockGuardInstallBindsFixedTargetAndAllowsDiagnostics(t *testing.T) {
	document := validBoxClockGuardDocument()
	document["future_diagnostic"] = map[string]any{"safe": true}
	result, err := ParseBoxClockGuardInstall(marshalBoxClockGuardDocument(t, document))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Succeeded() || !result.DidChange() ||
		result.Target.Path != BoxClockGuardTargetPath ||
		result.Target.SHA256 != result.SourceSHA256 || result.Backup == nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseBoxClockGuardInstallCleanupFailureRequiresAttention(t *testing.T) {
	document := validBoxClockGuardDocument()
	document["staging_cleanup_verified"] = false
	document["staging_cleanup_error"] = "temporary file remains\n\x1b[31m"
	result, err := ParseBoxClockGuardInstall(marshalBoxClockGuardDocument(t, document))
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded() {
		t.Fatal("cleanup failure was treated as a successful completed operation")
	}
	if diagnostic := result.DiagnosticSummary(); !strings.Contains(diagnostic, "cleanup was not verified") || strings.Contains(diagnostic, "\n") {
		t.Fatalf("diagnostic = %q", diagnostic)
	}
}

func TestParseBoxClockGuardInstallAcceptsStructuredFailure(t *testing.T) {
	document := validBoxClockGuardDocument()
	document["ok"] = false
	document["verified"] = false
	document["changed"] = false
	document["applied"] = false
	document["phase"] = "verify"
	document["error"] = "post-install digest mismatch"
	result, err := ParseBoxClockGuardInstall(marshalBoxClockGuardDocument(t, document))
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded() || !strings.Contains(result.DiagnosticSummary(), "digest mismatch") {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseBoxClockGuardInstallRejectsUnsafeOrIncompleteProofs(t *testing.T) {
	tests := map[string]func(map[string]any){
		"wrong schema":  func(document map[string]any) { document["schema"] = "other/v1" },
		"wrong account": func(document map[string]any) { document["account"] = "root" },
		"wrong target": func(document map[string]any) {
			document["target"].(map[string]any)["path"] = "/tmp/guard.py"
		},
		"wrong digest": func(document map[string]any) {
			document["target"].(map[string]any)["sha256"] = strings.Repeat("c", 64)
		},
		"wrong owner": func(document map[string]any) {
			document["target"].(map[string]any)["owner"] = "root"
		},
		"wrong mode": func(document map[string]any) {
			document["target"].(map[string]any)["mode"] = "0755"
		},
		"restart performed":     func(document map[string]any) { document["klipper_restarted"] = true },
		"service action":        func(document map[string]any) { document["klipper_service_action"] = "restart" },
		"config changed":        func(document map[string]any) { document["config_changed"] = true },
		"wrong section":         func(document map[string]any) { document["required_config_section"] = "[other]" },
		"missing cleanup proof": func(document map[string]any) { delete(document, "staging_cleanup_verified") },
		"bad backup": func(document map[string]any) {
			document["backup"].(map[string]any)["path"] = "/tmp/backup.py"
		},
		"failed but verified": func(document map[string]any) {
			document["ok"] = false
			document["error"] = "failed"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := validBoxClockGuardDocument()
			mutate(document)
			if _, err := ParseBoxClockGuardInstall(marshalBoxClockGuardDocument(t, document)); err == nil {
				t.Fatalf("accepted invalid document: %#v", document)
			}
		})
	}

	valid := string(marshalBoxClockGuardDocument(t, validBoxClockGuardDocument()))
	for name, raw := range map[string]string{
		"duplicate":  strings.Replace(valid, `"account":"mks"`, `"account":"root","account":"mks"`, 1),
		"case alias": strings.Replace(valid, `"account":"mks"`, `"Account":"root","account":"mks"`, 1),
		"optional case alias": strings.Replace(
			valid, `"changed":true`, `"Changed":false,"changed":true`, 1,
		),
		"nested case alias": strings.Replace(
			valid,
			`"path":"`+BoxClockGuardTargetPath+`"`,
			`"Path":"/tmp/wrong","path":"`+BoxClockGuardTargetPath+`"`, 1,
		),
		"multiple documents": valid + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBoxClockGuardInstall([]byte(raw)); err == nil {
				t.Fatalf("accepted invalid JSON: %s", raw)
			}
		})
	}
}
