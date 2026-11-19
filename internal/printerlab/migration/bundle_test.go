package migration

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBundleExtractsRequiredRuntimeWithoutCompanionCheckouts(t *testing.T) {
	home := t.TempDir()
	root, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"tools/migration/build-firmware.sh",
		"tools/migration/qidi-plus4-remote.sh", "tools/migration/bootstrap-mainline.sh",
		"tools/migration/commissioning-printer.cfg", "tools/migration/upgrade-klippee-agent.py",
		"tools/migration/update-klipper.py",
		"firmware-configs/klipper/plus4-mainboard.config",
		"firmware-configs/katapult/plus4-toolhead.config",
		"mainline-configs/mainline-klipper-beacon/printer.cfg",
		"mainline-configs/mainline-klipper-beacon/profile.json",
		"plr-hardening/install.py", "plr-hardening/manifest.json",
		"LICENSE.firmware", "LICENSE.configs",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			t.Errorf("required asset %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "tools", "migration", "tests")); !os.IsNotExist(err) {
		t.Fatalf("test suite unexpectedly packaged: %v", err)
	}
	if root != Root(home) {
		t.Fatalf("root %s != %s", root, Root(home))
	}
}

func TestBundleRefusesChangedCachedCode(t *testing.T) {
	home := t.TempDir()
	root, err := Ensure(home)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "tools", "migration", "qidi-plus4-remote.sh")
	if err := os.WriteFile(path, []byte("changed code"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Ensure(home); err == nil || !strings.Contains(err.Error(), "was changed") {
		t.Fatalf("changed flash helper was accepted: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "changed code" {
		t.Fatal("validation silently overwrote the changed cache")
	}
}

func TestEmbeddedRemoteHelpersUseUnixLineEndings(t *testing.T) {
	for _, name := range []string{
		"tools/migration/qidi-plus4-remote.sh", "tools/migration/bootstrap-mainline.sh",
		"tools/migration/qidi-host-tuning.sh", "tools/migration/backup-remote.sh",
		"tools/migration/build-firmware.sh",
		"plr-hardening/plr.sh.in",
	} {
		data, err := files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "\r") {
			t.Errorf("%s contains carriage returns that break Bash transport", name)
		}
	}
}

func TestConcurrentBundleExtraction(t *testing.T) {
	home := t.TempDir()
	var work sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		work.Go(func() {
			_, err := Ensure(home)
			errors <- err
		})
	}
	work.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(home, "tooling"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("extraction left incomplete staging: %v, %v", entries, err)
	}
}
