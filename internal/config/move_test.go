package config

import (
	"os"
	"strings"
	"testing"
)

func TestMoveHostAbsorbsTheScansDuplicate(t *testing.T) {
	path := writeConfig(t, `# lab
hosts:
  - name: cluster-node
    address: 192.0.2.50
    managed: true
    user: hlab
  - name: host-192-168-1-61
    address: 192.0.2.61
    managed: false
`)
	absorbed, err := MoveHost(path, "cluster-node", "192.0.2.61")
	if err != nil {
		t.Fatalf("MoveHost: %v", err)
	}
	if absorbed != "host-192-168-1-61" {
		t.Errorf("absorbed = %q", absorbed)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	hosts := cfg.Inventory.Hosts()
	if len(hosts) != 1 || hosts[0].Address != "192.0.2.61" || !hosts[0].Managed || hosts[0].User != "hlab" {
		t.Errorf("hosts = %+v", hosts)
	}
	if raw, _ := os.ReadFile(path); !strings.Contains(string(raw), "# lab") {
		t.Error("comments lost")
	}
}

func TestMoveHostRefusesToCollideWithAManagedMachine(t *testing.T) {
	path := writeConfig(t, `hosts:
  - name: a
    address: 198.51.100.1
    managed: true
  - name: b
    address: 198.51.100.2
    managed: true
`)
	if _, err := MoveHost(path, "a", "198.51.100.2"); err == nil {
		t.Fatal("moved onto a managed machine's address")
	}
	cfg, _ := Load(path)
	if len(cfg.Inventory.Hosts()) != 2 {
		t.Error("a refused move changed the config")
	}
}

// A different port is a different endpoint, not a duplicate.
func TestMoveHostLeavesOtherPortsAlone(t *testing.T) {
	path := writeConfig(t, `hosts:
  - name: a
    address: 198.51.100.1
    managed: true
  - name: jump
    address: 198.51.100.2
    port: 2222
    managed: false
`)
	absorbed, err := MoveHost(path, "a", "198.51.100.2")
	if err != nil || absorbed != "" {
		t.Fatalf("MoveHost = %q, %v; want nothing absorbed", absorbed, err)
	}
	cfg, _ := Load(path)
	if len(cfg.Inventory.Hosts()) != 2 {
		t.Error("the record on another port was dropped")
	}
}
