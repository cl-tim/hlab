package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
)

func TestKlipperServiceRestartSSHCommandIsFixed(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "ssh-test")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := Runner{sshExecutable: executable}
	_, args, err := runner.fixedSSHCommand(inventory.Printer{
		Name: "fixturePrinterA", Address: "fixturePrinterA.example", Port: 22, User: "plab",
	}, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	args = append(args, klipperServiceRestartCommand)
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"BatchMode=yes", "StrictHostKeyChecking=yes", "plab@fixturePrinterA.example",
		"sudo -n systemctl restart", "klipper.service", "previous_pid", "current_pid",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("SSH arguments omitted %q: %q", required, joined)
		}
	}
}

func TestParseKlipperServiceRestartRequiresChangedPIDAndActiveService(t *testing.T) {
	valid := []byte(`{"schema":"printerlab-klipper-service-restart/v1","service":"klipper.service","state":"active","previous_pid":10,"current_pid":11}`)
	result, err := ParseKlipperServiceRestart(valid)
	if err != nil {
		t.Fatal(err)
	}
	if result.PreviousPID != 10 || result.CurrentPID != 11 {
		t.Fatalf("result = %#v", result)
	}
	for name, raw := range map[string]string{
		"same pid": `{"schema":"printerlab-klipper-service-restart/v1","service":"klipper.service","state":"active","previous_pid":10,"current_pid":10}`,
		"inactive": `{"schema":"printerlab-klipper-service-restart/v1","service":"klipper.service","state":"failed","previous_pid":10,"current_pid":11}`,
		"extra":    string(valid) + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseKlipperServiceRestart([]byte(raw)); err == nil {
				t.Fatal("expected invalid proof rejection")
			}
		})
	}
}
