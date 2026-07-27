package inventory

import (
	"strings"
	"testing"
)

func testInventory(t *testing.T) *Inventory {
	t.Helper()

	hosts := []Host{
		{Managed: true, Name: "nas-01", Address: "198.51.100.10", Port: 22, User: "root", Tags: []string{"storage", "debian"}},
		{Managed: true, Name: "docker-01", Address: "198.51.100.20", Port: 22, User: "tim", Tags: []string{"docker", "debian"}},
		{Managed: true, Name: "docker-02", Address: "198.51.100.21", Port: 22, User: "tim", Tags: []string{"docker", "debian", "retired"}},
		{Managed: true, Name: "pi-dns", Address: "198.51.100.30", Port: 22, User: "pi", Tags: []string{"dns"}},
	}
	groups := map[string][]string{
		"core": {"nas-01", "docker-01", "docker-02"},
		"edge": {"pi-dns"},
	}

	inv, err := New(hosts, groups)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return inv
}

func names(hosts []Host) string {
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, h.Name)
	}
	return strings.Join(out, ",")
}

func TestSelect(t *testing.T) {
	inv := testInventory(t)

	tests := []struct {
		name string
		expr string
		want string
	}{
		{"all", "all", "docker-01,docker-02,nas-01,pi-dns"},
		{"exact name", "nas-01", "nas-01"},
		{"glob", "docker-*", "docker-01,docker-02"},
		{"group", "@core", "docker-01,docker-02,nas-01"},
		{"tag", "#dns", "pi-dns"},
		{"union dedupes", "@core,nas-01,#storage", "docker-01,docker-02,nas-01"},
		{"exclusion by tag", "@core,!#retired", "docker-01,nas-01"},
		{"exclusion by name", "all,!pi-dns", "docker-01,docker-02,nas-01"},
		{"exclusion applies regardless of order", "!#retired,@core", "docker-01,nas-01"},
		{"exclusion by glob", "all,!docker-*", "nas-01,pi-dns"},
		{"whitespace tolerated", " @core , !#retired ", "docker-01,nas-01"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := inv.Select(tc.expr)
			if err != nil {
				t.Fatalf("Select(%q): unexpected error: %v", tc.expr, err)
			}
			if names(got) != tc.want {
				t.Errorf("Select(%q) = %q, want %q", tc.expr, names(got), tc.want)
			}
		})
	}
}

func TestSelectErrors(t *testing.T) {
	inv := testInventory(t)

	tests := []struct {
		name string
		expr string
	}{
		{"empty", ""},
		{"unknown host", "nope-01"},
		{"unknown group", "@nope"},
		{"unknown tag", "#nope"},
		{"glob matching nothing", "zz-*"},
		{"everything excluded", "all,!all"},
		{"dangling negation", "!"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, _, err := inv.Select(tc.expr); err == nil {
				t.Errorf("Select(%q) = %q, want error", tc.expr, names(got))
			}
		})
	}
}

