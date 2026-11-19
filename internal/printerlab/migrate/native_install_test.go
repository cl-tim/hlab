package migrate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func commissioningTestArgs(t *testing.T, b *nativeBackend) []string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "config")
	serial := "/dev/serial/by-id/usb-Beacon_TEST-if00"
	if err := b.execute([]string{"render-config", "--commissioning", "--output", output, "--beacon-serial", serial}); err != nil {
		t.Fatal(err)
	}
	return []string{"install-config", "--printer", "printer.invalid", "--output", output, "--beacon-serial", serial, "--confirm", "INSTALL-CONFIG"}
}

func TestGuidedInstallRejectsCustomEntryPointBeforeSSH(t *testing.T) {
	b := nativeTestBackend(t)
	args := commissioningTestArgs(t, b)
	path := filepath.Join(args[4], "printer.cfg")
	if err := os.WriteFile(path, []byte("# calibrated configuration\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b.transport = func(string, []byte) ([]byte, int, error) {
		t.Fatal("contacted printer for invalid local configuration")
		return nil, 1, nil
	}
	if err := b.execute(args); err == nil || !strings.Contains(err.Error(), "unchanged commissioning") {
		t.Fatalf("error: %v", err)
	}
}

func TestGuidedInstallTransactionPreservesCalibratedConfigAndVerifiesStop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX transaction; also run with cross-compiled Linux test binary in WSL")
	}
	for _, scenario := range []string{"success", "calibrated", "stop-failed"} {
		t.Run(scenario, func(t *testing.T) {
			b := nativeTestBackend(t)
			args := commissioningTestArgs(t, b)
			remote := t.TempDir()
			data := filepath.Join(remote, "printer_data")
			cfg := filepath.Join(data, "config")
			os.MkdirAll(cfg, 0700)
			os.WriteFile(filepath.Join(data, "mainline-bootstrap.txt"), []byte("receipt"), 0600)
			smoke, _ := b.helper("smoke-test-printer.cfg")
			original := smoke
			if scenario == "calibrated" {
				original = []byte("# SAVE_CONFIG calibration\n")
			}
			os.WriteFile(filepath.Join(cfg, "printer.cfg"), original, 0600)
			service := filepath.Join(remote, "service")
			script := "#!/bin/sh\ncase \"$1\" in\nstop) exit 0;;\nis-active) echo inactive; exit 3;;\nrestart) exit 0;;\nesac\nexit 1\n"
			if scenario == "stop-failed" {
				script = strings.ReplaceAll(script, "echo inactive; exit 3", "echo active; exit 0")
			}
			os.WriteFile(service, []byte(script), 0700)
			b.transport = func(command string, input []byte) ([]byte, int, error) {
				if strings.HasPrefix(command, "bash -s -- probe") {
					return []byte(`{"printer_cfg":{"present":true,"size":100,"sha256":"real-config","mcu_sections":["mcu","mcu U_1"]},"bootstrap_receipt":{"present":true},"moonraker":{"reachable":true,"klippy_state":"ready","mcu_objects":["mcu","mcu U_1"]}}`), 0, nil
				}
				command = strings.ReplaceAll(command, "/home/mks/printer_data", data)
				command = strings.ReplaceAll(command, "sudo -n systemctl", quote(service))
				cmd := exec.CommandContext(context.Background(), "bash", "-c", command)
				cmd.Stdin = bytes.NewReader(input)
				raw, err := cmd.CombinedOutput()
				if err != nil {
					return raw, 1, fmt.Errorf("fixture transaction: %w: %s", err, raw)
				}
				return raw, 0, nil
			}
			err := b.execute(args)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("error: %v", err)
			}
			actual, _ := os.ReadFile(filepath.Join(cfg, "printer.cfg"))
			backups, _ := filepath.Glob(filepath.Join(data, "config-before-firmware-*"))
			if scenario == "success" {
				if len(backups) != 1 {
					t.Fatal("missing recovery snapshot")
				}
				old, _ := os.ReadFile(filepath.Join(backups[0], "printer.cfg"))
				if !bytes.Equal(old, original) {
					t.Fatal("recovery snapshot differs")
				}
				entry, _ := b.helper("commissioning-printer.cfg")
				if !bytes.Equal(actual, entry) {
					t.Fatal("commissioning entry point not installed")
				}
				if _, err := os.Stat(filepath.Join(cfg, "RENDER-METADATA.json")); !os.IsNotExist(err) {
					t.Fatal("render metadata copied to printer")
				}
			} else if !bytes.Equal(actual, original) || len(backups) != 0 {
				t.Fatal("refused installation changed remote config")
			}
			staging, _ := filepath.Glob(filepath.Join(data, ".commissioning.*"))
			if len(staging) != 0 {
				t.Fatal("staging not cleaned up")
			}
		})
	}
}
