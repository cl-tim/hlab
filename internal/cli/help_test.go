package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/clikit"
)

func TestHelpIncludesDocumentationLinks(t *testing.T) {
	tests := []struct {
		name string
		args []string
		url  string
	}{
		{name: "root", args: []string{"--help"}, url: clikit.HlabDocsURL},
		{name: "host command", args: []string{"run", "--help"}, url: clikit.HlabDocsURL},
		{name: "printer", args: []string{"printer", "--help"}, url: clikit.PrinterDocsURL},
		{name: "nested printer command", args: []string{"printer", "firmware", "--help"}, url: clikit.PrinterDocsURL},
		{name: "klippee", args: []string{"klippee", "--help"}, url: clikit.KlippeeDocsURL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout, _, err := runRoot(t, test.args...)
			if err != nil {
				t.Fatalf("help: %v", err)
			}
			if count := strings.Count(stdout, test.url); count != 1 {
				t.Errorf("help output contains documentation URL %q %d times, want once:\n%s", test.url, count, stdout)
			}
		})
	}
}

func TestStructuredVersionOutputHasNoHelpFooter(t *testing.T) {
	stdout, stderr, err := runRoot(t, "version", "-o", "json")
	if err != nil {
		t.Fatalf("version -o json: %v", err)
	}
	if stderr != "" {
		t.Errorf("version wrote to stderr: %q", stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Errorf("version output is not valid JSON: %q", stdout)
	}
	for _, url := range []string{clikit.HlabDocsURL, clikit.PrinterDocsURL, clikit.KlippeeDocsURL} {
		if strings.Contains(stdout, url) {
			t.Errorf("structured version output contains help documentation URL %q", url)
		}
	}
}
