package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostconfig "github.com/cl-tim/hlab/internal/config"
)

const unifiedConfig = `# The whole network.
defaults:
  user: ops # the server account, not the printer one
  connectTimeout: 5s

hosts:
  - name: nas
    address: 192.0.2.5
    tags: [storage]

groups:
  storage: [nas]

printerlab:
  defaults:
    moonrakerPort: 7130
  printers:
    - name: printer-a
      address: 192.0.2.60
      model: plus4
      firmware: stock
      tags: [plus4] # the one by the window
    - name: printer-b
      address: 192.0.2.61
  groups:
    farm: [printer-a, printer-b]
`

// One file, two readers. Each sees its own machines, its own defaults and its
// own groups, and neither mistakes the other's for its own — `user: ops` is
// the server account and must not become the account hlab logs into a printer
// with.
func TestOneFileHoldsHostsAndPrinters(t *testing.T) {
	hostHome, _ := isolate(t)
	path := write(t, filepath.Join(hostHome, "config.yaml"), unifiedConfig)

	printers, err := Load("")
	if err != nil {
		t.Fatalf("printer Load: %v", err)
	}
	if printers.Path != path || printers.Layout != LayoutUnified || !printers.Present || printers.Legacy {
		t.Errorf("Load = path %q layout %v present %v legacy %v", printers.Path, printers.Layout, printers.Present, printers.Legacy)
	}
	farm, err := printers.Inventory.Select("@farm")
	if err != nil || len(farm) != 2 {
		t.Fatalf("@farm = %v, %v; want both printers", farm, err)
	}
	if farm[0].User != "mks" || farm[0].MoonrakerPort != 7130 {
		t.Errorf("printer-a user %q port %d; want the printer defaults, not the host ones", farm[0].User, farm[0].MoonrakerPort)
	}
	if _, err := printers.Inventory.Select("@storage"); err == nil {
		t.Error("a host group resolved as a printer group")
	}
	if _, err := printers.Inventory.Select("nas"); err == nil {
		t.Error("a host resolved as a printer")
	}

	hosts, err := hostconfig.Load("")
	if err != nil {
		t.Fatalf("host Load: %v", err)
	}
	if len(hosts.Inventory.Hosts()) != 1 || hosts.Defaults.User != "ops" {
		t.Errorf("host side: %d host(s), user %q", len(hosts.Inventory.Hosts()), hosts.Defaults.User)
	}
	if _, err := hosts.Inventory.SelectAll("printer-a"); err == nil {
		t.Error("a printer resolved as a host: `hlab run all` would try to SSH into it as a server")
	}
}

// A typo in the printer section is the printer loader's to report. The host
// loader holds the section opaquely, so a broken printer entry cannot take
// `hlab run` away from the servers.
func TestPrinterSectionIsStrictForPrintersAndOpaqueToHosts(t *testing.T) {
	hostHome, _ := isolate(t)
	body := strings.Replace(unifiedConfig, "      model: plus4\n", "      modle: plus4\n", 1)
	write(t, filepath.Join(hostHome, "config.yaml"), body)

	if _, err := Load(""); err == nil || !strings.Contains(err.Error(), "modle") {
		t.Errorf("printer Load error = %v, want it to reject the unknown key", err)
	}
	if _, err := hostconfig.Load(""); err != nil {
		t.Errorf("host Load failed on a printer typo: %v", err)
	}
}

// Printers listed at the top level of a file that also lists hosts would be
// rejected by the host loader for reasons that never mention printers.
func TestTopLevelPrintersBesideHostsIsRefusedWithDirections(t *testing.T) {
	isolate(t)
	path := write(t, filepath.Join(t.TempDir(), "config.yaml"),
		"hosts: []\nprinters:\n  - name: printer-a\n    address: 192.0.2.60\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), SectionKey) {
		t.Errorf("error = %v, want one pointing at the %s section", err, SectionKey)
	}
}

func TestHlabConfigWithoutPrintersIsNotAnInventory(t *testing.T) {
	hostHome, _ := isolate(t)
	path := write(t, filepath.Join(hostHome, "config.yaml"), "hosts: []\n")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Present || cfg.Path != path || len(cfg.Inventory.Printers()) != 0 {
		t.Errorf("Present %v path %q printers %d; want an absent inventory in the hlab config", cfg.Present, cfg.Path, len(cfg.Inventory.Printers()))
	}
}

const standaloneConfig = `# Print farm
defaults:
  user: plab # management account
printers:
  - name: printer-a
    address: 192.0.2.60
    firmware: mainline
groups:
  farm: [printer-a]
`

