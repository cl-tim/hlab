package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAppliesPrinterDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`
defaults:
  user: printer
  moonrakerPort: 7126
printers:
  - name: printer-b
    address: mainline-plus4.example
groups:
  lab: [printer-b]
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	printer, ok := cfg.Inventory.Printer("printer-b")
	if !ok {
		t.Fatal("printer missing")
	}
	if printer.User != "printer" || printer.MoonrakerPort != 7126 || printer.Firmware != "auto" {
		t.Fatalf("defaults not applied: %#v", printer)
	}
}

func TestLoadKeepsOnlySSHPasswordEnvironmentName(t *testing.T) {
	cfg := writeConfig(t, `
printers:
  - name: fixturePrinterA
    address: fixturePrinterA.example
    identityFile: C:\keys\fixturePrinterA-ed25519
    sshPasswordEnv: FIXTURE_PRINTER_BOOTSTRAP_PASSWORD
`)
	printer, ok := cfg.Inventory.Printer("fixturePrinterA")
	if !ok {
		t.Fatal("printer missing")
	}
	if printer.SSHPasswordEnv != "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD" {
		t.Fatalf("sshPasswordEnv = %q", printer.SSHPasswordEnv)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("surprise: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown-field error")
	}
}

func writeConfig(t *testing.T, body string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func TestMigrationArtifactPathsAreDerivedFromTheFirmwareRepo(t *testing.T) {
	cfg := writeConfig(t, `
defaults:
  firmwareRepo: C:\src\qidi-plus4-firmware
printers:
  - name: printer-b
    address: plus4.example
`)

	want := filepath.Join(`C:\src\qidi-plus4-firmware`, "artifacts", "mainline-firmware")
	if got := cfg.Defaults.Migration.ArtifactDir; got != want {
		t.Errorf("artifactDir: got %q, want %q", got, want)
	}
	printer, _ := cfg.Inventory.Printer("printer-b")
	if printer.Migration.ArtifactDir != want {
		t.Errorf("printer artifactDir: got %q, want %q", printer.Migration.ArtifactDir, want)
	}
}

func TestBundledMigrationDefaultsNeedNoExternalRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	cfg := writeConfig(t, `
printers:
  - name: my-plus4
    address: plus4.example
    model: plus4
    firmware: stock
`)
	printer, _ := cfg.Inventory.Printer("my-plus4")
	if cfg.Defaults.FirmwareRepo != "" || printer.Migration.ProfileDir != "" {
		t.Fatal("bundled migration unexpectedly requires a checkout")
	}
	if got, want := printer.Migration.ArtifactDir, filepath.Join(home, "artifacts", "mainline-firmware"); got != want {
		t.Fatalf("artifact directory = %q, want %q", got, want)
	}
	if got, want := printer.Migration.ConfigOutput, filepath.Join(home, "artifacts", "generated-configs", "my-plus4"); got != want {
		t.Fatalf("config directory = %q, want %q", got, want)
	}
}

// Two printers must never render into the same directory: the configuration
// carries a machine-specific Beacon serial.
func TestEachPrinterRendersIntoItsOwnConfigDirectory(t *testing.T) {
	cfg := writeConfig(t, `
defaults:
  firmwareRepo: C:\src\qidi-plus4-firmware
printers:
  - name: printer-a
    address: a.example
  - name: printer-b
    address: b.example
`)

	first, _ := cfg.Inventory.Printer("printer-a")
	second, _ := cfg.Inventory.Printer("printer-b")
	if first.Migration.ConfigOutput == second.Migration.ConfigOutput {
		t.Fatalf("both printers share %q", first.Migration.ConfigOutput)
	}
	if filepath.Base(first.Migration.ConfigOutput) != "printer-a" {
		t.Errorf("got %q, want a directory named for the printer",
			first.Migration.ConfigOutput)
	}
}

func TestExplicitMigrationSettingsOverrideDerivedOnes(t *testing.T) {
	cfg := writeConfig(t, `
defaults:
  firmwareRepo: C:\src\qidi-plus4-firmware
  migration:
    artifactDir: D:\images
    sdcard: E:\
printers:
  - name: printer-b
    address: plus4.example
    migration:
      sdcard: F:\
      beaconSerial: /dev/serial/by-id/usb-Beacon_Beacon_RevH_ABC-if00
`)

	printer, _ := cfg.Inventory.Printer("printer-b")
	if printer.Migration.ArtifactDir != `D:\images` {
		t.Errorf("artifactDir: got %q", printer.Migration.ArtifactDir)
	}
	if printer.Migration.SDCard != `F:\` {
		t.Errorf("per-printer sdcard should win: got %q", printer.Migration.SDCard)
	}
	if printer.Migration.BeaconSerial == "" {
		t.Error("beaconSerial was not read from the printer")
	}
}

func TestMigrationProfileDirectorySupportsDefaultAndPerPrinterOverride(t *testing.T) {
	defaultProfile := filepath.Join(t.TempDir(), "profiles", "fixturePrinterB")
	overrideProfile := filepath.Join(t.TempDir(), "profiles", "fixturePrinterA")
	cfg := writeConfig(t, `
defaults:
  migration:
    profileDir: `+defaultProfile+`
printers:
  - name: fixturePrinterB
    address: fixturePrinterB.example
  - name: fixturePrinterA
    address: fixturePrinterA.example
    migration:
      profileDir: `+overrideProfile+`
