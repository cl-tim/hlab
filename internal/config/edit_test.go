package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/inventory"
)

func TestAddHostPreservesCommentsAndSettings(t *testing.T) {
	// Enrolment edits a file the operator wrote by hand; their comments and
	// unrelated settings must survive.
	original := `# My homelab inventory.
defaults:
  user: root
  # Connections should not hang forever.
  connectTimeout: 15s

hosts:
  - name: nas-01
    address: 192.0.2.10
    tags: [storage]

groups:
  storage:
    - nas-01
`
	path := filepath.Join(t.TempDir(), "homelab.yaml")
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := AddHost(path, inventory.Host{
		Name:         "pi-dns",
		Address:      "192.0.2.30",
		User:         "hlab",
		IdentityFile: "/home/alice/.config/hlab/id_hlab",
		Tags:         []string{"managed", "debian"},
	})
	if err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(updated)

	for _, want := range []string{
		"# My homelab inventory.",
		"# Connections should not hang forever.",
		"connectTimeout: 15s",
		"nas-01",
		"pi-dns",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lost %q:\n%s", want, got)
		}
	}

	// It must still load, with both hosts and the group intact.
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("updated config does not load: %v\n%s", err, got)
	}
	if len(cfg.Inventory.Hosts()) != 2 {
		t.Errorf("hosts = %d, want 2", len(cfg.Inventory.Hosts()))
	}
	if cfg.Defaults.ConnectTimeout.String() != "15s" {
		t.Errorf("connectTimeout = %s, want 15s", cfg.Defaults.ConnectTimeout)
	}
	if len(cfg.Inventory.Groups()) != 1 {
		t.Errorf("groups = %d, want 1", len(cfg.Inventory.Groups()))
	}

	added, ok := cfg.Inventory.Host("pi-dns")
	if !ok {
		t.Fatal("pi-dns missing")
	}
	if added.User != "hlab" {
		t.Errorf("user = %q, want hlab", added.User)
	}
	if added.IdentityFile == "" {
		t.Error("identityFile was not recorded")
	}
	if !added.HasTag("managed") {
		t.Errorf("tags = %v, want managed", added.Tags)
	}
}

func TestAddHostWritesBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homelab.yaml")
	original := "hosts:\n  - name: a\n    address: 198.51.100.1\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := AddHost(path, inventory.Host{Name: "b", Address: "198.51.100.2"}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want the original contents", backup)
	}
}

func TestAddHostCreatesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "homelab.yaml")

	if err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.1", User: "hlab"}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Inventory.Hosts()) != 1 {
		t.Errorf("hosts = %d, want 1", len(cfg.Inventory.Hosts()))
	}
	// No backup exists for a file that did not exist before.
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("a backup was written for a newly created file")
	}
}

func TestAddHostRejectsDuplicateName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homelab.yaml")
	if err := os.WriteFile(path, []byte("hosts:\n  - name: a\n    address: 198.51.100.1\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.9"})
	if err == nil {
		t.Fatal("want an error for a duplicate host name")
	}
	if !strings.Contains(err.Error(), "already in the config") {
		t.Errorf("error = %q", err)
	}

	// The file must be untouched by a rejected edit.
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "198.51.100.1") || strings.Contains(string(raw), "198.51.100.9") {
		t.Errorf("file was modified by a rejected edit:\n%s", raw)
	}
}

func TestAddHostOmitsDefaultPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homelab.yaml")

	if err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.1", Port: 22}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "port:") {
		t.Errorf("port 22 should be left implicit:\n%s", raw)
	}

	if err := AddHost(path, inventory.Host{Name: "b", Address: "198.51.100.2", Port: 2222}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "port: 2222") {
		t.Errorf("non-default port should be recorded:\n%s", raw)
	}
}

func TestAddHostToEmptyAndCommentOnlyFiles(t *testing.T) {
	for name, content := range map[string]string{
		"empty":        "",
		"comment only": "# nothing here yet\n",
		"no hosts key": "defaults:\n  user: root\n",
		"null hosts":   "hosts:\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "homelab.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}

			if err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.1"}); err != nil {
				t.Fatalf("AddHost: %v", err)
			}

			cfg, err := Load(path)
			if err != nil {
				raw, _ := os.ReadFile(path)
				t.Fatalf("Load: %v\n%s", err, raw)
			}
			if _, ok := cfg.Inventory.Host("a"); !ok {
				t.Error("host a missing")
			}
		})
	}
}

func TestAddHostRequiresName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homelab.yaml")
	if err := AddHost(path, inventory.Host{Address: "198.51.100.1"}); err == nil {
		t.Fatal("want an error for a host with no name")
	}
}

func TestAddHostRejectsUnparseableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "homelab.yaml")
	if err := os.WriteFile(path, []byte("hosts: [oh no: ["), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.1"}); err == nil {
		t.Fatal("want an error for a config that cannot be parsed")
	}
}
