package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

func TestRenameHostPreservesEverythingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := `# my lab
defaults:
  user: root
hosts:
  - name: pi-2
    address: 192.0.2.119
    managed: true
    user: hlab
    tags: [debian, managed]
    hostname: pi.hole
  - name: other
    address: 192.0.2.26
    managed: true
    user: hlab
groups:
  dns: [pi-2, other]
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := RenameHost(path, "pi-2", "pihole-secondary"); err != nil {
		t.Fatalf("RenameHost: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if _, gone := cfg.Inventory.Host("pi-2"); gone {
		t.Error("the old name is still present")
	}
	h, ok := cfg.Inventory.Host("pihole-secondary")
	if !ok {
		t.Fatalf("renamed host missing; got %+v", cfg.Inventory.Hosts())
	}
	if h.Address != "192.0.2.119" || h.User != "hlab" || !h.Managed {
		t.Errorf("record was altered by the rename: %+v", h)
	}
	if !h.HasTag("debian") {
		t.Errorf("tags lost: %v", h.Tags)
	}

	// A group must not be left pointing at a machine that no longer exists.
	groups := cfg.Inventory.Groups()
	if strings.Join(groups["dns"], ",") != "other,pihole-secondary" {
		t.Errorf("group membership = %v, want the new name", groups["dns"])
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# my lab") {
		t.Errorf("comments lost:\n%s", raw)
	}
}

func TestRenameHostRejectsCollisionsAndUnknowns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.1", Managed: true}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	if err := AddHost(path, inventory.Host{Name: "b", Address: "198.51.100.2", Managed: true}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	if err := RenameHost(path, "a", "b"); err == nil {
		t.Error("renaming onto an existing name succeeded")
	}
	if err := RenameHost(path, "nope", "c"); err == nil {
		t.Error("renaming an unknown machine succeeded")
	}

	// A rejected rename must leave the file alone.
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Inventory.Host("a"); !ok {
		t.Error("host a disappeared after a rejected rename")
	}
}

func TestValidateHostName(t *testing.T) {
	valid := []string{"pi", "pi-hole-2", "nas_01", "web.lan", "a", "A1"}
	for _, n := range valid {
		if err := ValidateHostName(n); err != nil {
			t.Errorf("ValidateHostName(%q) = %v, want nil", n, err)
		}
	}

	// Selector syntax must not be expressible in a name, or a machine becomes
	// unaddressable — or worse, addresses something else.
	invalid := []string{
		"", "all", "-leading", "has space", "@group", "#tag", "a,b", "!neg", "glob*",
		strings.Repeat("x", 64),
	}
	for _, n := range invalid {
		if err := ValidateHostName(n); err == nil {
			t.Errorf("ValidateHostName(%q) = nil, want an error", n)
		}
	}
}

func TestUpdateHostAttributesPrefersTheMachinesOwnHostname(t *testing.T) {
	// Reverse DNS can be wrong or shared — a resolver answering PTR for its
	// whole subnet hands the same name to every address. What the machine
	// calls itself wins.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := AddHost(path, inventory.Host{
		Name: "pi-2", Address: "192.0.2.119", Managed: true, User: "hlab",
		Hostname: "pi.hole", // from a scan's reverse lookup
	}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	if _, err := UpdateHostAttributes(path, []HostAttributes{{
		Name:     "pi-2",
		Hostname: "apps-2",
	}}, scanOne); err != nil {
		t.Fatalf("UpdateHostAttributes: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h, _ := cfg.Inventory.Host("pi-2")
	if h.Hostname != "apps-2" {
		t.Errorf("Hostname = %q, want the collected value to replace the reverse-DNS guess", h.Hostname)
	}
}
