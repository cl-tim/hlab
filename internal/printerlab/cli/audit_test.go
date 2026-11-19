package cli

import (
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func TestAuditSnapshot(t *testing.T) {
	result := auditSnapshot(moonraker.Snapshot{
		Name: "printer-b", Firmware: "mainline", Reachable: true,
		KlippyState: "ready", PrintState: "standby",
	})
	if result.Result != "pass" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAuditSnapshotWarnsForAutoFirmware(t *testing.T) {
	result := auditSnapshot(moonraker.Snapshot{
		Name: "printer-b", Firmware: "auto", Reachable: true,
		KlippyState: "ready", PrintState: "standby",
	})
	if result.Result != "warn" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestAuditSnapshotIncludesKlippyShutdownMessage(t *testing.T) {
	result := auditSnapshot(moonraker.Snapshot{
		Name: "printer-b", Firmware: "stock", Reachable: true,
		KlippyState: "shutdown", KlippyStateMessage: "MCU mcu_box1 shutdown: Timer too close",
		PrintState: "standby",
	})
	if result.Result != "fail" || len(result.Issues) != 1 ||
		result.Issues[0] != "Klippy state is shutdown: MCU mcu_box1 shutdown: Timer too close" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
