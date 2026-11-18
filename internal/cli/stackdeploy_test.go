package cli

import (
	"path/filepath"
	"testing"
)

func TestResolveDeploySource(t *testing.T) {
	home := t.TempDir()
	path, name, err := resolveDeploySource(home, "web", "", "")
	if want := filepath.Join(home, "stacks", "web", "compose.yaml"); err != nil || path != want || name != "web" {
		t.Fatalf("saved project = %q, %q, %v; want %q, web", path, name, err, want)
	}
	path, name, err = resolveDeploySource(home, "", "legacy.yaml", "")
	if err != nil || path != "legacy.yaml" || name != "legacy" {
		t.Fatalf("explicit file = %q, %q, %v", path, name, err)
	}
	if _, _, err := resolveDeploySource(home, "web", "other.yaml", ""); err == nil {
		t.Fatal("accepted both a saved project and an explicit file")
	}
	if _, _, err := resolveDeploySource(home, "../web", "", ""); err == nil {
		t.Fatal("accepted a project name outside the stacks directory")
	}
}
