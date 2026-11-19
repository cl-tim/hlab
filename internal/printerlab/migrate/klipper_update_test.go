package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func updateReceipt(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(KlipperUpdateResult{
		Schema: 1, OK: true, Status: "current", Account: "mks", CheckOnly: true,
		Before: strings.Repeat("a", 40), Target: strings.Repeat("a", 40), After: strings.Repeat("a", 40),
		Version: "v0.13.0-787-ge2b99a915-dirty",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNativeKlipperUpdateStreamsHelperAndKeyAsPrinterOwner(t *testing.T) {
	b := nativeTestBackend(t)
	t.Setenv("TEST_KLIPPER_API_KEY", "test-private-key")
	b.transport = func(command string, payload []byte) ([]byte, int, error) {
		if !strings.HasPrefix(command, "sudo -n -u 'mks' -H bash -c ") || strings.Contains(command, "test-private-key") {
			t.Fatalf("unsafe command: %s", command)
		}
		if !bytes.Contains(payload, []byte("test-private-key")) || !bytes.Contains(payload, []byte("class KlipperUpdate")) {
			t.Fatal("helper or private input missing from stdin")
		}
		return updateReceipt(t), 0, nil
	}
	if err := b.execute([]string{"update-klipper", "--printer", "fixturePrinterB.example", "--user", "plab", "--printer-user", "mks", "--moonraker-port", "7125", "--wait-seconds", "180", "--api-key-env", "TEST_KLIPPER_API_KEY", "--check"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseKlipperUpdate(b.out.(*bytes.Buffer).Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestNativeKlipperUpdateRequiresConfirmationBeforeTransport(t *testing.T) {
	b := nativeTestBackend(t)
	b.transport = func(string, []byte) ([]byte, int, error) {
		t.Fatal("unapproved update reached SSH")
		return nil, 1, fmt.Errorf("unexpected")
	}
	if err := b.execute([]string{"update-klipper"}); err == nil || !strings.Contains(err.Error(), KlipperUpdateAction) {
		t.Fatalf("error = %v", err)
	}
}

func TestKlipperUpdateReceiptRejectsContradictorySuccess(t *testing.T) {
	for _, mutate := range []func(*KlipperUpdateResult){
		func(r *KlipperUpdateResult) { r.After = strings.Repeat("b", 40) },
		func(r *KlipperUpdateResult) { r.Version = "" },
		func(r *KlipperUpdateResult) { r.Status = "updated" },
		func(r *KlipperUpdateResult) { r.Status = "available" },
		func(r *KlipperUpdateResult) { r.Error = "failed" },
	} {
		var result KlipperUpdateResult
		json.Unmarshal(updateReceipt(t), &result)
		mutate(&result)
		raw, _ := json.Marshal(result)
		if _, err := ParseKlipperUpdate(raw); err == nil {
			t.Fatalf("contradictory receipt accepted: %s", raw)
		}
	}
	if _, err := ParseKlipperUpdate(append(updateReceipt(t), []byte(` {"schema":1}`)...)); err == nil {
		t.Fatal("multiple JSON receipts accepted")
	}
}
