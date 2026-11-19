package migrate

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

func TestKlippeeDiagnosticsSSHCommandIsFixedAndTargetsPrinterUser(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	identity := t.TempDir() + "/identity"
	if err := os.WriteFile(identity, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	printer := inventory.Printer{
		Address: "fixturePrinterA.example", User: "plab", PrinterUser: "mks", Port: 22,
		IdentityFile: identity,
	}
	_, args, err := (Runner{sshExecutable: executable}).klippeeDiagnosticsSSHCommand(
		printer, 12*time.Second, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	for _, expected := range []string{
		"ConnectTimeout=12", "BatchMode=yes", "StrictHostKeyChecking=yes",
		"IdentitiesOnly=yes",
		"plab@fixturePrinterA.example", "sudo\x00-n\x00-H\x00-u\x00mks\x00--",
		"/home/mks/.local/bin/klippee\x00upload-diagnostics",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("args %q missing %q", joined, expected)
		}
	}
	for _, forbidden := range []string{"makerbase", "cat", "klippy.log"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("args %q contain forbidden value %q", joined, forbidden)
		}
	}
}

func TestKlippeeDiagnosticsSSHCommandRejectsUnsafeInventory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := inventory.Printer{Address: "fixturePrinterA.example", User: "mks", Port: 22}
	for name, mutate := range map[string]func(*inventory.Printer){
		"option host":   func(printer *inventory.Printer) { printer.Address = "-oProxyCommand=bad" },
		"shell account": func(printer *inventory.Printer) { printer.User = "mks;id" },
		"unsafe printer account": func(printer *inventory.Printer) {
			printer.PrinterUser = "../root"
		},
		"invalid port": func(printer *inventory.Printer) { printer.Port = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			printer := base
			mutate(&printer)
			if _, _, err := (Runner{sshExecutable: executable}).klippeeDiagnosticsSSHCommand(
				printer, 10*time.Second, false,
			); err == nil {
				t.Fatal("expected unsafe inventory rejection")
			}
		})
	}
}

func TestKlippeePreviousDiagnosticsSSHCommandIsFixed(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	printer := inventory.Printer{
		Address: "fixturePrinterA.example", User: "mks", Port: 22,
	}
	_, args, err := (Runner{sshExecutable: executable}).klippeeDiagnosticsSSHCommand(
		printer, 10*time.Second, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\x00")
	for _, expected := range []string{
		"mks@fixturePrinterA.example",
		"python3 -c",
		"/home/mks/printer_data/logs/klippy.log.*",
		"/home/mks/.local/bin/klippee",
		"upload-diagnostics",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("args %q missing %q", joined, expected)
		}
	}
	for _, forbidden := range []string{"makerbase", "cat", "tail", "scp"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("args %q contain forbidden value %q", joined, forbidden)
		}
	}
}
