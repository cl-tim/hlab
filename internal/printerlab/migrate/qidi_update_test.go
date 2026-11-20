package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func qidiReceipt(t *testing.T, status string) []byte {
	t.Helper()
	result := QIDIUpdateResult{Schema: 1, OK: true, Status: status, Model: "plus4", Target: "2.1.1", Before: "1.8.2", After: "1.8.2", ConfigPolicy: "preserve", ScreenStatus: "pending"}
	if status == "checked" {
		result.CheckOnly = true
	}
	if status == "prepared" || status == "updated" {
		result.Backup = "/var/lib/hlab/qidi-updates/" + strings.Repeat("a", 32)
		result.BackupSHA256 = hashBytes([]byte("recovery fixture"))
	}
	if status == "updated" {
		result.After, result.ScreenStatus, result.LocalBackup = "2.1.1", "acknowledged", "fixture/recovery.tar.gz"
		result.ConfigVerified, result.StartupVerified = true, true
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestQIDIReceiptRejectsIncompleteUpgradeEvidence(t *testing.T) {
	for _, mutate := range []func(*QIDIUpdateResult){
		func(r *QIDIUpdateResult) { r.StartupVerified = false },
		func(r *QIDIUpdateResult) { r.ConfigVerified = false },
		func(r *QIDIUpdateResult) { r.ScreenStatus = "transfer_started" },
		func(r *QIDIUpdateResult) { r.After = "1.8.2" },
		func(r *QIDIUpdateResult) { r.LocalBackup = "" },
		func(r *QIDIUpdateResult) { r.Backup = "/tmp/wrong" },
		func(r *QIDIUpdateResult) { r.BackupSHA256 = "" },
		func(r *QIDIUpdateResult) { r.CheckOnly = true },
	} {
		var result QIDIUpdateResult
		json.Unmarshal(qidiReceipt(t, "updated"), &result)
		mutate(&result)
		raw, _ := json.Marshal(result)
		if _, err := ParseQIDIUpdate(raw); err == nil {
			t.Fatalf("accepted contradictory success: %s", raw)
		}
	}
	for _, raw := range [][]byte{append(qidiReceipt(t, "updated"), []byte(` {}`)...), []byte(`{"schema":1,"schema":1}`), []byte(`{"schema":1}`)} {
		if _, err := ParseQIDIUpdate(raw); err == nil {
			t.Fatalf("accepted invalid receipt: %s", raw)
		}
	}
}

func TestQIDIUpgradeCopiesAndVerifiesRecoveryBeforeApply(t *testing.T) {
	b := nativeTestBackend(t)
	var phases []string
	b.transport = func(command string, payload []byte) ([]byte, int, error) {
		if strings.HasPrefix(command, "sudo -n cat -- ") {
			phases = append(phases, "copy")
			return []byte("recovery fixture"), 0, nil
		}
		if command != "sudo -n python3 -I -" || !bytes.Contains(payload, []byte("class QIDIUpdate")) {
			t.Fatalf("unexpected transport %s", command)
		}
		if bytes.Contains(payload[:bytes.Index(payload, []byte("#!/usr/bin/env"))], []byte(`\"phase\":\"apply\"`)) {
			phases = append(phases, "apply")
			matches, _ := filepath.Glob(filepath.Join(b.home, "backups", "qidi-firmware", "*", "recovery.tar.gz"))
			if len(matches) != 1 {
				t.Fatal("apply started before a desktop backup existed")
			}
			digest, _ := hashFile(matches[0])
			if digest != hashBytes([]byte("recovery fixture")) {
				t.Fatal("desktop backup was not verified")
			}
			var result QIDIUpdateResult
			json.Unmarshal(qidiReceipt(t, "updated"), &result)
			result.LocalBackup = matches[0]
			raw, _ := json.Marshal(result)
			return raw, 0, nil
		}
		phases = append(phases, "prepare")
		return qidiReceipt(t, "prepared"), 0, nil
	}
	if err := b.execute([]string{"update-qidi", "--printer", "fixture.example", "--user", "plab", "--printer-user", "mks", "--model", "plus4", "--firmware-version", "2.1.1", "--config-policy", "preserve", "--moonraker-port", "7125", "--wait-seconds", "180", "--confirm", QIDIUpdateAction}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(phases, ",") != "prepare,copy,apply" {
		t.Fatalf("incorrect transaction order: %v", phases)
	}
}

func TestQIDICorruptDesktopBackupNeverReachesInstall(t *testing.T) {
	b := nativeTestBackend(t)
	var calls int
	b.transport = func(command string, payload []byte) ([]byte, int, error) {
		calls++
		if calls == 1 {
			return qidiReceipt(t, "prepared"), 0, nil
		}
		if calls == 2 && strings.HasPrefix(command, "sudo -n cat -- ") {
			return []byte("corrupted"), 0, nil
		}
		t.Fatal("corrupt backup reached apply")
		return nil, 1, fmt.Errorf("unexpected")
	}
	err := b.execute([]string{"update-qidi", "--printer", "fixture.example", "--user", "mks", "--model", "plus4", "--firmware-version", "2.1.1", "--config-policy", "preserve", "--moonraker-port", "7125", "--wait-seconds", "180", "--confirm", QIDIUpdateAction})
	if err == nil || !strings.Contains(err.Error(), "backup verification failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestQIDIUnsupportedReleaseAndMissingApprovalNeverConnect(t *testing.T) {
	for _, args := range [][]string{{"update-qidi"}, {"update-qidi", "--confirm", QIDIUpdateAction, "--printer", "fixture.example", "--user", "mks", "--model", "q1pro", "--firmware-version", "2.1.1"}} {
		b := nativeTestBackend(t)
		b.transport = func(string, []byte) ([]byte, int, error) { t.Fatal("invalid request reached SSH"); return nil, 1, nil }
		if err := b.execute(args); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}

func TestQIDIReviewedCatalogHasNoMachineSpecificValues(t *testing.T) {
	releases, err := QIDIReleases()
	if err != nil || len(releases) == 0 {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := QIDIReleaseFor("plus4", "2.1.1"); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(releases)
	for _, forbidden := range []string{"qidi1", "qidi2", "C:\\Users", "id_plab", "beacon_serial"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("machine-specific value %q in release catalog", forbidden)
		}
	}
}