`)

	if cfg.Defaults.Migration.ProfileDir != defaultProfile {
		t.Fatalf("default profileDir: got %q, want %q",
			cfg.Defaults.Migration.ProfileDir, defaultProfile)
	}
	fixturePrinterB, _ := cfg.Inventory.Printer("fixturePrinterB")
	if fixturePrinterB.Migration.ProfileDir != defaultProfile {
		t.Errorf("fixturePrinterB profileDir: got %q, want default %q",
			fixturePrinterB.Migration.ProfileDir, defaultProfile)
	}
	fixturePrinterA, _ := cfg.Inventory.Printer("fixturePrinterA")
	if fixturePrinterA.Migration.ProfileDir != overrideProfile {
		t.Errorf("fixturePrinterA profileDir: got %q, want override %q",
			fixturePrinterA.Migration.ProfileDir, overrideProfile)
	}
}

func TestSiteWideSDCardReachesEveryPrinter(t *testing.T) {
	cfg := writeConfig(t, `
defaults:
  migration:
    sdcard: E:\
printers:
  - name: printer-b
    address: plus4.example
`)

	printer, _ := cfg.Inventory.Printer("printer-b")
	if printer.Migration.SDCard != `E:\` {
		t.Fatalf("got %q, want the site-wide sdcard", printer.Migration.SDCard)
	}
}

// A shared Beacon serial would render a configuration pointing the wrong probe
// at the wrong machine, so the field does not exist under defaults and strict
// parsing rejects it.
func TestSiteWideBeaconSerialIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`
defaults:
  migration:
    beaconSerial: /dev/serial/by-id/usb-Beacon_Beacon_RevH_ABC-if00
printers:
  - name: printer-b
    address: plus4.example
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("a site-wide beaconSerial must be rejected")
	}
}

func TestMigrationBlockIsOptional(t *testing.T) {
	cfg := writeConfig(t, `
printers:
  - name: printer-b
    address: plus4.example
`)

	printer, _ := cfg.Inventory.Printer("printer-b")
	if printer.Migration.ArtifactDir == "" || printer.Migration.ConfigOutput == "" || printer.Migration.BeaconSerial != "" {
		t.Fatalf("unexpected migration defaults: %#v", printer.Migration)
	}
}

func TestKlippeeWorkspacesAreDerivedPerPrinter(t *testing.T) {
	root := filepath.Join(t.TempDir(), "desktop workspaces")
	cfg := writeConfig(t, `
defaults:
  klippee:
    executable: klippee-custom
    workspaceRoot: `+root+`
printers:
  - name: fixturePrinterA
    address: fixturePrinterA.example
  - name: fixturePrinterB
    address: fixturePrinterB.example
`)

	if cfg.Defaults.Klippee.Executable != "klippee-custom" {
		t.Fatalf("unexpected executable %q", cfg.Defaults.Klippee.Executable)
	}
	first, _ := cfg.Inventory.Printer("fixturePrinterA")
	second, _ := cfg.Inventory.Printer("fixturePrinterB")
	if first.Klippee.Workspace != filepath.Join(root, "fixturePrinterA") {
		t.Fatalf("fixturePrinterA workspace: got %q", first.Klippee.Workspace)
	}
	if second.Klippee.Workspace != filepath.Join(root, "fixturePrinterB") {
		t.Fatalf("fixturePrinterB workspace: got %q", second.Klippee.Workspace)
	}
	if first.Klippee.Workspace == second.Klippee.Workspace {
		t.Fatal("derived Klippee workspaces are not unique")
	}
}

func TestKlippeeExecutableDefaultsToInstalledCommand(t *testing.T) {
	cfg := writeConfig(t, `
printers:
  - name: fixturePrinterA
    address: fixturePrinterA.example
`)
	if cfg.Defaults.Klippee.Executable != "klippee" {
		t.Fatalf("got %q, want klippee", cfg.Defaults.Klippee.Executable)
	}
}

func TestPerPrinterKlippeeWorkspaceOverridesRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "default")
	override := filepath.Join(t.TempDir(), "explicit qidi workspace")
	cfg := writeConfig(t, `
defaults:
  klippee:
    workspaceRoot: `+root+`
printers:
  - name: fixturePrinterA
    address: fixturePrinterA.example
    klippee:
      workspace: `+override+`
`)

	printer, _ := cfg.Inventory.Printer("fixturePrinterA")
	if printer.Klippee.Workspace != override {
		t.Fatalf("got %q, want explicit workspace %q", printer.Klippee.Workspace, override)
	}
}

func TestDuplicateKlippeeWorkspacesAreRejected(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared")
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`
printers:
  - name: fixturePrinterA
    address: fixturePrinterA.example
    klippee:
      workspace: ` + shared + `
  - name: fixturePrinterB
    address: fixturePrinterB.example
    klippee:
      workspace: ` + shared + `
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected shared Klippee workspace to be rejected")
	}
}

func TestKlippeeCredentialsAreRejectedFromInventory(t *testing.T) {
	tests := map[string]string{
		"default token": `
defaults:
  klippee:
    workspaceRoot: C:\workspaces
    token: do-not-store-this-here
printers:
  - name: fixturePrinterA
    address: fixturePrinterA.example
	`,
		"printer repository": `
printers:
  - name: fixturePrinterA
    address: fixturePrinterA.example
    klippee:
      workspace: C:\workspaces\fixturePrinterA
      repositoryId: repo-do-not-duplicate
`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("expected Klippee credential or repository field to be rejected")
			}
		})
	}
}
