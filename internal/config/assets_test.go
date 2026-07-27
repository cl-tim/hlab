package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

var (
	scanOne = time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	scanTwo = time.Date(2026, 7, 28, 15, 30, 0, 0, time.UTC)
)

func TestMergeObservationsRecordsUnmanagedAssets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	diff, err := MergeObservations(path, []Observation{
		{Address: "192.0.2.26", Port: 22, Hostname: "pi.hole", OS: "Debian", Banner: "SSH-2.0-OpenSSH_10.0p2"},
		{Address: "192.0.2.1", Port: 22, Banner: "SSH-2.0-Server"},
	}, scanOne)
	if err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}
	if diff.Added != 2 {
		t.Errorf("added = %d, want 2", diff.Added)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	hosts := cfg.Inventory.Hosts()
	if len(hosts) != 2 {
		t.Fatalf("hosts = %d, want 2", len(hosts))
	}
	for _, h := range hosts {
		if h.Managed {
			t.Errorf("%s recorded as managed; a scan cannot establish that", h.Name)
		}
		if h.User != "" {
			t.Errorf("%s has a user; an unmanaged record must not look reachable", h.Name)
		}
	}

	// Named from reverse DNS where available, from the address otherwise.
	if _, ok := cfg.Inventory.Host("pi"); !ok {
		t.Errorf("expected a record named from reverse DNS, got %+v", hosts)
	}
	if _, ok := cfg.Inventory.Host("host-192-0-2-1"); !ok {
		t.Errorf("expected a record named from the address, got %+v", hosts)
	}
}

func TestMergeObservationsDetectsChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if _, err := MergeObservations(path, []Observation{
		{Address: "198.51.100.1", Hostname: "box.example", OS: "Debian", Banner: "SSH-2.0-OpenSSH_9.6p1"},
	}, scanOne); err != nil {
		t.Fatalf("first scan: %v", err)
	}

	// The machine's SSH has been upgraded since.
	diff, err := MergeObservations(path, []Observation{
		{Address: "198.51.100.1", Hostname: "box.example", OS: "Debian", Banner: "SSH-2.0-OpenSSH_10.0p2"},
	}, scanTwo)
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}

	if diff.Updated != 1 || diff.Added != 0 {
		t.Errorf("diff = %+v, want 1 updated", diff)
	}
	change := diff.ByAddress["198.51.100.1"]
	if change.Kind != ChangeUpdated {
		t.Errorf("kind = %q, want %q", change.Kind, ChangeUpdated)
	}
	if len(change.Changes) != 1 || !strings.Contains(change.Changes[0], "OpenSSH_9.6p1 -> SSH-2.0-OpenSSH_10.0p2") {
		t.Errorf("changes = %v, want the banner difference described", change.Changes)
	}

	// An identical rescan reports no change.
	diff, err = MergeObservations(path, []Observation{
		{Address: "198.51.100.1", Hostname: "box.example", OS: "Debian", Banner: "SSH-2.0-OpenSSH_10.0p2"},
	}, scanTwo)
	if err != nil {
		t.Fatalf("third scan: %v", err)
	}
	if diff.Unchanged != 1 || diff.Updated != 0 {
		t.Errorf("diff = %+v, want 1 unchanged", diff)
	}
}

