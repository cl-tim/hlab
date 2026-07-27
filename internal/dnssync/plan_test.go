package dnssync

import (
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

func host(name, addr string, managed bool, ifaces ...inventory.Interface) inventory.Host {
	h := inventory.Host{Name: name, Address: addr, Managed: managed}
	if len(ifaces) > 0 {
		h.Network = &inventory.NetworkInfo{Interfaces: ifaces}
	}
	return h
}

// Publishing an unmanaged machine gives authoritative DNS to something nobody
// has verified and that hlab cannot tell has gone away.
func TestOnlyManagedMachinesArePublished(t *testing.T) {
	hosts := []inventory.Host{
		host("apps-1", "198.51.100.4", true),
		host("host-198-51-100-1", "198.51.100.1", false),
	}

	records, _ := DesiredRecords(hosts, "lan")
	if len(records) != 1 || records[0].Name != "apps-1.example" {
		t.Errorf("records = %+v, want just apps-1.example", records)
	}
}

// A name in the zone that hlab does not know is far more likely to be a phone
// than a mistake: DHCP registers every device on the network into it.
func TestExtraRecordsAreReportedNeverRemoved(t *testing.T) {
	want := []Record{{Name: "apps-1.example", Address: "198.51.100.4"}}
	have := map[string]string{
		"apps-1.example": "198.51.100.4",
		"iphone.example": "198.51.100.62",
	}

	plan := Diff("lan", want, have)
	if plan.Actionable() {
		t.Error("an extra record made the plan actionable; it would be deleted")
	}

	var extra int
	for _, c := range plan.Changes {
		if c.Action == Extra && c.Name == "iphone.example" {
			extra++
		}
	}
	if extra != 1 {
		t.Errorf("the extra record was not reported: %+v", plan.Changes)
	}
}

func TestDriftIsDetected(t *testing.T) {
	want := []Record{{Name: "printer-1.example", Address: "198.51.100.58"}}
	have := map[string]string{"printer-1.example": "198.51.100.199"}

	plan := Diff("lan", want, have)
	if !plan.Actionable() {
		t.Fatal("a record disagreeing with the inventory was not actionable")
	}
	c := plan.Changes[0]
	if c.Action != Update || c.Address != "198.51.100.58" || c.Was != "198.51.100.199" {
		t.Errorf("change = %+v, want an update saying what it was", c)
	}
}

// A machine with two interfaces must be reserved against the one actually
// carrying its address. The wrong MAC is worse than no reservation, because it
// looks done.
func TestReservationUsesTheInterfaceHoldingTheAddress(t *testing.T) {
	h := host("apps-1", "198.51.100.4", true,
		inventory.Interface{Name: "wlan0", MAC: "aa:aa:aa:aa:aa:aa", Addresses: []string{"198.51.100.99/24"}},
		inventory.Interface{Name: "eth0", MAC: "bb:bb:bb:bb:bb:bb", Addresses: []string{"198.51.100.4/24"}},
	)

	got := DesiredReservations([]inventory.Host{h})
	if len(got) != 1 || got[0].MAC != "bb:bb:bb:bb:bb:bb" {
		t.Errorf("reservation = %+v, want the eth0 MAC", got)
	}
}

// An address outside the DHCP range can never be offered, so reserving it is
// noise. Statically configured machines live outside the pool by design.
func TestReservationsOutsideTheScopeAreSkipped(t *testing.T) {
	want := []Reservation{
		{Name: "apps-1", MAC: "aa:bb:cc:dd:ee:01", Address: "198.51.100.4"},
		{Name: "cam-1", MAC: "aa:bb:cc:dd:ee:02", Address: "198.51.100.100"},
	}

	got := MissingReservations(want, nil, "198.51.100.50", "198.51.100.250")
	if len(got) != 1 || got[0].Name != "cam-1" {
		t.Errorf("reservations = %+v, want only the one inside the pool", got)
	}
}

// Asserting reservations that already exist makes every run claim work, which
// trains an operator to ignore the command.
func TestReservationsAlreadyHeldAreSkipped(t *testing.T) {
	want := []Reservation{{Name: "cam-1", MAC: "AA:BB:CC:DD:EE:02", Address: "198.51.100.100"}}
	have := map[string]string{"aa:bb:cc:dd:ee:02": "198.51.100.100"}

	if got := MissingReservations(want, have, "198.51.100.50", "198.51.100.250"); len(got) != 0 {
		t.Errorf("reservations = %+v, want none", got)
	}
}

func TestNamesThatCannotBeDNSLabelsAreSkipped(t *testing.T) {
	hosts := []inventory.Host{host("host with spaces", "198.51.100.9", true)}
	records, skipped := DesiredRecords(hosts, "lan")
	if len(records) != 0 || len(skipped) != 1 {
		t.Errorf("records=%+v skipped=%v", records, skipped)
	}
	if !strings.Contains(skipped[0], "DNS label") {
		t.Errorf("skip reason = %q", skipped[0])
	}
}
