package cli

import (
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestStarterConfigContainsNoLocalPrintersOrPaths(t *testing.T) {
	var parsed struct {
		Defaults struct {
			FirmwareRepo string `yaml:"firmwareRepo"`
			Klippee      struct {
				WorkspaceRoot string `yaml:"workspaceRoot"`
			} `yaml:"klippee"`
		} `yaml:"defaults"`
		Printers []map[string]any `yaml:"printers"`
	}
	if err := yaml.Unmarshal([]byte(starterConfig), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Printers) != 0 {
		t.Fatalf("starter config contains %d printer records", len(parsed.Printers))
	}
	if parsed.Defaults.FirmwareRepo != "" {
		t.Fatalf("starter config activates a local firmware repository: %q", parsed.Defaults.FirmwareRepo)
	}
	if parsed.Defaults.Klippee.WorkspaceRoot != "" {
		t.Fatalf("starter config activates a local Klippee workspace: %q",
			parsed.Defaults.Klippee.WorkspaceRoot)
	}
}
