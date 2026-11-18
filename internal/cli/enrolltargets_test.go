package cli

import (
	"testing"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

func TestNamedEnrollmentTargets(t *testing.T) {
	inv, err := inventory.New([]inventory.Host{
		{Name: "new-pi", Address: "192.0.2.10", Port: 2222, Description: "workbench"},
		{Name: "managed-pi", Address: "192.0.2.11", Managed: true},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	named, remaining, err := namedEnrollmentTargets(inv, []string{"new-pi", "192.0.2.12"})
	if err != nil || len(named) != 1 || len(remaining) != 1 {
		t.Fatalf("targets = %+v, %v, %v", named, remaining, err)
	}
	if got := named[0]; !got.Stored || got.Address != "192.0.2.10" || got.Port != 2222 || got.Description != "workbench" ||
		remaining[0] != "192.0.2.12" {
		t.Fatalf("stored candidate = %+v, remaining = %v", got, remaining)
	}
	if _, _, err := namedEnrollmentTargets(inv, []string{"managed-pi"}); err == nil {
		t.Fatal("accepted an already-managed inventory name")
	}
}
