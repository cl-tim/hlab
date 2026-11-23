package cli

import (
	"context"
	"encoding/json"
	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/inventory"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tlsTestConfig(t *testing.T) string {
	t.Helper()
	home, _ := isolateHomes(t)
	path := filepath.Join(home, "config.yaml")
	raw := `hosts:
  - name: pi
    address: 192.0.2.1
    managed: true
tls:
  authority: pi
  authorityDNS: pi.lan
  trustOnEnroll: true
  services:
    - name: homepage
      target: host:pi
      names: [homepage.lan]
      upstream: http://127.0.0.1:80
`
	if e := os.WriteFile(path, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

func TestTLSDNSCannotRedirectAnEnrolledHostOrPrinter(t *testing.T) {
	path := tlsTestConfig(t)
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteString("printerlab:\n  printers:\n    - name: voron\n      address: 192.0.2.2\n")
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	cfg, e := config.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	a := &app{cfg: cfg}
	c := cfg.TLS.Defaults()
	c.Services[0].Names = []string{"voron.lan"}
	if _, e = a.tlsDNSRecords(context.Background(), c, "lan"); e == nil || !strings.Contains(e.Error(), "refusing to redirect") {
		t.Fatalf("printer redirected: %v", e)
	}
	c.AuthorityDNS = "ca.lan"
	c.Services[0].Names = []string{"pi.lan"}
	// Use the other device's address by adding a second managed host target.
	if e = config.AddHost(path, inventory.Host{Name: "media", Address: "192.0.2.3", Managed: true}); e != nil {
		t.Fatal(e)
	}
	cfg, e = config.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	a.cfg = cfg
	c.Services[0].Target = "host:media"
	if _, e = a.tlsDNSRecords(context.Background(), c, "lan"); e == nil || !strings.Contains(e.Error(), "refusing to redirect") {
		t.Fatalf("host redirected: %v", e)
	}
}

func TestEnrollmentTrustPolicyDoesNotBootstrapANewRoot(t *testing.T) {
	path := tlsTestConfig(t)
	cfg, e := config.Load(path)
	if e != nil {
		t.Fatal(e)
	}
	a := &app{cfg: cfg}
	if e = a.trustEnrolledHost(context.Background(), "pi"); e == nil || !strings.Contains(e.Error(), "pinned CA") {
		t.Fatalf("enrollment bootstrapped CA: %v", e)
	}
	raw, _ := os.ReadFile(path)
	raw = []byte(strings.ReplaceAll(string(raw), "trustOnEnroll: true", "trustOnEnroll: false"))
	if e = os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e = a.trustEnrolledHost(context.Background(), "pi"); e != nil {
		t.Fatalf("disabled policy touched trust: %v", e)
	}
}

func TestTLSPreviewNeedsNeitherNetworkNorPinnedCA(t *testing.T) {
	path := tlsTestConfig(t)
	for _, args := range [][]string{{"tls", "ca"}, {"tls", "apply", "pi"}, {"tls", "trust", "pi"}, {"tls", "trust", "--local"}} {
		full := append([]string{"--config", path, "-o", "json"}, args...)
		stdout, stderr, e := runRoot(t, full...)
		if e != nil {
			t.Fatalf("%v: %v", args, e)
		}
		if !json.Valid([]byte(stdout)) {
			t.Fatalf("not JSON: %s", stdout)
		}
		if stderr != "" {
			t.Fatalf("unexpected stderr %s", stderr)
		}
	}
	if _, e := os.Stat(filepath.Join(filepath.Dir(path), "pki")); !os.IsNotExist(e) {
		t.Fatal("preview created PKI state")
	}
}

func TestTLSWritesRequirePinAndVerifiedSSH(t *testing.T) {
	path := tlsTestConfig(t)
	_, _, e := runRoot(t, "--config", path, "tls", "trust", "pi", "--apply")
	if e == nil || !strings.Contains(e.Error(), "pinned CA") {
		t.Fatalf("expected missing pin: %v", e)
	}
	_, _, e = runRoot(t, "--config", path, "--insecure-host-key", "tls", "ca", "--apply")
	if e == nil || !strings.Contains(e.Error(), "verified SSH") {
		t.Fatalf("insecure bootstrap allowed: %v", e)
	}
}

func TestTLSInvalidConfigDoesNotPreventOrdinaryHostCommands(t *testing.T) {
	path := tlsTestConfig(t)
	raw, _ := os.ReadFile(path)
	raw = []byte(strings.ReplaceAll(string(raw), "pi.lan", "bad name"))
	if e := os.WriteFile(path, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e := runRoot(t, "--config", path, "hosts"); e != nil {
		t.Fatalf("invalid TLS stranded host access: %v", e)
	}
	if _, _, e := runRoot(t, "--config", path, "tls", "ca"); e == nil {
		t.Fatal("invalid TLS accepted")
	}
}