// An inventory from before the merge keeps working where it is until the
// operator moves it, and the caller is told so it can say how.
func TestStandaloneInventoryAtTheOldLocationIsStillRead(t *testing.T) {
	hostHome, printerHome := isolate(t)
	legacy := write(t, filepath.Join(printerHome, "config.yaml"), standaloneConfig)

	for _, hostFile := range []string{"", "hosts: []\n"} {
		if hostFile != "" {
			write(t, filepath.Join(hostHome, "config.yaml"), hostFile)
		}
		cfg, err := Load("")
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Legacy || cfg.Path != legacy || cfg.Layout != LayoutStandalone || len(cfg.Inventory.Printers()) != 1 {
			t.Errorf("with host config %q: legacy %v path %q printers %d", hostFile, cfg.Legacy, cfg.Path, len(cfg.Inventory.Printers()))
		}
		if got := WritePath(""); got != legacy {
			t.Errorf("WritePath = %q; edits must land where the inventory is read from", got)
		}
	}

	// Once the hlab config has a printer section, it is the inventory, and the
	// old file is no longer consulted.
	write(t, filepath.Join(hostHome, "config.yaml"), unifiedConfig)
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Legacy || len(cfg.Inventory.Printers()) != 2 {
		t.Errorf("legacy %v, %d printer(s); want the unified inventory to win", cfg.Legacy, len(cfg.Inventory.Printers()))
	}
}

// Editing a printer in the shared file must leave everything about the hosts
// exactly as loadable as before, comments included.
func TestEditingAPrinterLeavesTheHostInventoryAlone(t *testing.T) {
	hostHome, _ := isolate(t)
	path := write(t, filepath.Join(hostHome, "config.yaml"), unifiedConfig)

	if err := Edit(path, "printer-a", map[string]string{"migration.wslDistro": "Ubuntu"},
		map[string]string{"firmware": "mainline"}); err != nil {
		t.Fatal(err)
	}
	if err := Edit(path, "printer-c", nil, map[string]string{"address": "192.0.2.62"}); err != nil {
		t.Fatal(err)
	}
	if err := EditComponent(path, "printer-b", "hotend", map[string]string{"type": "hotend", "model": "Example"}, false); err != nil {
		t.Fatal(err)
	}

	printers, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := printers.Inventory.Printer("printer-a")
	b, _ := printers.Inventory.Printer("printer-b")
	if a.Firmware != "mainline" || len(printers.Inventory.Printers()) != 3 || b.Components["hotend"].Model != "Example" {
		t.Errorf("edits did not land: firmware %q, %d printers, components %v", a.Firmware, len(printers.Inventory.Printers()), b.Components)
	}

	hosts, err := hostconfig.Load("")
	if err != nil {
		t.Fatalf("host Load after a printer edit: %v", err)
	}
	nas, ok := hosts.Inventory.Host("nas")
	if !ok || nas.User != "ops" || hosts.Defaults.ConnectTimeout.Duration().Seconds() != 5 {
		t.Errorf("host side changed: nas %+v, timeout %v", nas, hosts.Defaults.ConnectTimeout)
	}

	raw, _ := os.ReadFile(path)
	for _, want := range []string{"# The whole network.", "# the server account, not the printer one", "# the one by the window"} {
		if !bytes.Contains(raw, []byte(want)) {
			t.Errorf("lost comment %q", want)
		}
	}
	// The printer defaults went under the section, not onto the host defaults,
	// which the host loader would reject as an unknown key.
	if strings.Count(string(raw), "wslDistro") != 1 || strings.Contains(string(raw), "\nprinters:") {
		t.Errorf("printer settings escaped the %s section:\n%s", SectionKey, raw)
	}
}

