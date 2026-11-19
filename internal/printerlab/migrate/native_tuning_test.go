package migrate

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeTuningInstallHasARealSourceFileAndCleansIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell semantics")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash is unavailable")
	}
	b := nativeTestBackend(t)
	root := t.TempDir()
	directory := filepath.Join(root, "tools", "migration")
	os.MkdirAll(directory, 0700)
	source := filepath.Join(directory, "qidi-host-tuning.sh")
	// An installer that consumes its own source exercises the real helper's
	// self-copy requirement without touching system services or hardware.
	raw := []byte("#!/usr/bin/env bash\nset -eu\ntest -f \"$0\"\ncmp \"$0\" \"$EXPECTED_TUNING_SOURCE\"\ntest \"$1\" = install\ntest \"$2\" = --printer-user\ntest \"$3\" = mks\nprintf '%s' \"$0\"\n")
	os.WriteFile(source, raw, 0700)
	b.root = root
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\ntest \"$1\" = -n\nshift\nexec \"$@\"\n"), 0700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("EXPECTED_TUNING_SOURCE", source)
	b.transport = func(command string, input []byte) ([]byte, int, error) {
		cmd := exec.Command("bash", "-c", command)
		cmd.Stdin = bytes.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("shell output: %s", out)
			return out, 1, err
		}
		return out, 0, nil
	}
	if err := b.execute([]string{"tune-host", "--printer", "plus4.invalid", "--user", "plab", "--printer-user", "mks", "--confirm", "TUNE-HOST"}); err != nil {
		t.Fatal(err)
	}
	staged := strings.TrimSpace(b.out.(*bytes.Buffer).String())
	if !strings.HasPrefix(staged, "/tmp/qidi-host-tuning.") {
		t.Fatalf("unexpected source path %s", staged)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("remote helper was not cleaned")
	}
}
