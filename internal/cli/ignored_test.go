package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/config"
)

func TestIgnoredEnrollmentSurvivesConfigReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("# keep this comment\nhosts: []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ignored := candidate{Address: "::ffff:192.0.2.10"}
	for i := 0; i < 2; i++ {
		if err := config.RememberIgnored(path, ignored.endpoint()); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Ignored) != 1 || cfg.Ignored[0] != "192.0.2.10:22" {
		t.Fatalf("reloaded ignore list = %v", cfg.Ignored)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "# keep this comment") {
		t.Fatalf("config comment was lost, read error = %v", err)
	}

	var out bytes.Buffer
	app := &app{cfg: cfg, in: strings.NewReader(""), errOut: &out}
	host := candidate{Address: "192.0.2.10", Port: 22}
	if err := app.runEnrollment(context.Background(), []candidate{host}, &enrollOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No eligible machines") {
		t.Fatalf("ignored host was offered: %s", out.String())
	}
	if err := app.runEnrollment(context.Background(), []candidate{host}, &enrollOptions{includeIgnored: true}); err == nil ||
		!strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("include-ignored did not reach enrollment: %v", err)
	}
	if app.isIgnored(candidate{Address: host.Address, Port: 2222}) {
		t.Fatal("ignored a different SSH port")
	}
}
