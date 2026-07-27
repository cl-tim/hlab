package cli

import (
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/output"
)

func TestManagedHostsDefaultsToTheManagedFleet(t *testing.T) {
	a := testApp(t, []inventory.Host{
		{Name: "a", Address: "198.51.100.1", Managed: true, User: "hlab"},
		{Name: "b", Address: "198.51.100.2", Managed: true, User: "hlab"},
		{Name: "printer", Address: "198.51.100.50", Managed: false},
	})

	hosts, err := a.managedHosts(nil)
	if err != nil {
		t.Fatalf("managedHosts: %v", err)
	}

	var names []string
	for _, h := range hosts {
		names = append(names, h.Name)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("hosts = %v, want only the managed ones", names)
	}
}

func TestManagedHostsCarryTheirRecordedCredentials(t *testing.T) {
	// Repair re-asserts the account each host actually uses, which need not be
	// the default management user.
	a := testApp(t, []inventory.Host{
		{Name: "a", Address: "198.51.100.1", Port: 2222, Managed: true,
			User: "svc-hlab", IdentityFile: "/keys/id_hlab"},
	})

	hosts, err := a.managedHosts(nil)
	if err != nil {
		t.Fatalf("managedHosts: %v", err)
	}
	h := hosts[0]
	if h.User != "svc-hlab" || h.IdentityFile != "/keys/id_hlab" || h.Port != 2222 {
		t.Errorf("host = %+v, want its recorded credentials", h)
	}
}

func TestRepairResultClassification(t *testing.T) {
	tests := []struct {
		name    string
		result  repairResult
		ok      bool
		changed bool
	}{
		{
			name:    "already correct",
			result:  repairResult{Verified: true, SudoConfigured: true},
			ok:      true,
			changed: false,
		},
		{
			name:    "key had to be reinstalled",
			result:  repairResult{Verified: true, SudoConfigured: true, KeyInstalled: true},
			ok:      true,
			changed: true,
		},
		{
			name:    "account had to be recreated",
			result:  repairResult{Verified: true, AccountCreated: true},
			ok:      true,
			changed: true,
		},
		{
			name:   "unreachable",
			result: repairResult{Error: "locked out"},
			ok:     false,
		},
		{
			name:   "ran but could not be verified",
			result: repairResult{SudoConfigured: true},
			ok:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.ok(); got != tc.ok {
				t.Errorf("ok() = %v, want %v", got, tc.ok)
			}
			if tc.ok && tc.result.changed() != tc.changed {
				t.Errorf("changed() = %v, want %v", tc.result.changed(), tc.changed)
			}
		})
	}
}

// TestRepairReportsDriftDistinctlyFromFailure covers the reporting promise: a
// machine that was already correct must not look like one that needed work,
// and a failure must not be silently counted as ok.
func TestRepairReportsDriftDistinctlyFromFailure(t *testing.T) {
	a := testApp(t, nil)
	var out strings.Builder
	a.out = &out
	a.errOut = &strings.Builder{}
	a.format = output.FormatTable

	err := a.renderRepair([]repairResult{
		{Host: "fine", Verified: true, SudoConfigured: true},
		{Host: "drifted", Verified: true, SudoConfigured: true, KeyInstalled: true},
		{Host: "broken", Error: "locked out"},
	})

	if err == nil {
		t.Error("want a non-zero exit when a host failed")
	}

	body := out.String()
	for _, want := range []string{"fine", "drifted", "broken", "repaired", "failed", "2/3 ok, 1 needed repair"} {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q:\n%s", want, body)
		}
	}
}

func TestRepairExitCodeZeroWhenAllHealthy(t *testing.T) {
	a := testApp(t, nil)
	a.out = &strings.Builder{}
	a.errOut = &strings.Builder{}
	a.format = output.FormatTable

	if err := a.renderRepair([]repairResult{{Host: "a", Verified: true}}); err != nil {
		t.Errorf("renderRepair = %v, want nil for a healthy fleet", err)
	}
}
