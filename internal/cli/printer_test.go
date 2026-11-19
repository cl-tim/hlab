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

// isolateHomes gives the test empty hlab and printer homes and no overrides.
func isolateHomes(t *testing.T) (hostHome, printerHome string) {
	t.Helper()
	hostHome, printerHome = t.TempDir(), t.TempDir()
	t.Setenv(config.EnvHome, hostHome)
	t.Setenv(config.EnvConfigPath, "")
	t.Setenv(printerconfig.EnvHome, printerHome)
	t.Setenv(printerconfig.EnvConfigPath, "")
	return hostHome, printerHome
}

// The host tree creates its config on first use. A printer command that only
// reads must not: conjuring a config as a side effect of asking about a
// printer would be a file nobody asked for.
func TestReadingPrintersDoesNotCreateTheConfig(t *testing.T) {
	hostHome, _ := isolateHomes(t)

	out, _, err := runRoot(t, "printer", "config", "path")
	if err != nil {
		t.Fatalf("printer config path: %v", err)
	}
	if want := filepath.Join(hostHome, "config.yaml"); strings.TrimSpace(out) != want {
		t.Errorf("printer config path = %q, want the hlab config %q", strings.TrimSpace(out), want)
	}

	if _, _, err := runRoot(t, "printer", "printers"); err == nil ||
		!strings.Contains(err.Error(), "hlab printer config init") {
		t.Errorf("printer printers error = %v, want a pointer at `hlab printer config init`", err)
	}

	entries, err := os.ReadDir(hostHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a read-only printer command wrote %d file(s) into the hlab home", len(entries))
	}
}

// The point of the merge: one config.yaml, and both command trees work from it.
func TestHostsAndPrintersShareOneConfig(t *testing.T) {
	hostHome, _ := isolateHomes(t)
	path := filepath.Join(hostHome, "config.yaml")

	if _, _, err := runRoot(t, "printer", "config", "init"); err != nil {
		t.Fatalf("printer config init: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("init did not create the hlab config: %v", err)
	}
	// It is hlab's scaffold with a printer section added, not a file of
	// printers alone.
	for _, want := range []string{"hosts: []", printerconfig.SectionKey + ":", "moonrakerPort: 7125"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("config lacks %q:\n%s", want, raw)
		}
	}
	if _, _, err := runRoot(t, "printer", "config", "init"); err == nil {
		t.Error("a second init did not refuse")
	}

	body := `defaults:
  user: ops
hosts:
  - name: nas
    address: 192.0.2.5
printerlab:
  printers:
    - name: printer-a
      address: 192.0.2.60
      tags: [plus4]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	printers, _, err := runRoot(t, "printer", "printers", "#plus4")
	if err != nil {
		t.Fatalf("printer printers: %v", err)
	}
	if !strings.Contains(printers, "printer-a") || strings.Contains(printers, "nas") {
		t.Errorf("printer listing:\n%s", printers)
	}

	hosts, _, err := runRoot(t, "hosts")
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	if !strings.Contains(hosts, "nas") || strings.Contains(hosts, "printer-a") {
		t.Errorf("host listing:\n%s", hosts)
	}

	// A printer is not a host. `all` must never sweep one up.
	if _, _, err := runRoot(t, "hosts", "printer-a"); err == nil {
		t.Error("a printer resolved as a host selector")
	}
}

func TestMigrateFoldsTheOldInventoryIntoTheHlabConfig(t *testing.T) {
	hostHome, printerHome := isolateHomes(t)
	target := filepath.Join(hostHome, "config.yaml")
	legacy := filepath.Join(printerHome, "config.yaml")
	old := "# Print farm\nprinters:\n  - name: printer-a\n    address: 192.0.2.60\ngroups:\n  farm: [printer-a]\n"
	if err := os.WriteFile(legacy, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	// Before migrating, the old file is read where it is, and says so.
	out, stderr, err := runRoot(t, "printer", "printers")
	if err != nil || !strings.Contains(out, "printer-a") {
		t.Fatalf("legacy read: %v\n%s", err, out)
	}
	if !strings.Contains(stderr, "hlab printer config migrate") {
		t.Errorf("no pointer at the migration: %q", stderr)
	}
	// Structured output stays clean of the notice on both streams.
	if _, stderr, err := runRoot(t, "printer", "printers", "-o", "json"); err != nil || stderr != "" {
		t.Errorf("json run: err %v, stderr %q", err, stderr)
	}

	if _, _, err := runRoot(t, "printer", "config", "migrate"); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"hosts: []", "# Print farm", printerconfig.SectionKey + ":"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("hlab config lacks %q:\n%s", want, raw)
		}
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the old inventory is still in place: %v", err)
	}
	if kept, err := os.ReadFile(legacy + ".migrated"); err != nil || string(kept) != old {
		t.Errorf("the old inventory was not kept intact: %v", err)
	}

	out, stderr, err = runRoot(t, "printer", "printers", "@farm")
	if err != nil || !strings.Contains(out, "printer-a") {
		t.Fatalf("after migrate: %v\n%s", err, out)
	}
	if stderr != "" {
		t.Errorf("still nagging after the migration: %q", stderr)
	}
	if _, err := config.Load(""); err != nil {
		t.Errorf("the host loader cannot read the migrated config: %v", err)
	}

	// Nothing left to move.
	if _, _, err := runRoot(t, "printer", "config", "migrate"); err == nil {
		t.Error("a second migrate did not error")
	}
}

func TestVersionIsTheSameFromEitherTree(t *testing.T) {
	isolateHomes(t)

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
// The file named here is a standalone inventory: that layout is still read
// when it is the one pointed at.
func TestPrinterErrorsReachMainAsTheSharedType(t *testing.T) {
	isolateHomes(t)
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
