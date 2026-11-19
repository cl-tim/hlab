package migrate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var plrAssets = []string{"install.py", "patch_virtual_sdcard.py", "plr_checkpoint.py", "generate_recovery.py", "plr.sh.in", "manifest.json"}

func (b *nativeBackend) plrLayout() (PLRPaths, error) {
	if b.value("service") != KlipperService {
		return PLRPaths{}, fmt.Errorf("PLR operations target exactly %s", KlipperService)
	}
	paths, err := CanonicalPLRPaths(b.account())
	if err != nil {
		return paths, err
	}
	for key, expected := range map[string]string{"klipper-dir": paths.KlipperDir, "printer-data": paths.PrinterData, "scripts-dir": paths.ScriptsDir, "remote-python": paths.Python} {
		if b.value(key) != expected {
			return paths, fmt.Errorf("PLR --%s must match printer account %s: %s", key, b.account(), expected)
		}
	}
	return paths, nil
}
func validateRemotePLRIdentity(doc map[string]any, account string, paths PLRPaths) error {
	raw, err := json.Marshal(doc["paths"])
	if err != nil {
		return err
	}
	var actual PLRPaths
	if err := json.Unmarshal(raw, &actual); err != nil {
		return err
	}
	if doc["account"] != account || actual != paths {
		return fmt.Errorf("remote PLR result did not match validated account and paths")
	}
	return nil
}
func (b *nativeBackend) remotePLRJSON(command string, paths PLRPaths) (map[string]any, error) {
	raw, code, runErr := b.sshCapture(command, nil)
	doc, err := decodeObject(raw)
	if err != nil {
		return nil, fmt.Errorf("remote PLR installer returned invalid JSON: %w (%v)", err, runErr)
	}
	if err := validateRemotePLRIdentity(doc, b.account(), paths); err != nil {
		return nil, err
	}
	if code != 0 || doc["ok"] != true {
		return doc, fmt.Errorf("remote PLR installer failed: %v (%v)", doc["error"], runErr)
	}
	return doc, nil
}
func handleText(value any) string { text, _ := value.(string); return text }
func (b *nativeBackend) plrTransaction() error {
	rollback := b.stage == "rollback-plr-hardening"
	confirmation := "DEPLOY-PLR-HARDENING"
	schema := PLRDeploymentSchema
	if rollback {
		confirmation = "ROLLBACK-PLR-HARDENING"
		schema = PLRRollbackSchema
	}
	if err := b.confirm(confirmation); err != nil {
		return err
	}
	paths, err := b.plrLayout()
	if err != nil {
		return err
	}
	backup := ""
	if rollback {
		backup, err = b.required("backup-manifest")
		if err != nil {
			return err
		}
		if err := ValidatePLRBackupManifest(backup, b.account()); err != nil {
			return err
		}
	}
	files := map[string][]byte{}
	for _, name := range plrAssets {
		raw, err := b.asset("plr-hardening/" + name)
		if err != nil {
			return err
		}
		files[name] = raw
	}
	bundle, err := tarFiles(files)
	if err != nil {
		return err
	}
	phase := "staging"
	stopped := false
	var plan, applied, verification map[string]any
	safety := ""
	remoteStage := ""
	operation := func() error {
		raw, _, err := b.sshCapture("mktemp -d /tmp/qidi-plr-hardening.XXXXXXXX", nil)
		if err != nil {
			return err
		}
		remoteStage = strings.TrimSpace(string(raw))
		if !regexp.MustCompile(`^/tmp/qidi-plr-hardening\.[A-Za-z0-9]+$`).MatchString(remoteStage) {
			remoteStage = ""
			return fmt.Errorf("unsafe remote PLR staging path")
		}
		defer b.sshCapture("rm -rf -- "+quote(remoteStage), nil)
		if _, _, err := b.sshCapture("tar -xf - -C "+quote(remoteStage), bundle); err != nil {
			return err
		}
		transactionID := time.Now().UTC().Format("20060102T150405Z") + "-" + randomToken()
		parts := []string{"sudo", "-n", paths.Python, remoteStage + "/install.py", "--printer-user", b.account(), "--transaction-id", transactionID, "--klipper-dir", paths.KlipperDir, "--printer-data", paths.PrinterData, "--scripts-dir", paths.ScriptsDir, "--python", paths.Python, "--service", KlipperService, "--json"}
		if rollback {
			parts = append(parts, "--rollback-manifest", backup)
		}
		command := quoted(parts...)
		phase = "plan"
		plan, err = b.remotePLRJSON(command, paths)
		if err != nil {
			return err
		}
		changed, ok := plan["changed"].(bool)
		if !ok {
			return fmt.Errorf("PLR plan omitted boolean changed")
		}
		var code int
		if rollback {
			if plan["backup_manifest"] != backup {
				return fmt.Errorf("rollback plan changed backup manifest")
			}
			if changed {
				safety = handleText(plan["safety_manifest"])
				if !validPLRManifest(safety, paths.ScriptsDir, "plr-rollback-safety-", "safety-manifest.json") {
					return fmt.Errorf("rollback plan omitted canonical safety manifest")
				}
			} else if plan["safety_manifest"] != nil {
				return fmt.Errorf("unchanged rollback plan returned a safety manifest")
			}
		} else {
			if changed {
				backup = handleText(plan["backup_manifest"])
				if err := ValidatePLRBackupManifest(backup, b.account()); err != nil {
					return err
				}
			} else if plan["backup_manifest"] != nil {
				return fmt.Errorf("unchanged deployment plan returned a backup manifest")
			}
		}
		if !changed {
			return nil
		}
		phase = "stop"
		if _, _, err := b.sshCapture("sudo -n systemctl stop "+quote(KlipperService), nil); err != nil {
			return err
		}
		raw, code, _ = b.sshCapture("systemctl is-active "+quote(KlipperService), nil)
		if code != 3 || strings.TrimSpace(string(raw)) != "inactive" {
			return fmt.Errorf("could not prove Klipper inactive after stop (exit %d, state=%q)", code, strings.TrimSpace(string(raw)))
		}
		stopped = true
		phase = "apply"
		applied, err = b.remotePLRJSON(command+" --apply --klipper-stopped", paths)
		if err != nil {
			return err
		}
		if applied["backup_manifest"] != backup {
			return fmt.Errorf("PLR installer changed its predeclared backup manifest")
		}
		if rollback && applied["safety_manifest"] != safety {
			return fmt.Errorf("rollback changed its predeclared safety manifest")
		}
		phase = "verify"
		verification, err = b.remotePLRJSON(command, paths)
		if err != nil {
			return err
		}
		if verification["changed"] != false {
			return fmt.Errorf("post-install PLR verification is not idempotent")
		}
		if rollback && verification["backup_manifest"] != backup {
			return fmt.Errorf("rollback verification changed its backup manifest")
		}
		return nil
	}
	err = operation()
	// Accept recovery state only from a receipt with the validated handles.
	clean := func(doc map[string]any) map[string]any {
		if doc == nil {
			return nil
		}
		if value := doc["backup_manifest"]; value != nil && value != backup {
			return nil
		}
		if rollback {
			if value := doc["safety_manifest"]; value != nil && value != safety {
				return nil
			}
		} else if doc["safety_manifest"] != nil {
			return nil
		}
		return doc
	}
	plan = clean(plan)
	applied = clean(applied)
	verification = clean(verification)
	changed := plan != nil && plan["changed"] == true
	state := "not_started"
	var appliedProof any = false
	ok := err == nil
	if ok && changed {
		appliedProof = true
		state = "applied"
		if rollback {
			state = "rolled_back"
		}
	}
	if err != nil && stopped {
		state = "indeterminate"
		appliedProof = nil
		remoteDoc := plan
		if phase == "apply" {
			remoteDoc = applied
		} else if phase == "verify" {
			remoteDoc = verification
		}
		if remoteDoc != nil && !(phase == "verify" && applied != nil) {
			remoteState := handleText(remoteDoc["transaction_state"])
			allowed := remoteState == "not_started" || remoteState == "partial" || remoteState == "indeterminate" || (!rollback && remoteState == "auto_rolled_back") || (rollback && remoteState == "safety_restored")
			if allowed {
				state = remoteState
				if state == "not_started" || state == "auto_rolled_back" || state == "safety_restored" {
					appliedProof = false
				}
			}
		}
	}
	var exposedBackup, exposedSafety any
	if backup != "" && (rollback || state != "not_started") {
		exposedBackup = backup
	}
	if safety != "" && state != "not_started" {
		exposedSafety = safety
	}
	result := map[string]any{"schema": schema, "ok": ok, "changed": changed, "applied": appliedProof, "verified": ok, "klipper_left_stopped": stopped, "transaction_state": state, "account": b.account(), "paths": paths, "backup_manifest": exposedBackup, "safety_manifest": exposedSafety, "plan": plan}
	if rollback {
		result["rollback"] = applied
	} else {
		result["installation"] = applied
	}
	result["verification"] = verification
	if err != nil {
		result["phase"] = phase
		result["service"] = KlipperService
		result["error"] = err.Error()
		if exposedBackup != nil {
			result["next_command"] = "rollback-plr-hardening --backup-manifest " + backup + " --confirm ROLLBACK-PLR-HARDENING"
		}
		if stopped {
			fmt.Fprintln(b.errOut, "PLR operation left klipper.service stopped after failure.")
		}
	}
	if writeErr := b.writeJSON(result); writeErr != nil {
		return writeErr
	}
	return err
}
func (b *nativeBackend) startKlipper() error {
	if err := b.confirm("START-KLIPPER"); err != nil {
		return err
	}
	if b.value("service") != KlipperService {
		return fmt.Errorf("start targets exactly %s", KlipperService)
	}
	manifest := b.value("rollback-manifest")
	if manifest != "" {
		if err := ValidatePLRBackupManifest(manifest, b.account()); err != nil {
			return err
		}
		paths, err := CanonicalPLRPaths(b.account())
		if err != nil {
			return err
		}
		if b.value("scripts-dir") != paths.ScriptsDir {
			return fmt.Errorf("scripts directory does not match printer account")
		}
	}
	_, startExit, _ := b.sshCapture("sudo -n systemctl start "+quote(KlipperService), nil)
	samples := []map[string]any{}
	state := "unknown"
	stateExit := -1
	for index := 0; index < 5; index++ {
		raw, code, _ := b.sshCapture("systemctl is-active "+quote(KlipperService), nil)
		state = strings.TrimSpace(string(raw))
		if state == "" {
			state = "unknown"
		}
		stateExit = code
		samples = append(samples, map[string]any{"state": state, "exit": code})
		if code != 0 || state != "active" {
			break
		}
		if index < 4 {
			if err := waitContext(b.ctx, time.Second); err != nil {
				return err
			}
		}
	}
	ok := startExit == 0 && len(samples) == 5 && stateExit == 0 && state == "active"
	var hint any
	if manifest != "" {
		hint = manifest
	}
	result := map[string]any{"schema": KlipperStartSchema, "ok": ok, "service": KlipperService, "observed_state": state, "start_exit": startExit, "state_exit": stateExit, "stability_checks_required": 5, "stability_interval_seconds": 1.0, "stability_samples": samples, "config_or_file_reads_performed": false, "rollback_manifest": hint}
	if !ok {
		result["error"] = "Klipper activation did not remain active for the bounded stability window"
		if manifest != "" {
			result["next_command"] = "rollback-plr-hardening --backup-manifest " + manifest + " --confirm ROLLBACK-PLR-HARDENING"
		}
	}
	if err := b.writeJSON(result); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Klipper start stability verification failed")
	}
	return nil
}
