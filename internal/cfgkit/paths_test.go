package cfgkit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	got := ExpandHome("~/.ssh/id_ed25519")
	want := filepath.Join(home, ".ssh", "id_ed25519")
	if got != want {
		t.Errorf("ExpandHome = %q, want %q", got, want)
	}

	if got := ExpandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("ExpandHome(/abs/path) = %q, want unchanged", got)
	}
	if got := ExpandHome(""); got != "" {
		t.Errorf("ExpandHome(\"\") = %q, want empty", got)
	}
	if got := ExpandHome("~other/key"); got != "~other/key" {
		t.Errorf("ExpandHome(~other/key) = %q, want unchanged", got)
	}
}

func TestHomePrefersTheEnvironment(t *testing.T) {
	t.Setenv("CFGKIT_TEST_HOME", "  /somewhere/else  ")
	if got := Home("CFGKIT_TEST_HOME", "tool"); got != "/somewhere/else" {
		t.Errorf("Home = %q, want the trimmed environment value", got)
	}

	t.Setenv("CFGKIT_TEST_HOME", "")
	got := Home("CFGKIT_TEST_HOME", "tool")
	if filepath.Base(got) != "tool" || !filepath.IsAbs(got) {
		t.Errorf("Home = %q, want an absolute path ending in the tool name", got)
	}
}

// A path somebody named and got wrong must fail; only a default location may
// be absent without complaint.
func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(present, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.yaml")
	const env = "CFGKIT_TEST_CONFIG"

	t.Setenv(env, "")
	if got, err := ResolvePath("", env, []string{missing, present}); err != nil || got != present {
		t.Errorf("default search = %q, %v; want %q", got, err, present)
	}
	if got, err := ResolvePath("", env, []string{missing}); err != nil || got != "" {
		t.Errorf("nothing present = %q, %v; want empty and no error", got, err)
	}
	if _, err := ResolvePath(missing, env, []string{present}); err == nil {
		t.Error("a missing explicit path did not error")
	}

	t.Setenv(env, missing)
	if _, err := ResolvePath("", env, []string{present}); err == nil || !strings.Contains(err.Error(), env) {
		t.Errorf("a missing environment path error = %v, want one naming %s", err, env)
	}
	if got, err := ResolvePath(present, env, nil); err != nil || got != present {
		t.Errorf("explicit over environment = %q, %v; want %q", got, err, present)
	}
}

func TestDurationForms(t *testing.T) {
	cases := map[string]time.Duration{
		`"10s"`: 10 * time.Second,
		`2m`:    2 * time.Minute,
		`30`:    30 * time.Second,
	}
	for input, want := range cases {
		var got Duration
		if err := yaml.Unmarshal([]byte(input), &got); err != nil {
			t.Errorf("%s: %v", input, err)
			continue
		}
		if got.Duration() != want {
			t.Errorf("%s = %v, want %v", input, got, want)
		}
	}
	for _, input := range []string{`soon`, `[1, 2]`} {
		var got Duration
		if err := yaml.Unmarshal([]byte(input), &got); err == nil {
			t.Errorf("%s decoded to %v, want an error", input, got)
		}
	}
}
