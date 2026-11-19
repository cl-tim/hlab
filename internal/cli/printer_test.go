package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/clikit"
	"github.com/cl-tim/hlab/internal/config"
	printercli "github.com/cl-tim/hlab/internal/printerlab/cli"
	printerconfig "github.com/cl-tim/hlab/internal/printerlab/config"
)

// main unwraps one ExitError type. If either command tree grew its own again,
// its exit codes would silently collapse to 1.
var (
	_ *clikit.ExitError = (*ExitError)(nil)
	_ *clikit.ExitError = (*printercli.ExitError)(nil)
)

func runRoot(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCmd(strings.NewReader(""), &out, &errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// The host tree creates its config on first use. A printer command must not
// trigger that: it reads a different inventory, and conjuring a host config as
// a side effect of asking about a printer would be a file nobody asked for.
func TestPrinterCommandsDoNotCreateTheHostConfig(t *testing.T) {
	hostHome, printerHome := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvHome, hostHome)
	t.Setenv(config.EnvConfigPath, "")
	t.Setenv(printerconfig.EnvHome, printerHome)
	t.Setenv(printerconfig.EnvConfigPath, "")

	out, _, err := runRoot(t, "printer", "config", "path")
	if err != nil {
		t.Fatalf("printer config path: %v", err)
	}
	if want := filepath.Join(printerHome, "config.yaml"); strings.TrimSpace(out) != want {
		t.Errorf("printer config path = %q, want %q", strings.TrimSpace(out), want)
	}

	// A command that needs the printer inventory fails for lack of it, and
	// still leaves the host home untouched.
	if _, _, err := runRoot(t, "printer", "printers"); err == nil ||
		!strings.Contains(err.Error(), "hlab printer config init") {
		t.Errorf("printer printers error = %v, want a pointer at `hlab printer config init`", err)
	}

	entries, err := os.ReadDir(hostHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a printer command wrote %d file(s) into the host home", len(entries))
	}
}

func TestVersionIsTheSameFromEitherTree(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(printerconfig.EnvHome, t.TempDir())

	for _, format := range []string{"table", "json"} {
		host, _, err := runRoot(t, "version", "-o", format)
		if err != nil {
			t.Fatalf("version -o %s: %v", format, err)
		}
		printer, _, err := runRoot(t, "printer", "version", "-o", format)
		if err != nil {
			t.Fatalf("printer version -o %s: %v", format, err)
		}
		if host != printer || host == "" {
			t.Errorf("-o %s: version = %q, printer version = %q", format, host, printer)
		}
	}
}

// A printer selector that matches nothing must surface as an error through
// the combined tree, and an exit-code error must stay recognisable to main.
func TestPrinterErrorsReachMainAsTheSharedType(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv(printerconfig.EnvHome, t.TempDir())
	inventory := filepath.Join(t.TempDir(), "printers.yaml")
	body := "printers:\n  - name: printer-a\n    address: 192.0.2.10\n"
	if err := os.WriteFile(inventory, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, _, err := runRoot(t, "printer", "--config", inventory, "printers", "nope")
	if err == nil || !strings.Contains(err.Error(), `unknown printer "nope"`) {
		t.Errorf("error = %v, want unknown printer", err)
	}

	wrapped := error(&printercli.ExitError{Code: 3})
	var exitErr *ExitError
	if !errors.As(wrapped, &exitErr) || exitErr.Code != 3 {
		t.Error("a printer ExitError is not recognised as the host ExitError")
	}
}
