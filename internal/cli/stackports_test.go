package cli

import (
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/stack"
)

// A stack that already holds a port conflicts with nothing: `compose up`
// replaces its own containers. Reporting it would mean passing --force on
// every routine redeploy, which is exactly when the check should still work —
// this is what made redeploying Caddy demand --force to replace itself.
func TestAStackDoesNotConflictWithItsOwnDeployment(t *testing.T) {
	ports := []stack.PublishedPort{{Port: 80, Service: "caddy"}}

	// Nothing recorded as listening, so nothing to clash with either way.
	if got := portConflicts([]listeningPort(nil), ports, nil); len(got) != 0 {
		t.Errorf("conflicts = %v, want none", got)
	}

	// Port held, and it is this stack's own: not a conflict.
	own := map[int]bool{80: true}
	if got := portConflicts([]listeningPort{{port: 80, process: "docker-proxy"}}, ports, own); len(got) != 0 {
		t.Errorf("conflicts = %v, want none for the stack's own port", got)
	}
}

// The check must still fire for a port held by something else, or the
// exclusion has quietly disabled it.
func TestAPortHeldBySomethingElseIsStillAConflict(t *testing.T) {
	ports := []stack.PublishedPort{{Port: 80, Service: "caddy"}}

	got := portConflicts([]listeningPort{{port: 80, process: "docker-proxy"}}, ports, map[int]bool{443: true})
	if len(got) != 1 || !strings.Contains(got[0], "80") {
		t.Errorf("conflicts = %v, want port 80 reported", got)
	}
}
