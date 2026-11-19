package discover

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/homelab-cli/internal/config"
)

var emitTime = time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC)

func TestEmitInventoryRoundTripsThroughTheConfigLoader(t *testing.T) {
	// The whole point of --emit is that its output can be pasted into a config,
	// so it has to survive the loader, including KnownFields(true).
	results := []Result{
		{Address: "192.0.2.10", Port: 22, Hostname: "nas-01.lan", Banner: "SSH-2.0-OpenSSH_9.2p1 Debian-2", OS: "Debian"},
		{Address: "192.0.2.20", Port: 2222, Hostname: "docker-01.lan", Banner: "SSH-2.0-OpenSSH_9.6p1 Ubuntu-3", OS: "Ubuntu"},
		{Address: "192.0.2.30", Port: 22},
	}

	var b strings.Builder
	if err := EmitInventory(&b, results, nil, "hlab discover scan 192.0.2.0/24", emitTime); err != nil {
		t.Fatalf("EmitInventory: %v", err)
	}

	path := filepath.Join(t.TempDir(), "homelab.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("emitted YAML did not load:\n%s\nerror: %v", b.String(), err)
	}

	hosts := cfg.Inventory.Hosts()
	if len(hosts) != 3 {
		t.Fatalf("got %d hosts, want 3", len(hosts))
	}

	nas, ok := cfg.Inventory.Host("nas-01")
	if !ok {
		t.Fatalf("host nas-01 missing; got %v", hosts)
	}
	if nas.Address != "192.0.2.10" {
		t.Errorf("nas-01 address = %q", nas.Address)
	}
	if !nas.HasTag("discovered") || !nas.HasTag("debian") {
		t.Errorf("nas-01 tags = %v, want discovered + debian", nas.Tags)
	}

	docker, ok := cfg.Inventory.Host("docker-01")
	if !ok {
		t.Fatal("host docker-01 missing")
	}
	if docker.Port != 2222 {
		t.Errorf("docker-01 port = %d, want 2222", docker.Port)
	}

	// No reverse DNS name, so the address becomes the name.
	if _, ok := cfg.Inventory.Host("host-192-0-2-30"); !ok {
		t.Errorf("unnamed host missing; got %v", hosts)
	}
}

func TestEmitInventoryAvoidsNameCollisions(t *testing.T) {
	results := []Result{
		{Address: "192.0.2.10", Port: 22, Hostname: "nas-01.lan"},
		{Address: "192.0.2.11", Port: 22, Hostname: "nas-01.other"},
	}
	taken := map[string]struct{}{"nas-01": {}}

	var b strings.Builder
	if err := EmitInventory(&b, results, taken, "hlab discover scan", emitTime); err != nil {
		t.Fatalf("EmitInventory: %v", err)
	}

	got := b.String()
	for _, want := range []string{"name: nas-01-2", "name: nas-01-3"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "name: nas-01\n") {
		t.Errorf("reused a name already in the inventory:\n%s", got)
	}
}

func TestEmitInventoryNeutralisesHostileBanners(t *testing.T) {
	// A banner is whatever the listening service chooses to send. It must not
	// be able to inject YAML keys or break the document.
	results := []Result{{
		Address:  "192.0.2.10",
		Port:     22,
		Hostname: "evil.lan",
		Banner:   "SSH-2.0-x\ntags: [pwned]\nuser: root\n  - name: injected",
	}}

	var b strings.Builder
	if err := EmitInventory(&b, results, nil, "hlab discover scan", emitTime); err != nil {
		t.Fatalf("EmitInventory: %v", err)
	}

	path := filepath.Join(t.TempDir(), "homelab.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("emitted YAML did not load:\n%s\nerror: %v", b.String(), err)
	}

	hosts := cfg.Inventory.Hosts()
	if len(hosts) != 1 {
		t.Fatalf("banner injected extra hosts: got %d, want 1", len(hosts))
	}
	h := hosts[0]
	if h.User != "" {
		t.Errorf("banner injected a user: %q", h.User)
	}
	if h.HasTag("pwned") {
		t.Errorf("banner injected a tag: %v", h.Tags)
	}
}

func TestEmitInventoryEmptyResults(t *testing.T) {
	var b strings.Builder
	if err := EmitInventory(&b, nil, nil, "hlab discover scan", emitTime); err != nil {
		t.Fatalf("EmitInventory: %v", err)
	}
	if b.String() != "" {
		t.Errorf("output = %q, want empty", b.String())
	}
}

func TestSanitiseName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"NAS-01", "nas-01"},
		{"nas_01", "nas-01"},
		{"  spaced  name  ", "spaced-name"},
		{"192.0.2.30", "192-0-2-30"},
		{"---", ""},
		{"", ""},
		// Non-ASCII runes are dropped, each leaving a separator behind.
		{"Ünïcodé", "n-cod"},
		{"a..b", "a-b"},
	}

	for _, tc := range tests {
		if got := sanitiseName(tc.in); got != tc.want {
			t.Errorf("sanitiseName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAnnotateMarksManagedHosts(t *testing.T) {
	results := []Result{
		{Address: "192.0.2.10"},
		{Address: "192.0.2.99"},
	}
	known := KnownHosts{"192.0.2.10": "nas-01"}

	Annotate(results, known)

	if !results[0].Managed || results[0].ManagedAs != "nas-01" {
		t.Errorf("results[0] = %+v, want known as nas-01", results[0])
	}
	if results[1].Managed {
		t.Errorf("results[1] marked known, want new")
	}

	if got := Unmanaged(results); len(got) != 1 || got[0].Address != "192.0.2.99" {
		t.Errorf("NewHosts = %+v, want just 192.0.2.99", got)
	}
}

func TestAnnotateMatchesByHostname(t *testing.T) {
	// The inventory may refer to a machine by a DNS name that did not resolve.
	results := []Result{{Address: "192.0.2.10", Hostname: "nas.lan"}}
	known := KnownHosts{"nas.lan": "nas-01"}

	Annotate(results, known)

	if !results[0].Managed || results[0].ManagedAs != "nas-01" {
		t.Errorf("result = %+v, want known as nas-01", results[0])
	}
}

func TestBuildKnownHostsIndexesLiteralAddresses(t *testing.T) {
	known := BuildKnownHosts(context.Background(), map[string]string{
		"nas-01":    "192.0.2.10",
		"docker-01": "198.51.100.5",
	}, time.Second)

	if known["192.0.2.10"] != "nas-01" {
		t.Errorf("known[192.0.2.10] = %q, want nas-01", known["192.0.2.10"])
	}
	if known["198.51.100.5"] != "docker-01" {
		t.Errorf("known[198.51.100.5] = %q, want docker-01", known["198.51.100.5"])
	}
}

func TestBuildKnownHostsKeepsUnresolvableNames(t *testing.T) {
	// An unresolvable inventory name must still be indexed, so a managed host
	// is never reported as new just because DNS is down.
	known := BuildKnownHosts(context.Background(), map[string]string{
		"nas-01": "nas-01.invalid",
	}, 200*time.Millisecond)

	if known["nas-01.invalid"] != "nas-01" {
		t.Errorf("known = %v, want the literal name indexed", known)
	}
}
