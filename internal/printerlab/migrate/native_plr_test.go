package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNativePLRTransactionsPreserveRecoveryContract(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		for _, scenario := range []string{"unchanged", "success", "bad-identity", "apply-failure", "compensated-failure", "bad-handle", "verify-failure"} {
			name := fmt.Sprintf("rollback=%v/%s", rollback, scenario)
			t.Run(name, func(t *testing.T) {
				b := nativeTestBackend(t)
				paths, _ := CanonicalPLRPaths("mks")
				backup := paths.ScriptsDir + "/backups/plr-hardening-test/backup-manifest.json"
				safety := paths.ScriptsDir + "/backups/plr-rollback-safety-test/safety-manifest.json"
				calls := []string{}
				plans := 0
				b.transport = func(command string, input []byte) ([]byte, int, error) {
					calls = append(calls, command)
					if strings.HasPrefix(command, "mktemp") {
						return []byte("/tmp/qidi-plr-hardening.TEST\n"), 0, nil
					}
					if strings.HasPrefix(command, "systemctl is-active") {
						return []byte("inactive\n"), 3, fmt.Errorf("inactive")
					}
					if !strings.Contains(command, "'install.py'") && !strings.Contains(command, "/install.py'") {
						return nil, 0, nil
					}
					applying := strings.HasSuffix(command, " --apply --klipper-stopped")
					if !applying {
						plans++
					}
					doc := map[string]any{"ok": true, "account": "mks", "paths": paths, "changed": scenario != "unchanged" && plans == 1, "backup_manifest": nil, "safety_manifest": nil}
					if rollback || doc["changed"] == true {
						doc["backup_manifest"] = backup
					}
					if rollback && doc["changed"] == true {
						doc["safety_manifest"] = safety
					}
					if applying {
						doc["backup_manifest"] = backup
						if rollback {
							doc["safety_manifest"] = safety
						}
					}
					code := 0
					var err error
					if scenario == "bad-identity" {
						doc["account"] = "root"
					}
					if applying && (scenario == "apply-failure" || scenario == "compensated-failure" || scenario == "bad-handle") {
						doc["ok"] = false
						doc["error"] = "fixture failure"
						doc["transaction_state"] = "partial"
						if scenario != "apply-failure" {
							doc["transaction_state"] = "auto_rolled_back"
							if rollback {
								doc["transaction_state"] = "safety_restored"
							}
						}
						if scenario == "bad-handle" {
							doc["backup_manifest"] = "/tmp/untrusted"
						}
						code = 1
						err = fmt.Errorf("fixture failure")
					}
					if !applying && plans == 2 && scenario == "verify-failure" {
						doc["ok"] = false
						doc["error"] = "verification failed"
						code = 1
						err = fmt.Errorf("verification failed")
					}
					raw, _ := json.Marshal(doc)
					return raw, code, err
				}
				stage := "deploy-plr-hardening"
				confirm := "DEPLOY-PLR-HARDENING"
				schema := PLRDeploymentSchema
				if rollback {
					stage = "rollback-plr-hardening"
					confirm = "ROLLBACK-PLR-HARDENING"
					schema = PLRRollbackSchema
				}
				args := []string{stage, "--printer", "plus4.invalid", "--user", "plab", "--printer-user", "mks", "--confirm", confirm}
				if rollback {
					args = append(args, "--backup-manifest", backup)
				}
				err := b.execute(args)
				success := scenario == "success" || scenario == "unchanged"
				if (err == nil) != success {
					t.Fatalf("operation err=%v", err)
				}
				receipt, parseErr := ParsePLROperation(b.out.(*bytes.Buffer).Bytes(), schema)
				if parseErr != nil {
					t.Fatalf("invalid receipt: %v\n%s", parseErr, b.out)
				}
				if err := ValidatePLRTarget(receipt, "mks"); err != nil {
					t.Fatal(err)
				}
				if err := ValidatePLRLifecycle(receipt); err != nil {
					t.Fatalf("invalid lifecycle: %v\n%s", err, b.out)
				}
				stopped := false
				restarted := false
				for _, command := range calls {
					if strings.Contains(command, "systemctl stop") {
						stopped = true
					}
					if strings.Contains(command, "systemctl start") || strings.Contains(command, "systemctl restart") {
						restarted = true
					}
				}
				if restarted {
					t.Fatal("transaction implicitly restarted Klipper")
				}
				wantStop := scenario != "unchanged" && scenario != "bad-identity"
				if stopped != wantStop {
					t.Fatalf("stopped=%v want %v", stopped, wantStop)
				}
				if wantStop && receipt.BackupManifest != backup {
					t.Fatal("failure lost predeclared backup")
				}
				if scenario == "bad-handle" && receipt.TransactionState != "indeterminate" {
					t.Fatal("untrusted handle supplied compensation proof")
				}
				if scenario == "verify-failure" && receipt.Applied != nil {
					t.Fatal("verification failure claimed a known applied state")
				}
			})
		}
	}
}
func TestNativePLRRejectsUnsafeLayoutBeforeTransport(t *testing.T) {
	b := nativeTestBackend(t)
	calls := 0
	b.transport = func(string, []byte) ([]byte, int, error) { calls++; return nil, 0, nil }
	for _, args := range [][]string{{"deploy-plr-hardening", "--printer", "plus4.invalid", "--confirm", "DEPLOY-PLR-HARDENING", "--scripts-dir", "/tmp/plr"}, {"rollback-plr-hardening", "--printer", "plus4.invalid", "--confirm", "ROLLBACK-PLR-HARDENING", "--backup-manifest", "/tmp/backup-manifest.json"}, {"start-klipper", "--printer", "plus4.invalid", "--confirm", "START-KLIPPER", "--service", "moonraker.service"}} {
		if err := b.execute(args); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
	if calls != 0 {
		t.Fatal("unsafe target reached transport")
	}
}
func TestNativeStartChecksStabilityAndReturnsRollbackHint(t *testing.T) {
	b := nativeTestBackend(t)
	states := 0
	b.transport = func(command string, _ []byte) ([]byte, int, error) {
		if strings.HasPrefix(command, "systemctl is-active") {
			states++
			return []byte("failed\n"), 3, fmt.Errorf("failed")
		}
		return nil, 0, nil
	}
	manifest := "/home/mks/scripts/plr/backups/plr-hardening-test/backup-manifest.json"
	err := b.execute([]string{"start-klipper", "--printer", "plus4.invalid", "--confirm", "START-KLIPPER", "--rollback-manifest", manifest})
	if err == nil {
		t.Fatal("failed start accepted")
	}
	receipt, err := ParsePLROperation(b.out.(*bytes.Buffer).Bytes(), KlipperStartSchema)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.RollbackManifest != manifest || states != 1 || len(receipt.StabilitySamples) != 1 {
		t.Fatal("activation failure lost recovery/stability evidence")
	}
	if err := ValidatePLRLifecycle(receipt); err != nil {
		t.Fatal(err)
	}
}
