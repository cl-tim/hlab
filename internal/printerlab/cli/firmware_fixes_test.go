package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func executeFixes(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := NewCommand(strings.NewReader(""), &out, &errOut)
	cmd.SetArgs(append([]string{"firmware", "fixes"}, args...))
	err := cmd.Execute()
	return out.String(), err
}

func TestDirectFixesBypassesInventoryAndForwardsOptions(t *testing.T) {
	fixture, home := directFirmwareFixture(t, true)
	receipt := migrate.QIDIFixesResult{Schema: 1, OK: true, Status: "checked", Model: "plus4", Version: "2.1.1", CheckOnly: true, Polling: true, TrimRunoutWaits: true, USBEventLog: true, FastProbing: true, BoxDrying: true, FastVariableSaves: true}
	raw, _ := json.Marshal(receipt)
	setMigrationHelper(t, fixture, string(raw))
	out, err := executeFixes(t, "--target", "printer.example", "--user", "ops", "--identity", "/fixture/key", "--ssh-port", "2222", "--moonraker-port", "17125", "--api-key-env", "FIXTURE_KEY", "--box-motion-polling", "--trim-runout-waits", "--usb-event-log", "--fast-probing", "--box-drying", "--fast-variable-saves", "--check", "--config", "missing.yaml", "-o", "json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("check failed: %v %s", err, out)
	}
	args := strings.Join(readMigrationArgs(t, fixture), " ")
	for _, want := range []string{"qidi-fixes", "--printer printer.example", "--printer-user mks", "--user ops", "--port 2222", "--identity /fixture/key", "--moonraker-port 17125", "--api-key-env FIXTURE_KEY", "--box-motion-polling", "--trim-runout-waits", "--usb-event-log", "--fast-probing", "--box-drying", "--fast-variable-saves", "--check"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %s in %s", want, args)
		}
	}
	if strings.Contains(args, "--confirm") {
		t.Fatal("check forwarded apply authorization")
	}
	if _, err := os.Stat(filepath.Join(home, "config.yaml")); !os.IsNotExist(err) {
		t.Fatal("direct fixes created inventory")
	}
}

func TestFixesRejectsUnsafeRequestsBeforeBackend(t *testing.T) {
	for _, args := range [][]string{
		{"--target", "printer.example"},
		{"--target", "-oProxyCommand=bad", "--check"},
		{"saved", "--target", "printer.example", "--check"},
		{"--target", "printer.example", "--rollback", "/tmp/wrong", "--check"},
		{"--target", "printer.example", "--box-motion-polling", "--rollback", "/tmp/wrong", "-y"},
		{"--target", "printer.example", "--trim-runout-waits", "--rollback", "/tmp/wrong", "-y"},
		{"--target", "printer.example", "--usb-event-log", "--rollback", "/tmp/wrong", "-y"},
		{"--target", "printer.example", "--fast-probing", "--rollback", "/tmp/wrong", "-y"},
		{"--target", "printer.example", "--box-drying", "--rollback", "/tmp/wrong", "-y"},
		{"--target", "printer.example", "--fast-variable-saves", "--rollback", "/tmp/wrong", "-y"},
		{"--target", "printer.example", "--wait-timeout", "1s", "--check"},
	} {
		fixture, _ := directFirmwareFixture(t, true)
		if _, err := executeFixes(t, args...); err == nil {
			t.Fatalf("unsafe request accepted: %v", args)
		}
		if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
			t.Fatal("invalid request reached backend")
		}
	}
}
