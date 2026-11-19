package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/config"
)

func standaloneInventory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inventory.yaml")
	raw := `printers:
  - name: my-plus4
    address: plus4.invalid
    model: plus4
    firmware: stock
    migration:
      beaconSerial: /dev/serial/by-id/usb-Beacon_Beacon_RevH_TEST-if00
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStandaloneCLIPathsAndRealCommissioningRender(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	path := standaloneInventory(t)
	stdout, _, err := executeMainlineCLI(t, path, "paths", "my-plus4", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var paths mainlinePaths
	if err := json.Unmarshal([]byte(stdout), &paths); err != nil {
		t.Fatal(err)
	}
	if paths.Backend != "bundled" || !strings.HasPrefix(paths.Tools, home+string(os.PathSeparator)) {
		t.Fatalf("unexpected standalone paths: %#v", paths)
	}
	if _, _, err := executeMainlineCLI(t, path, "render-config", "--for", "my-plus4", "--commissioning"); err != nil {
		t.Fatal(err)
	}
	printer, err := os.ReadFile(filepath.Join(paths.Config, "printer.cfg"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(printer), "max_accel: 1000") || strings.Contains(string(printer), "[include production.cfg]") {
		t.Fatalf("commissioning safeguards missing: %s", printer)
	}
	beacon, err := os.ReadFile(filepath.Join(paths.Config, "beacon.cfg"))
	if err != nil || !strings.Contains(string(beacon), "usb-Beacon_Beacon_RevH_TEST-if00") {
		t.Fatalf("target serial was not rendered: %v", err)
	}
}

func TestArmbianCommandUsesReviewedHelperWithoutDownloadingInTest(t *testing.T) {
	fixture := newRenderConfigFixture(t)
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	output := filepath.Join(t.TempDir(), "images")
	stdout, _, err := executeMainlineCLI(t, fixture.configPath, "armbian-image",
		"--list-only", "--output", output, "--url", "https://example.invalid/armbian.img.xz")
	if err != nil {
		t.Fatal(err)
	}
	want := helperArgsLine(t, fixture.toolPath, "armbian-image", "--output", output,
		"--minimum-kernel", "6.18.54", "--list-only", "--url", "https://example.invalid/armbian.img.xz")
	if !strings.Contains(stdout, want) {
		t.Fatalf("unexpected image helper arguments: %s", stdout)
	}
}
