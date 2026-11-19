package migrate

import (
	"encoding/json"
	"strings"
	"testing"
)

func validKlippeeAgentUpgradeDocument() map[string]any {
	return map[string]any{
		"schema":     KlippeeAgentUpgradeSchema,
		"ok":         true,
		"changed":    true,
		"account":    KlippeeAgentAccount,
		"cli_path":   KlippeeAgentCLIPath,
		"source_url": KlippeeAgentOfficialSource,
		"before": map[string]any{
			"version": "2026.08.06.5", "sha256": strings.Repeat("a", 64),
		},
		"after": map[string]any{
			"version": "2026.08.06.6", "sha256": strings.Repeat("b", 64),
		},
		"service":                     KlippeeAgentService,
		"scope":                       "user",
		"observed_state":              "active",
		"before_pid":                  1234,
		"after_pid":                   5678,
		"upgrade_exit":                0,
		"state_exit":                  0,
		"upgrade_reported_success":    true,
		"agent_restart_performed":     true,
		"klipper_actions_performed":   false,
		"config_deployment_performed": false,
	}
}

func marshalKlippeeAgentUpgrade(t *testing.T, document map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseKlippeeAgentUpgradeValidatesChangedAndCurrentResults(t *testing.T) {
	result, err := ParseKlippeeAgentUpgrade(
		marshalKlippeeAgentUpgrade(t, validKlippeeAgentUpgradeDocument()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Succeeded() || !result.DidChange() ||
		result.Before.Version != "2026.08.06.5" || result.After.Version != "2026.08.06.6" {
		t.Fatalf("result = %#v", result)
	}

	current := validKlippeeAgentUpgradeDocument()
	current["changed"] = false
	current["after"] = map[string]any{
		"version": "2026.08.06.5", "sha256": strings.Repeat("a", 64),
	}
	result, err = ParseKlippeeAgentUpgrade(marshalKlippeeAgentUpgrade(t, current))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Succeeded() || result.DidChange() || *result.BeforePID == *result.AfterPID {
		t.Fatalf("current result = %#v", result)
	}
}

func TestParseKlippeeAgentUpgradeAcceptsStructuredFailure(t *testing.T) {
	document := validKlippeeAgentUpgradeDocument()
	document["ok"] = false
	document["changed"] = false
	document["after"] = map[string]any{
		"version": "2026.08.06.5", "sha256": strings.Repeat("a", 64),
	}
	document["after_pid"] = 1234
	document["upgrade_exit"] = 1
	document["upgrade_reported_success"] = false
	document["agent_restart_performed"] = false
	document["error"] = "official upgrade command failed"
	result, err := ParseKlippeeAgentUpgrade(marshalKlippeeAgentUpgrade(t, document))
	if err != nil {
		t.Fatal(err)
	}
	if result.Succeeded() || !strings.Contains(result.DiagnosticSummary(), "official upgrade") {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseKlippeeAgentUpgradeRejectsUnsafeOrInconsistentProofs(t *testing.T) {
	tests := map[string]func(map[string]any){
		"wrong schema":   func(document map[string]any) { document["schema"] = "other/v1" },
		"wrong account":  func(document map[string]any) { document["account"] = "root" },
		"wrong CLI path": func(document map[string]any) { document["cli_path"] = "/tmp/klippee" },
		"wrong source":   func(document map[string]any) { document["source_url"] = "https://example.com/klippee.py" },
		"wrong service":  func(document map[string]any) { document["service"] = "klipper.service" },
		"wrong scope":    func(document map[string]any) { document["scope"] = "system" },
		"invalid date": func(document map[string]any) {
			document["after"].(map[string]any)["version"] = "2026.02.31.1"
		},
		"non canonical version": func(document map[string]any) {
			document["after"].(map[string]any)["version"] = "v2026.08.07"
		},
		"downgrade": func(document map[string]any) {
			document["after"].(map[string]any)["version"] = "2026.08.05.9"
		},
		"invalid hash": func(document map[string]any) {
			document["after"].(map[string]any)["sha256"] = "ABC"
		},
		"changed false with difference": func(document map[string]any) { document["changed"] = false },
		"changed true without difference": func(document map[string]any) {
			document["after"] = map[string]any{
				"version": "2026.08.06.5", "sha256": strings.Repeat("a", 64),
			}
		},
		"same PID":        func(document map[string]any) { document["after_pid"] = 1234 },
		"invalid PID":     func(document map[string]any) { document["after_pid"] = maximumLinuxPID + 1 },
		"inactive":        func(document map[string]any) { document["observed_state"] = "failed" },
		"upgrade exit":    func(document map[string]any) { document["upgrade_exit"] = 1 },
		"state exit":      func(document map[string]any) { document["state_exit"] = 1 },
		"upgrade refused": func(document map[string]any) { document["upgrade_reported_success"] = false },
		"no restart":      func(document map[string]any) { document["agent_restart_performed"] = false },
		"Klipper action":  func(document map[string]any) { document["klipper_actions_performed"] = true },
		"config deploy":   func(document map[string]any) { document["config_deployment_performed"] = true },
		"success error":   func(document map[string]any) { document["error"] = "unexpected" },
		"missing outcome": func(document map[string]any) { delete(document, "ok") },
		"null proof":      func(document map[string]any) { document["after_pid"] = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			document := validKlippeeAgentUpgradeDocument()
			mutate(document)
			if _, err := ParseKlippeeAgentUpgrade(marshalKlippeeAgentUpgrade(t, document)); err == nil {
				t.Fatalf("accepted invalid result: %#v", document)
			}
		})
	}

	failureWithoutError := validKlippeeAgentUpgradeDocument()
	failureWithoutError["ok"] = false
	if _, err := ParseKlippeeAgentUpgrade(
		marshalKlippeeAgentUpgrade(t, failureWithoutError),
	); err == nil {
		t.Fatal("accepted failed result without error")
	}
}

func TestParseKlippeeAgentUpgradeRejectsAmbiguousJSON(t *testing.T) {
	valid := string(marshalKlippeeAgentUpgrade(t, validKlippeeAgentUpgradeDocument()))
	tests := map[string]string{
		"duplicate": strings.Replace(
			valid, `"account":"mks"`, `"account":"root","account":"mks"`, 1,
		),
		"case alias": strings.Replace(
			valid, `"account":"mks"`, `"Account":"root","account":"mks"`, 1,
		),
		"nested case alias": strings.Replace(
			valid, `"version":"2026.08.06.5"`,
			`"Version":"invalid","version":"2026.08.06.5"`, 1,
		),
		"unknown":  strings.Replace(valid, `"account":"mks"`, `"extra":true,"account":"mks"`, 1),
		"trailing": valid + `{}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseKlippeeAgentUpgrade([]byte(raw)); err == nil {
				t.Fatalf("accepted ambiguous JSON: %s", raw)
			}
		})
	}
}

func TestParseKlippeeAgentUpgradeBoundsResult(t *testing.T) {
	oversized := append([]byte(`{"`), []byte(strings.Repeat("x", maximumKlippeeAgentUpgradeResult))...)
	if _, err := ParseKlippeeAgentUpgrade(oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v", err)
	}
}
