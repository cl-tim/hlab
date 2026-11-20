package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	plabconfig "github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func TestStockFirmwareCheckUsesVendorBackend(t *testing.T) {
	fixture := newPLRFixture(t, "http://127.0.0.1:1")
	if err := plabconfig.Edit(fixture.configPath, "fixturePrinterA", nil, map[string]string{"firmware": "stock"}); err != nil {
		t.Fatal(err)
	}
	result := migrate.QIDIUpdateResult{Schema: 1, OK: true, Status: "checked", Model: "plus4", Target: "2.1.1", Before: "1.8.2", After: "1.8.2", CheckOnly: true, ConfigPolicy: "preserve", ScreenStatus: "pending"}
	raw, _ := json.Marshal(result)
	setMigrationHelper(t, fixture, string(raw))
	stdout, _, err := executeFirmwareUpdate(t, fixture.configPath, "fixturePrinterA", "--version", "2.1.1", "--check", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"status": "checked"`) {
		t.Fatalf("lost receipt: %s", stdout)
	}
	args := strings.Join(readMigrationArgs(t, fixture), " ")
	for _, want := range []string{"update-qidi", "--firmware-version 2.1.1", "--model plus4", "--check", "--config-policy preserve"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %s in %s", want, args)
		}
	}
	if strings.Contains(args, "--confirm") || strings.Contains(args, "update-klipper") {
		t.Fatalf("incorrect stock check arguments: %s", args)
	}
}

func TestFirmwareCatalogWorksWithoutInventory(t *testing.T) {
	var stdout, stderr bytes.Buffer
	command := NewCommand(strings.NewReader(""), &stdout, &stderr)
	command.SetArgs([]string{"--config", "missing-inventory.yaml", "firmware", "releases", "plus4", "-o", "json"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "2.1.1") || !json.Valid(stdout.Bytes()) {
		t.Fatalf("invalid catalog output: %s", stdout.String())
	}
}