// TestSelectNeverTargetsUnmanagedMachines is the guard on the whole asset
// model: scan results and managed machines share one list, so the selector is
// the only thing standing between `hlab run all` and every device on the
// network. Unmanaged machines have no credentials; targeting them could only
// fail, and doing so silently would be worse.
func TestSelectNeverTargetsUnmanagedMachines(t *testing.T) {
	inv, err := New([]Host{
		{Managed: true, Name: "nas-01", Address: "198.51.100.10", Tags: []string{"debian"}},
		{Managed: false, Name: "printer", Address: "198.51.100.50", Tags: []string{"debian"}},
		{Managed: false, Name: "router", Address: "198.51.100.1"},
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	selected, skipped, err := inv.Select("all")
	if err != nil {
		t.Fatalf("Select(all): %v", err)
	}
	if names(selected) != "nas-01" {
		t.Errorf("Select(all) = %q, want only the managed machine", names(selected))
	}
	if names(skipped) != "printer,router" {
		t.Errorf("skipped = %q, want the unmanaged machines reported", names(skipped))
	}

	// A tag shared by both must still only resolve to the managed one.
	selected, skipped, err = inv.Select("#debian")
	if err != nil {
		t.Fatalf("Select(#debian): %v", err)
	}
	if names(selected) != "nas-01" || names(skipped) != "printer" {
		t.Errorf("Select(#debian) = %q, skipped %q", names(selected), names(skipped))
	}

	// Naming an unmanaged machine directly must fail rather than quietly
	// resolving to nothing.
	if _, _, err := inv.Select("printer"); err == nil {
		t.Error("Select(printer) succeeded for an unmanaged machine")
	} else if !strings.Contains(err.Error(), "none of them managed") {
		t.Errorf("error = %q, want it to explain why", err)
	}

	// SelectAll still sees everything, which is what listing needs.
	all, err := inv.SelectAll("all")
	if err != nil {
		t.Fatalf("SelectAll: %v", err)
	}
	if names(all) != "nas-01,printer,router" {
		t.Errorf("SelectAll(all) = %q, want every machine", names(all))
	}
}

func TestManagedAndUnmanagedViews(t *testing.T) {
	inv, err := New([]Host{
		{Managed: true, Name: "a", Address: "198.51.100.1"},
		{Managed: false, Name: "b", Address: "198.51.100.2"},
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if names(inv.Managed()) != "a" {
		t.Errorf("Managed() = %q, want a", names(inv.Managed()))
	}
	if names(inv.Unmanaged()) != "b" {
		t.Errorf("Unmanaged() = %q, want b", names(inv.Unmanaged()))
	}
	if names(inv.Hosts()) != "a,b" {
		t.Errorf("Hosts() = %q, want both", names(inv.Hosts()))
	}
}

func TestByAddress(t *testing.T) {
	inv, err := New([]Host{
		{Name: "a", Address: "198.51.100.1", Port: 22},
		{Name: "b", Address: "198.51.100.1", Port: 2222},
		{Name: "c", Address: "198.51.100.2"},
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if h, ok := inv.ByAddress("198.51.100.1", 2222); !ok || h.Name != "b" {
		t.Errorf("ByAddress(198.51.100.1, 2222) = %+v, %v; want b", h, ok)
	}
	// Port 0 means the default, which must match a record written without one.
	if h, ok := inv.ByAddress("198.51.100.2", 0); !ok || h.Name != "c" {
		t.Errorf("ByAddress(198.51.100.2, 0) = %+v, %v; want c", h, ok)
	}
	if _, ok := inv.ByAddress("198.51.100.9", 22); ok {
		t.Error("ByAddress found an address that is not recorded")
	}
}

func TestNewRejectsBadInventory(t *testing.T) {
	tests := []struct {
		name   string
		hosts  []Host
		groups map[string][]string
	}{
		{
			name:  "duplicate host name",
			hosts: []Host{{Name: "a", Address: "1.1.1.1"}, {Name: "a", Address: "2.2.2.2"}},
		},
		{
			name:  "missing name",
			hosts: []Host{{Address: "1.1.1.1"}},
		},
		{
			name:  "missing address",
			hosts: []Host{{Name: "a"}},
		},
		{
			name:   "group refers to unknown host",
			hosts:  []Host{{Name: "a", Address: "1.1.1.1"}},
			groups: map[string][]string{"g": {"b"}},
		},
		{
			name:   "reserved group name",
			hosts:  []Host{{Name: "a", Address: "1.1.1.1"}},
			groups: map[string][]string{"all": {"a"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.hosts, tc.groups); err == nil {
				t.Error("New: want error, got nil")
			}
		})
	}
}

func TestHasTagIsCaseInsensitive(t *testing.T) {
	h := Host{Name: "a", Tags: []string{"Docker"}}
	if !h.HasTag("docker") {
		t.Error("HasTag(docker) = false, want true")
	}
	if h.HasTag("dns") {
		t.Error("HasTag(dns) = true, want false")
	}
}
