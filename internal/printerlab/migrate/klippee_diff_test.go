package migrate

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

func TestKlippeeDiffSSHCommandIsFixedToCurrentReadOnlyCLIContract(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "ssh-test")
	if err := os.WriteFile(executable, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, args, err := (Runner{sshExecutable: executable}).klippeeDiffSSHCommand(
		inventory.Printer{
			Name: "fixturePrinterB", Address: "fixturePrinterB.example", Port: 22, User: "plab", PrinterUser: "mks",
		},
		10*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sudo", "-n", "-u", "mks", "--", "/usr/bin/env", "-i",
		"HOME=/home/mks", "USER=mks", "LOGNAME=mks",
		"KLIPPEE_STATE_DIR=/home/mks/.klippee", "PATH=/usr/bin:/bin",
		"LANG=C", "LC_ALL=C",
		"/bin/sh", "-c",
	}
	if len(args) <= len(want) || !reflect.DeepEqual(args[len(args)-len(want)-1:len(args)-1], want) {
		t.Fatalf("SSH arguments ended with %#v; want %#v", args, want)
	}
	script := args[len(args)-1]
	for _, argument := range []string{"/home/mks/.local/bin/hlab", "/home/mks/.local/bin/klippee", "check", "--diff"} {
		if !strings.Contains(script, argument) {
			t.Fatalf("fixed SSH check omitted %s", argument)
		}
	}
}
