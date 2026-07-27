package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/cl-tim/homelab-cli/internal/discover"
	"github.com/cl-tim/homelab-cli/internal/inventory"
)

func TestCandidateFromStoredHost(t *testing.T) {
	// A machine recorded by an earlier scan carries everything needed to offer
	// it again without rescanning.
	h := inventory.Host{
		Name:        "pi",
		Address:     "192.0.2.26",
		Port:        22,
		Hostname:    "pi.hole",
		OS:          "Debian",
		Banner:      "SSH-2.0-OpenSSH_10.0p2 Debian-7+deb13u4",
		Description: "Pi-hole DNS",
		LastSeen:    time.Now().Add(-2 * time.Hour),
	}

	c := candidateFromHost(h)

	if !c.Stored {
		t.Error("Stored = false, want true for a config record")
	}
	if c.Name != "pi" {
		t.Errorf("Name = %q, want the recorded name", c.Name)
	}
	if c.Description != "Pi-hole DNS" {
		t.Errorf("Description = %q, want it carried through for editing", c.Description)
	}
	// Software is derived from the stored banner, so the header reads the same
	// as it would after a live scan.
	if c.Software != "OpenSSH_10.0p2" {
		t.Errorf("Software = %q, want it parsed from the banner", c.Software)
	}

	desc := c.describe()
	for _, want := range []string{"192.0.2.26", "pi", "Debian", "OpenSSH_10.0p2", "2h ago"} {
		if !strings.Contains(desc, want) {
			t.Errorf("describe() = %q, missing %q", desc, want)
		}
	}
}

func TestCandidateFromScanResult(t *testing.T) {
	c := candidateFromScan(discover.Result{
		Address:  "198.51.100.5",
		Port:     2222,
		Hostname: "box.example",
		OS:       "Ubuntu",
		Software: "OpenSSH_9.6p1",
	})

	if c.Stored {
		t.Error("Stored = true, want false for a live scan result")
	}
	if !strings.Contains(c.describe(), "198.51.100.5:2222") {
		t.Errorf("describe() = %q, want the non-default port shown", c.describe())
	}
}

func TestCandidateSuggestedName(t *testing.T) {
	inv, err := inventory.New([]inventory.Host{
		{Name: "pi", Address: "198.51.100.1", Managed: true},
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// A stored record keeps the name it already has.
	stored := candidate{Name: "existing-name", Address: "198.51.100.9"}
	if got := stored.suggestedName(inv); got != "existing-name" {
		t.Errorf("suggestedName = %q, want the record's own name", got)
	}

	// A fresh sighting is named from reverse DNS, avoiding a collision.
	fresh := candidate{Address: "198.51.100.2", Hostname: "pi.example"}
	if got := fresh.suggestedName(inv); got != "pi-2" {
		t.Errorf("suggestedName = %q, want pi-2 to avoid the existing pi", got)
	}

	// With no name to work from, the address becomes one.
	anon := candidate{Address: "198.51.100.3"}
	if got := anon.suggestedName(inv); got != "host-198-51-100-3" {
		t.Errorf("suggestedName = %q, want a name derived from the address", got)
	}
}

func TestCandidateDialPort(t *testing.T) {
	if got := (candidate{}).dialPort(); got != 22 {
		t.Errorf("dialPort() = %d, want 22 by default", got)
	}
	if got := (candidate{Port: 2222}).dialPort(); got != 2222 {
		t.Errorf("dialPort() = %d, want 2222", got)
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Now()
	tests := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, ""},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-50 * time.Hour), "2d ago"},
	}

	for _, tc := range tests {
		if got := relativeTime(tc.at); got != tc.want {
			t.Errorf("relativeTime(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}
