package cli

import (
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// testApp builds an app around a fixed inventory, with output discarded.
func testApp(t *testing.T, hosts []inventory.Host) *app {
	t.Helper()

	inv, err := inventory.New(hosts, nil)
	if err != nil {
		t.Fatalf("inventory.New: %v", err)
	}
	return &app{
		cfg:    &config.Config{Inventory: inv},
		in:     strings.NewReader(""),
		out:    &strings.Builder{},
		errOut: &strings.Builder{},
	}
}

// TestManagedTargetsDefaultsToTheManagedFleet covers the promise that fact
// collection never reaches for a machine hlab has no credentials for: with no
// selector it covers exactly the managed set, and nothing else.
func TestManagedTargetsDefaultsToTheManagedFleet(t *testing.T) {
	a := testApp(t, []inventory.Host{
		{Name: "managed-a", Address: "198.51.100.1", Managed: true, User: "hlab", Tags: []string{"debian"}},
		{Name: "managed-b", Address: "198.51.100.2", Managed: true, User: "hlab"},
		{Name: "printer", Address: "198.51.100.50", Managed: false, Tags: []string{"debian"}},
		{Name: "router", Address: "198.51.100.1", Managed: false},
	})

	targets, err := a.managedTargets(nil)
	if err != nil {
		t.Fatalf("managedTargets: %v", err)
	}

	var got []string
	for _, tgt := range targets {
		got = append(got, tgt.Name)
	}
	if strings.Join(got, ",") != "managed-a,managed-b" {
		t.Errorf("targets = %v, want only the managed machines", got)
	}
}

func TestManagedTargetsHonoursASelector(t *testing.T) {
	a := testApp(t, []inventory.Host{
		{Name: "managed-a", Address: "198.51.100.1", Managed: true, User: "hlab", Tags: []string{"debian"}},
		{Name: "managed-b", Address: "198.51.100.2", Managed: true, User: "hlab", Tags: []string{"ubuntu"}},
		{Name: "printer", Address: "198.51.100.50", Managed: false, Tags: []string{"debian"}},
	})

	targets, err := a.managedTargets([]string{"#debian"})
	if err != nil {
		t.Fatalf("managedTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "managed-a" {
		t.Errorf("targets = %+v, want just managed-a; the unmanaged printer shares the tag", targets)
	}
}

func TestManagedTargetsWithNothingManaged(t *testing.T) {
	a := testApp(t, []inventory.Host{
		{Name: "printer", Address: "198.51.100.50", Managed: false},
	})

	_, err := a.managedTargets(nil)
	if err == nil {
		t.Fatal("want an error when nothing is managed")
	}
	if !strings.Contains(err.Error(), "hlab enroll") {
		t.Errorf("error = %q, want it to point at the next step", err)
	}
}

func TestManagedTargetsCarryCredentials(t *testing.T) {
	a := testApp(t, []inventory.Host{
		{
			Name: "a", Address: "198.51.100.1", Port: 2222, Managed: true,
			User: "hlab", IdentityFile: "/keys/id_hlab",
		},
	})

	targets, err := a.managedTargets(nil)
	if err != nil {
		t.Fatalf("managedTargets: %v", err)
	}
	got := targets[0]
	if got.User != "hlab" || got.IdentityFile != "/keys/id_hlab" || got.Port != 2222 {
		t.Errorf("target = %+v, want the recorded credentials and port", got)
	}
}