func TestMergeObservationsPreservesManagement(t *testing.T) {
	// A scan must never undo enrolment: credentials, tags and the chosen name
	// all survive, and managed stays true.
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := AddHost(path, inventory.Host{
		Name: "nas-01", Address: "198.51.100.1", Managed: true,
		User: "hlab", IdentityFile: "/home/alice/.config/hlab/id_hlab",
		Tags: []string{"storage"},
	}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	if _, err := MergeObservations(path, []Observation{
		{Address: "198.51.100.1", Hostname: "nas.example", OS: "Debian", Banner: "SSH-2.0-OpenSSH_9.2p1"},
	}, scanOne); err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h, ok := cfg.Inventory.Host("nas-01")
	if !ok {
		t.Fatalf("nas-01 missing; got %+v", cfg.Inventory.Hosts())
	}
	if !h.Managed {
		t.Error("a scan turned a managed machine unmanaged")
	}
	if h.User != "hlab" || h.IdentityFile == "" {
		t.Errorf("credentials lost: %+v", h)
	}
	if !h.HasTag("storage") {
		t.Errorf("tags lost: %v", h.Tags)
	}
	// ...while still being enriched by what the scan saw.
	if h.OS != "Debian" || h.Banner == "" {
		t.Errorf("observed fields not recorded: %+v", h)
	}
}

func TestMergeObservationsMatchesRecordsByDNSName(t *testing.T) {
	// A hand-written host recorded as "nas.example" must be recognised when the
	// scan finds its IP, rather than creating a duplicate record.
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := AddHost(path, inventory.Host{
		Name: "nas-01", Address: "nas.example", Managed: true, User: "hlab",
	}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	if _, err := MergeObservations(path, []Observation{
		{Address: "198.51.100.1", OS: "Debian", MatchName: "nas-01"},
	}, scanOne); err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(cfg.Inventory.Hosts()); got != 1 {
		t.Fatalf("hosts = %d, want 1 (no duplicate): %+v", got, cfg.Inventory.Hosts())
	}
	h, _ := cfg.Inventory.Host("nas-01")
	if h.Address != "nas.example" {
		t.Errorf("address = %q, want the configured name kept", h.Address)
	}
	if h.OS != "Debian" {
		t.Error("the record was not enriched by the scan")
	}
}

func TestAddHostPromotesAScannedRecord(t *testing.T) {
	// Enrolment must upgrade the record a scan created, not duplicate it.
	path := filepath.Join(t.TempDir(), "config.yaml")

	if _, err := MergeObservations(path, []Observation{
		{Address: "192.0.2.26", Hostname: "pi.hole", OS: "Debian", Banner: "SSH-2.0-OpenSSH_10.0p2"},
	}, scanOne); err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}

	if err := AddHost(path, inventory.Host{
		Name: "pi-dns", Address: "192.0.2.26", Managed: true,
		User: "hlab", IdentityFile: "/keys/id_hlab",
	}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(cfg.Inventory.Hosts()); got != 1 {
		t.Fatalf("hosts = %d, want 1: enrolment duplicated the record: %+v", got, cfg.Inventory.Hosts())
	}

	h, ok := cfg.Inventory.Host("pi-dns")
	if !ok {
		t.Fatal("the machine was not renamed to the chosen name")
	}
	if !h.Managed || h.User != "hlab" {
		t.Errorf("not promoted: %+v", h)
	}
	// What the scan learned survives the promotion.
	if h.OS != "Debian" || h.Banner == "" {
		t.Errorf("observed fields lost on promotion: %+v", h)
	}
	if !h.FirstSeen.Equal(scanOne) {
		t.Errorf("FirstSeen = %v, want the original sighting %v", h.FirstSeen, scanOne)
	}
}

func TestAddHostRejectsNameHeldByAnotherMachine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.1", Managed: true}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}
	err := AddHost(path, inventory.Host{Name: "a", Address: "198.51.100.2", Managed: true})
	if err == nil {
		t.Fatal("want an error for a name already used at a different address")
	}
	if !strings.Contains(err.Error(), "already in the config") {
		t.Errorf("error = %q", err)
	}
}

func TestMergeObservationsMigratesLegacyDiscoveredSection(t *testing.T) {
	// Configs written by an earlier build kept scan results in their own
	// section. They must keep working, and be folded in on the next write.
	path := filepath.Join(t.TempDir(), "config.yaml")
	legacy := `# my lab
hosts:
  - name: nas-01
    address: 198.51.100.1
    managed: true
    user: hlab
discovered:
  - address: 198.51.100.50
    port: 22
    hostname: printer.example
    os: Debian
    firstSeen: 2026-07-01T00:00:00Z
    lastSeen: 2026-07-01T00:00:00Z
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Reading alone already exposes the old entries as unmanaged assets.
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(cfg.Inventory.Hosts()); got != 2 {
		t.Fatalf("hosts = %d, want 2 after migration: %+v", got, cfg.Inventory.Hosts())
	}

	if _, err := MergeObservations(path, []Observation{{Address: "198.51.100.60"}}, scanTwo); err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "discovered:") {
		t.Errorf("the legacy section survived the rewrite:\n%s", raw)
	}
	if !strings.Contains(string(raw), "# my lab") {
		t.Errorf("comments lost:\n%s", raw)
	}

	cfg, err = Load(path)
	if err != nil {
		t.Fatalf("Load after migration: %v", err)
	}
	if got := len(cfg.Inventory.Hosts()); got != 3 {
		t.Errorf("hosts = %d, want 3", got)
	}
	if h, ok := cfg.Inventory.Host("nas-01"); !ok || !h.Managed {
		t.Error("the managed host did not survive migration")
	}
	if len(cfg.Inventory.Unmanaged()) != 2 {
		t.Errorf("unmanaged = %d, want 2", len(cfg.Inventory.Unmanaged()))
	}
}

// TestHandWrittenHostsDefaultToManaged guards backward compatibility: an entry
// a person wrote without a `managed:` key is one they intend hlab to manage.
// Defaulting the other way would silently make every existing inventory
// unreachable, with no error to explain it.
func TestHandWrittenHostsDefaultToManaged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	handWritten := `defaults:
  user: root
hosts:
  - name: nas-01
    address: 192.0.2.10
  - name: seen-only
    address: 192.0.2.50
    managed: false
`
	if err := os.WriteFile(path, []byte(handWritten), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	nas, ok := cfg.Inventory.Host("nas-01")
	if !ok {
		t.Fatal("nas-01 missing")
	}
	if !nas.Managed {
		t.Error("a host with no `managed:` key was treated as unmanaged")
	}
	if nas.User != "root" {
		t.Errorf("user = %q, want the default applied to a managed host", nas.User)
	}

	// An explicit false is still honoured.
	seen, ok := cfg.Inventory.Host("seen-only")
	if !ok {
		t.Fatal("seen-only missing")
	}
	if seen.Managed {
		t.Error("`managed: false` was ignored")
	}
	if seen.User != "" {
		t.Errorf("user = %q, want no credentials on an unmanaged record", seen.User)
	}

	// Selectors must reflect that split.
	selected, skipped, err := cfg.Inventory.Select("all")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if len(selected) != 1 || selected[0].Name != "nas-01" {
		t.Errorf("selected = %+v, want just nas-01", selected)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped = %+v, want seen-only", skipped)
	}
}

func TestUpdateHostAttributesRecordsContainers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := AddHost(path, inventory.Host{
		Name: "docker", Address: "192.0.2.236", Managed: true, User: "hlab",
	}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	applied, err := UpdateHostAttributes(path, []HostAttributes{{
		Name:   "docker",
		Kernel: "6.8.0-51-generic",
		Containers: []inventory.Container{
			{Runtime: "docker", Name: "plex", Image: "plexinc/pms-docker", Status: "Up 3 hours"},
			{Runtime: "docker", Name: "tim-stremio-1", Image: "tsaridas/stremio-docker:latest"},
		},
	}}, scanOne)
	if err != nil {
		t.Fatalf("UpdateHostAttributes: %v", err)
	}
	if applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h, _ := cfg.Inventory.Host("docker")
	if len(h.Containers) != 2 {
		t.Fatalf("containers = %+v, want 2", h.Containers)
	}
	if h.Containers[0].Name != "plex" || h.Containers[0].Image != "plexinc/pms-docker" {
		t.Errorf("containers[0] = %+v", h.Containers[0])
	}
	// Credentials and management state survive the update.
	if !h.Managed || h.User != "hlab" {
		t.Errorf("host = %+v, want its management state intact", h)
	}
}

func TestUpdateHostAttributesRemovesStoppedContainers(t *testing.T) {
	// A container that has gone away must disappear from the record rather
	// than lingering as a permanent lie.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := AddHost(path, inventory.Host{
		Name: "docker", Address: "198.51.100.1", Managed: true, User: "hlab",
		Containers: []inventory.Container{
			{Runtime: "docker", Name: "old-one", Image: "old:1"},
			{Runtime: "docker", Name: "plex", Image: "plexinc/pms-docker"},
		},
	}); err != nil {
		t.Fatalf("AddHost: %v", err)
	}

	if _, err := UpdateHostAttributes(path, []HostAttributes{{
		Name:       "docker",
		Containers: []inventory.Container{{Runtime: "docker", Name: "plex", Image: "plexinc/pms-docker"}},
	}}, scanTwo); err != nil {
		t.Fatalf("UpdateHostAttributes: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h, _ := cfg.Inventory.Host("docker")
	if len(h.Containers) != 1 || h.Containers[0].Name != "plex" {
		t.Errorf("containers = %+v, want only plex", h.Containers)
	}
}

func TestMergeObservationsOrdersNumericallyByAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if _, err := MergeObservations(path, []Observation{
		{Address: "192.0.2.119"},
		{Address: "192.0.2.1"},
		{Address: "192.0.2.26"},
	}, scanOne); err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	var got []string
	for _, h := range cfg.Inventory.Hosts() {
		got = append(got, h.Address)
	}
	// Inventory.Hosts sorts by name, so check the file itself for order.
	raw, _ := os.ReadFile(path)
	first := strings.Index(string(raw), "192.0.2.1\n")
	last := strings.Index(string(raw), "192.0.2.119")
	if first < 0 || last < 0 || first > last {
		t.Errorf("addresses are not in numeric order:\n%s", raw)
	}
}

func TestMergeObservationsNoResultsIsANoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	diff, err := MergeObservations(path, nil, scanOne)
	if err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}
	if diff.Added != 0 || diff.Updated != 0 {
		t.Errorf("diff = %+v, want no changes", diff)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("an empty scan created a config file")
	}
}

func TestMergeObservationsAvoidsNameCollisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if _, err := MergeObservations(path, []Observation{
		{Address: "198.51.100.1", Hostname: "pi.example"},
		{Address: "198.51.100.2", Hostname: "pi.other"},
	}, scanOne); err != nil {
		t.Fatalf("MergeObservations: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Inventory.Host("pi"); !ok {
		t.Error("pi missing")
	}
	if _, ok := cfg.Inventory.Host("pi-2"); !ok {
		t.Errorf("pi-2 missing; got %+v", cfg.Inventory.Hosts())
	}
}
