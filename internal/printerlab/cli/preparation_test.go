package cli

import (
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/journal"
	"github.com/cl-tim/homelab-cli/internal/printerlab/probe"
	"github.com/cl-tim/homelab-cli/internal/printerlab/stages"
	"testing"
	"time"
)

func TestOldBackupExpiresOnlyBeforeStockCutover(t *testing.T) {
	now := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	book := &journal.Journal{Entries: map[string]journal.Entry{
		"backup": {Attempt: stages.AttemptCompleted, Finished: now.Add(-48 * time.Hour)},
		"audit":  {Attempt: stages.AttemptCompleted, Finished: now.Add(-time.Hour)},
	}}
	p := inventory.Printer{Firmware: inventory.FirmwareStock}
	got := preparationAttempts(book, p, now)
	if got["backup"] != stages.AttemptNone || got["audit"] != stages.AttemptCompleted {
		t.Fatal(got)
	}
	if book.Entries["backup"].Attempt != stages.AttemptCompleted {
		t.Fatal("original journal was modified")
	}
	book.Entries["stage-mainboard"] = journal.Entry{Attempt: stages.AttemptStarted}
	if preparationAttempts(book, p, now)["backup"] != stages.AttemptCompleted {
		t.Fatal("cutover recovery evidence expired")
	}
	delete(book.Entries, "stage-mainboard")
	p.Firmware = inventory.FirmwareMainline
	if preparationAttempts(book, p, now)["backup"] != stages.AttemptCompleted {
		t.Fatal("commissioned printer's evidence expired")
	}
}

func TestCommissionedBaselineRequiresMainlineProvenanceAndReadyConfig(t *testing.T) {
	report := engineReport{Probes: []probe.Result{
		{Probe: "config.installed", Status: probe.Satisfied},
		{Probe: "mcu.online", Status: probe.Satisfied},
		{Probe: "bootstrap.receipt", Status: probe.Satisfied},
	}}
	if !commissioned(report) {
		t.Fatal("healthy commissioned printer would repeat historical migration")
	}
	for i := range report.Probes {
		report.Probes[i].Status = probe.Indeterminate
		if commissioned(report) {
			t.Fatal("incomplete evidence hid unfinished migration")
		}
		report.Probes[i].Status = probe.Satisfied
	}
}
