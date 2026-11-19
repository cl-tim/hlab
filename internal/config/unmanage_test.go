package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

func TestUnmanageHostKeepsTheDeviceAndDropsTheAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := `# lab
hosts:
  - name: a
    address: 198.51.100.1
    managed: true
    user: hlab
    identityFile: /keys/id_hlab
    hostname: a.lan
    kernel: 6.6.0
  - name: b
    address: 198.51.100.2
    managed: true
groups:
  core: [a, b]
  edge: [b]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	groups, err := UnmanageHost(path, "a")
	if err != nil {
		t.Fatalf("UnmanageHost: %v", err)
	}
	if strings.Join(groups, ",") != "core" {
		t.Errorf("groups = %v, want [core]", groups)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var a inventory.Host
	found := false
	for _, h := range cfg.Inventory.Hosts() {
		if h.Name == "a" {
			a, found = h, true
		}
	}
	if !found {
		t.Fatal("record a is gone; unmanaging must keep it")
	}
	if a.Managed {
		t.Error("a is still managed")
	}
	if a.Hostname != "a.lan" || a.Kernel != "6.6.0" {
		t.Errorf("observations lost: %+v", a)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "/keys/id_hlab") {
		t.Errorf("the revoked credential is still recorded:\n%s", raw)
	}
	if got := strings.Join(cfg.Inventory.Groups()["core"], ","); got != "b" {
		t.Errorf("group core = %q, want b", got)
	}
	if !strings.Contains(string(raw), "# lab") {
		t.Errorf("comments lost:\n%s", raw)
	}

	// The whole point: nothing that connects can select it any more.
	if _, _, err := cfg.Inventory.Select("a"); err == nil {
		t.Error("an unmanaged record is still selectable")
	}
}

func TestUnmanageHostRejectsUnknownNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(path, []byte("hosts:\n  - name: a\n    address: 198.51.100.1\n"), 0o600)
	if _, err := UnmanageHost(path, "nope"); err == nil {
		t.Error("UnmanageHost accepted a machine that is not recorded")
	}
}