// And the other direction: the host editor rewrites only `hosts:`, so adding a
// server must not disturb a single printer.
func TestEditingAHostLeavesThePrinterInventoryAlone(t *testing.T) {
	hostHome, _ := isolate(t)
	path := write(t, filepath.Join(hostHome, "config.yaml"), unifiedConfig)
	before, err := Load("")
	if err != nil {
		t.Fatal(err)
	}

	hosts, err := hostconfig.Load("")
	if err != nil {
		t.Fatal(err)
	}
	nas, _ := hosts.Inventory.Host("nas")
	added := nas
	added.Name, added.Address, added.Managed = "pi", "192.0.2.6", true
	if err := hostconfig.AddHost(path, added); err != nil {
		t.Fatal(err)
	}

	after, err := Load("")
	if err != nil {
		t.Fatalf("printer Load after a host edit: %v", err)
	}
	if len(after.Inventory.Printers()) != len(before.Inventory.Printers()) || after.Defaults.MoonrakerPort != 7130 {
		t.Errorf("printer side changed: %d printers, port %d", len(after.Inventory.Printers()), after.Defaults.MoonrakerPort)
	}
	if _, err := after.Inventory.Select("@farm"); err != nil {
		t.Errorf("printer group lost: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("# the one by the window")) {
		t.Error("a host edit dropped a comment inside the printer section")
	}
}

// A file created from nothing is created in the unified layout, so the first
// printer never starts a second config.
func TestFirstPrinterCreatesTheUnifiedLayout(t *testing.T) {
	isolate(t)
	path := filepath.Join(t.TempDir(), "new.yaml")
	if err := Edit(path, "printer-a", nil, map[string]string{"address": "192.0.2.60"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Layout != LayoutUnified || len(cfg.Inventory.Printers()) != 1 {
		t.Errorf("layout %v, %d printer(s)", cfg.Layout, len(cfg.Inventory.Printers()))
	}
	if _, err := hostconfig.Load(path); err != nil {
		t.Errorf("the host loader cannot read it: %v", err)
	}
}

func TestMigrateMovesAStandaloneInventoryIntact(t *testing.T) {
	hostHome, printerHome := isolate(t)
	target := write(t, filepath.Join(hostHome, "config.yaml"), "# Servers\nhosts:\n  - name: nas\n    address: 192.0.2.5\n")
	source := write(t, filepath.Join(printerHome, "config.yaml"), standaloneConfig)
	want, err := Load(source)
	if err != nil {
		t.Fatal(err)
	}

	if err := Migrate(source, target); err != nil {
		t.Fatal(err)
	}

	got, err := loadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got.Layout != LayoutUnified || !got.Present {
		t.Fatalf("target layout %v present %v", got.Layout, got.Present)
	}
	wantPrinter, _ := want.Inventory.Printer("printer-a")
	gotPrinter, _ := got.Inventory.Printer("printer-a")
	if gotPrinter.User != "plab" || gotPrinter.Firmware != wantPrinter.Firmware || gotPrinter.Address != wantPrinter.Address {
		t.Errorf("printer changed in transit: %+v", gotPrinter)
	}
	if _, err := got.Inventory.Select("@farm"); err != nil {
		t.Errorf("group lost: %v", err)
	}

	raw, _ := os.ReadFile(target)
	for _, comment := range []string{"# Servers", "# Print farm", "# management account"} {
		if !bytes.Contains(raw, []byte(comment)) {
			t.Errorf("lost comment %q:\n%s", comment, raw)
		}
	}
	if hosts, err := hostconfig.Load(target); err != nil || len(hosts.Inventory.Hosts()) != 1 {
		t.Errorf("host side after migrate: %v", err)
	}
	if untouched, _ := os.ReadFile(source); string(untouched) != standaloneConfig {
		t.Error("Migrate changed the source file")
	}
	if backups, _ := filepath.Glob(target + ".before-printer-migrate-*"); len(backups) != 1 {
		t.Errorf("backups of the hlab config: %v", backups)
	}
}

// Two inventories is a choice for a person. Migrating onto an existing printer
// section must change nothing.
func TestMigrateRefusesToMergeOrOverwrite(t *testing.T) {
	hostHome, printerHome := isolate(t)
	source := write(t, filepath.Join(printerHome, "config.yaml"), standaloneConfig)

	target := write(t, filepath.Join(hostHome, "config.yaml"), unifiedConfig)
	if err := Migrate(source, target); err == nil || !strings.Contains(err.Error(), "already has") {
		t.Errorf("onto an existing section: error = %v", err)
	}
	if raw, _ := os.ReadFile(target); string(raw) != unifiedConfig {
		t.Error("a refused migration changed the target")
	}

	if err := Migrate(source, source); err == nil {
		t.Error("migrating a file onto itself did not error")
	}
	if err := Migrate(target, filepath.Join(t.TempDir(), "elsewhere.yaml")); err == nil {
		t.Error("migrating from a file that is not a standalone inventory did not error")
	}
}

func TestInitAddsASectionWithoutTouchingHosts(t *testing.T) {
	hostHome, _ := isolate(t)
	path := write(t, filepath.Join(hostHome, "config.yaml"), "# Servers\nhosts:\n  - name: nas\n    address: 192.0.2.5\n")
	starter := "defaults:\n  moonrakerPort: 7125\n# Add printers here.\nprinters: []\ngroups: {}\n"

	if err := Init(path, starter); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Present || cfg.Layout != LayoutUnified || len(cfg.Inventory.Printers()) != 0 {
		t.Errorf("present %v layout %v", cfg.Present, cfg.Layout)
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("# Add printers here.")) || !bytes.Contains(raw, []byte("# Servers")) {
		t.Errorf("comments lost:\n%s", raw)
	}
	if hosts, err := hostconfig.Load(""); err != nil || len(hosts.Inventory.Hosts()) != 1 {
		t.Errorf("host side after init: %v", err)
	}

	if err := Init(path, starter); err == nil {
		t.Error("a second init did not refuse")
	}
}
