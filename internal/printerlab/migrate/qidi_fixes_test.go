package migrate

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixesReceipt(status string) QIDIFixesResult {
	r := QIDIFixesResult{Schema: 1, OK: true, Status: status, Model: "plus4", Version: "2.1.1"}
	if status == "checked" {
		r.CheckOnly = true
	}
	if status == "prepared" || status == "applied" || status == "rolled_back" {
		r.Backup = "/var/lib/hlab/qidi-fixes/" + strings.Repeat("a", 32)
		r.BackupSHA256 = hashBytes([]byte("fixes recovery"))
	}
	r.StartupVerified = status == "applied" || status == "rolled_back" || status == "unchanged"
	if status == "applied" {
		r.LocalBackup = "fixture/recovery.tar.gz"
	}
	return r
}

func TestFixesVerifiesDesktopBackupBeforeApply(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		b := nativeTestBackend(t)
		var phases []string
		b.transport = func(command string, payload []byte) ([]byte, int, error) {
			if strings.HasPrefix(command, "sudo -n cat -- ") {
				phases = append(phases, "copy")
				if corrupt {
					return []byte("bad"), 0, nil
				}
				return []byte("fixes recovery"), 0, nil
			}
			if command != "sudo -n python3 -I -" {
				t.Fatalf("unexpected transport %s", command)
			}
			firstLine := strings.SplitN(string(payload), "\n", 3)[1]
			var encoded string
			if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(firstLine, "PLAB_OPTIONS=json.loads("), ")")), &encoded); err != nil {
				t.Fatal(err)
			}
			var options map[string]any
			if err := json.Unmarshal([]byte(encoded), &options); err != nil {
				t.Fatal(err)
			}
			phase := options["phase"].(string)
			phases = append(phases, phase)
			result := fixesReceipt("prepared")
			if phase == "apply" {
				if corrupt {
					t.Fatal("corrupted backup authorized apply")
				}
				result = fixesReceipt("applied")
				result.LocalBackup = options["local_backup"].(string)
				if filepath.Base(result.LocalBackup) != "recovery.tar.gz" {
					t.Fatal("apply has no desktop recovery path")
				}
				if digest, err := hashFile(result.LocalBackup); err != nil || digest != result.BackupSHA256 {
					t.Fatal("apply happened before verified backup")
				}
			}
			if len(options["transaction_id"].(string)) != 32 {
				t.Fatal("transaction ID does not match recovery handle format")
			}
			raw, _ := json.Marshal(result)
			return raw, 0, nil
		}
		err := b.execute([]string{"qidi-fixes", "--printer", "fixture.example", "--model", "plus4", "--moonraker-port", "7125", "--wait-seconds", "5", "--confirm", QIDIFixesAction})
		want := []string{"prepare", "copy", "apply"}
		if corrupt {
			want = want[:2]
			if err == nil || !strings.Contains(err.Error(), "backup verification failed") {
				t.Fatalf("corrupt backup: %v", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(phases, want) {
			t.Fatalf("phases %v; want %v", phases, want)
		}
	}
}

func TestFixesRejectsContradictoryReceipts(t *testing.T) {
	for _, mutate := range []func(*QIDIFixesResult){
		func(r *QIDIFixesResult) { r.StartupVerified = false },
		func(r *QIDIFixesResult) { r.Backup = "/tmp/elsewhere" },
		func(r *QIDIFixesResult) { r.BackupSHA256 = "" },
		func(r *QIDIFixesResult) { r.CheckOnly = true },
		func(r *QIDIFixesResult) { r.Version = "9.0.0" },
		func(r *QIDIFixesResult) { r.Status = "checked" },
		func(r *QIDIFixesResult) { r.Error = "actually failed" },
	} {
		r := fixesReceipt("applied")
		mutate(&r)
		raw, _ := json.Marshal(r)
		if _, err := ParseQIDIFixes(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{}`, `{"schema":1,"schema":1}`, `{"Schema":1}`} {
		if _, err := ParseQIDIFixes([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestFixesInvalidBackendRequestsNeverConnect(t *testing.T) {
	for _, args := range [][]string{
		{"qidi-fixes"},
		{"qidi-fixes", "--confirm", QIDIFixesAction, "--printer", "printer.example", "--model", "q1pro"},
		{"qidi-fixes", "--confirm", QIDIFixesAction, "--printer", "printer.example", "--model", "plus4", "--rollback-manifest", "/tmp/wrong"},
	} {
		b := nativeTestBackend(t)
		b.transport = func(string, []byte) ([]byte, int, error) {
			t.Fatal("invalid request reached SSH")
			return nil, 1, fmt.Errorf("unexpected")
		}
		if err := b.execute(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
