package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/printerlab/config"
)

func runComponents(t *testing.T, path string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := NewCommand(strings.NewReader(""), &out, &errOut)
	cmd.SetArgs(append([]string{"--config", path, "components"}, args...))
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func componentConfig(t *testing.T, endpoint string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := fmt.Sprintf(`# Fleet notes
printers:
  - name: orange
    address: orange.invalid
    moonrakerURL: %q
    tags: [voron]
    components:
      hotend.upgrade:
        type: hotend
        manufacturer: Phaetus
        model: neXt G
        status: spare
        notes: Variant unconfirmed # Keep evidence
  - name: other
    address: other.invalid
    moonrakerURL: %q
groups:
  workshop: [orange, other]
`, endpoint, endpoint)
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestComponentRecordsPersistPartialUpdatesWithoutContactingPrinters(t *testing.T) {
	path := componentConfig(t, "http://127.0.0.1:1")
	_, _, err := runComponents(t, path, "set", "orange", "hotend.upgrade", "--status", "planned", "--notes", "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runComponents(t, path, "set", "orange", "probe", "--type", "probe", "--model", "Beacon Rev H", "--config-section", "beacon")
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := runComponents(t, path, "#voron", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var reports []componentReport
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Printer != "orange" {
		t.Fatalf("reports = %#v", reports)
	}
	part := reports[0].Components["hotend.upgrade"]
	if part.Model != "neXt G" || part.Manufacturer != "Phaetus" || part.Status != "planned" || part.Notes != "" {
		t.Fatalf("partial update lost fields or did not clear notes: %#v", part)
	}
	if reports[0].Components["probe"].ConfigSection != "beacon" {
		t.Fatal("lost section link")
	}
	before, _ := os.ReadFile(path)
	for _, args := range [][]string{
		{"set", "orange", "broken", "--type", "hotend"},
		{"set", "orange", "hotend.upgrade", "--status", "invented"},
		{"set", "orange", "hotend.upgrade", "--model", " "},
		{"set", "orange", " ", "--type", "fan", "--model", "5015"},
		{"set", "all", "probe", "--model", "wrong target"},
		{"remove", "orange", "missing"},
	} {
		if _, _, err := runComponents(t, path, args...); err == nil {
			t.Fatalf("accepted %v", args)
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatalf("invalid edit changed file: %v", args)
		}
	}
	_, _, err = runComponents(t, path, "remove", "orange", "hotend.upgrade")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.Inventory.Printer("orange")
	if _, exists := p.Components["hotend.upgrade"]; exists {
		t.Fatal("remove did not persist")
	}
	if _, err := cfg.Inventory.Select("@workshop"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("# Fleet notes")) {
		t.Fatal("lost unrelated comment")
	}
	backups, _ := filepath.Glob(path + ".before-components-*")
	if len(backups) != 3 {
		t.Fatalf("backups = %v", backups)
	}
}

func TestComponentCaptureDryRunSaveRefreshAndOfflineRead(t *testing.T) {
	sections := `"mcu":{"serial":"/dev/serial/by-id/usb-main"},"extruder":{"sensor_type":"PT1000","nozzle_diameter":"0.4"},"beacon":{"serial":"/dev/serial/by-id/usb-Beacon_RevH_TEST"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/printer/objects/query" || r.URL.Query().Get("configfile") != "config" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		fmt.Fprintf(w, `{"result":{"status":{"configfile":{"config":{%s,"gcode_macro SECRET":{"gcode":"credential=secret"}}}}}}`, sections)
	}))
	defer server.Close()
	path := componentConfig(t, server.URL)
	before, _ := os.ReadFile(path)
	out, _, err := runComponents(t, path, "capture", "orange", "--dry-run", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PT1000") || strings.Contains(out, "credential") {
		t.Fatalf("output = %s", out)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("dry-run wrote inventory")
	}
	if _, _, err := runComponents(t, path, "capture", "@workshop"); err != nil {
		t.Fatal(err)
	}
	backupPaths, _ := filepath.Glob(path + ".before-components-*")
	if len(backupPaths) != 1 {
		t.Fatalf("fleet capture was not a single edit: %v", backupPaths)
	}
	backup, _ := os.ReadFile(backupPaths[0])
	if !bytes.Equal(before, backup) {
		t.Fatal("backup is not exact")
	}
	// A fresh capture removes disappeared configured hardware, but keeps parts.
	sections = `"mcu":{"serial":"/dev/serial/by-id/usb-new"}`
	if _, _, err := runComponents(t, path, "capture", "orange"); err != nil {
		t.Fatal(err)
	}
	server.Close()
	out, _, err = runComponents(t, path, "all", "-o", "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "neXt G") || !strings.Contains(out, "usb-new") {
		t.Fatalf("offline output = %s", out)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	orange, _ := cfg.Inventory.Printer("orange")
	other, _ := cfg.Inventory.Printer("other")
	if len(orange.ComponentSnapshot.Components) != 1 || len(other.ComponentSnapshot.Components) != 3 {
		t.Fatal("snapshot replacement affected other printers or retained stale hardware")
	}
	if orange.Components["hotend.upgrade"].Status != "spare" {
		t.Fatal("capture changed manual identity")
	}
}

func TestComponentCaptureFailureDoesNotSaveAnySnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"result":{"status":{"configfile":{"config":{"mcu":{}}}}}}`)
	}))
	defer server.Close()
	path := componentConfig(t, server.URL)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Change only the second printer endpoint, as an external inventory editor would.
	updated := strings.Replace(string(raw), "name: other\n    address: other.invalid\n    moonrakerURL: \""+server.URL+"\"", "name: other\n    address: other.invalid\n    moonrakerURL: \"http://127.0.0.1:1\"", 1)
	if updated == string(raw) {
		t.Fatal("test did not change second endpoint")
	}
	if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	_, _, err = runComponents(t, path, "capture", "all")
	if err == nil || !strings.Contains(err.Error(), "no snapshots saved") {
		t.Fatalf("err = %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed capture partially saved inventory")
	}
}

func TestComponentCaptureRejectsConcurrentInventoryEdit(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			return
		}
		updated := strings.Replace(string(raw), "address: orange.invalid", "address: new.invalid", 1)
		if err := os.WriteFile(path, []byte(updated), 0600); err != nil {
			t.Error(err)
			return
		}
		fmt.Fprint(w, `{"result":{"status":{"configfile":{"config":{"mcu":{}}}}}}`)
	}))
	defer server.Close()
	path = componentConfig(t, server.URL)
	_, _, err := runComponents(t, path, "capture", "orange")
	if err == nil || !strings.Contains(err.Error(), "changed during capture") {
		t.Fatalf("err = %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := cfg.Inventory.Printer("orange")
	if p.Address != "new.invalid" || p.ComponentSnapshot != nil {
		t.Fatal("concurrent edit lost or stale snapshot saved")
	}
}
