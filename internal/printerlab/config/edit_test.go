package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestEditPreservesOtherPrintersGroupsCommentsAndExactBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("# Fleet notes\ndefaults:\n  user: mks # SSH owner\nprinters:\n  - name: first\n    address: first.invalid\n    model: plus4\n    tags: [stock]\n    description: keep this\n  - name: other\n    address: other.invalid\n    model: plus4\ngroups:\n  workshop: [first, other]\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Edit(path, "first", map[string]string{"migration.wslDistro": "Ubuntu"}, map[string]string{"migration.beaconSerial": "/dev/serial/by-id/usb-Beacon_TEST", "identityFile": "~/.ssh/first"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := cfg.Inventory.Select("first")
	other, _ := cfg.Inventory.Select("other")
	group, err := cfg.Inventory.Select("@workshop")
	if err != nil || len(group) != 2 || len(other) != 1 || first[0].Description != "keep this" || len(first[0].Tags) != 1 {
		t.Fatal("lost unrelated inventory settings")
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("# Fleet notes")) || !bytes.Contains(raw, []byte("# SSH owner")) {
		t.Fatal("lost comments")
	}
	backups, _ := filepath.Glob(path + ".before-firmware-*")
	if len(backups) != 1 {
		t.Fatalf("backups: %v", backups)
	}
	backup, _ := os.ReadFile(backups[0])
	if !bytes.Equal(backup, original) {
		t.Fatal("backup differs from original bytes")
	}
	if err := Edit(path, "first", nil, map[string]string{"identityFile": "~/.ssh/first"}); err != nil {
		t.Fatal(err)
	}
	backups, _ = filepath.Glob(path + ".before-firmware-*")
	if len(backups) != 1 {
		t.Fatal("identical rerun rewrote inventory")
	}
}

func TestEditInvalidChangeDoesNotReplaceInventory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := Edit(path, "new", nil, map[string]string{"address": "printer.invalid", "model": "plus4"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := Edit(path, "new", nil, map[string]string{"unknownField": "value"}); err == nil {
		t.Fatal("accepted invalid field")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid change replaced inventory")
	}
	backups, _ := filepath.Glob(path + ".before-firmware-*")
	if len(backups) != 0 {
		t.Fatal("invalid change published backup")
	}
}
