package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/discover"
)

// What --emit prints is meant to go into the hlab config, so it has to be a
// printer section the loader accepts there, beside hosts.
func TestEmittedFragmentLoadsAsAnInventory(t *testing.T) {
	found := []discover.Result{
		{Address: "192.0.2.60", Hostname: "printer-a"},
		{Address: "192.0.2.61", Hostname: "printer-a"},
		{Address: "192.0.2.62"},
	}
	var out bytes.Buffer
	if err := output.Write(&out, output.FormatYAML, emitFragment(found, 7125), output.Table{}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), config.SectionKey+":\n") {
		t.Fatalf("fragment does not open with the %s section:\n%s", config.SectionKey, out.String())
	}

	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "hosts:\n  - name: nas\n    address: 192.0.2.5\n" + out.String()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("the emitted fragment does not load:\n%s\n%v", body, err)
	}
	if cfg.Layout != config.LayoutUnified {
		t.Errorf("layout = %v, want unified", cfg.Layout)
	}
	var names []string
	for _, printer := range cfg.Inventory.Printers() {
		names = append(names, printer.Name)
	}
	if got := strings.Join(names, ","); got != "printer-192-0-2-62,printer-a,printer-a-2" &&
		got != "printer-a,printer-a-2,printer-192-0-2-62" {
		t.Errorf("printers = %s", got)
	}
}

// No printers found must still be a section that loads, not `printers: null`.
func TestEmittedFragmentWithNothingFound(t *testing.T) {
	var out bytes.Buffer
	if err := output.Write(&out, output.FormatYAML, emitFragment(nil, 7125), output.Table{}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.Load(path); err != nil || len(cfg.Inventory.Printers()) != 0 {
		t.Errorf("empty fragment:\n%s\nerr %v", out.String(), err)
	}
}
