package secrets

import (
	"strings"
	"testing"
)

// The stack and file names become a path in a script running as root, so they
// are a security boundary rather than a convenience.
func TestSecretPathsCannotEscapeTheStackDirectory(t *testing.T) {
	for _, file := range []string{
		"../../../etc/cron.d/x", "/etc/passwd", "..", "config/../../x",
		"a/b/c", "other/thing", "-rf", "with'quote", "with\"quote", "nul\x00",
	} {
		if _, err := BuildGenerateScript(Options{Stack: "s", File: file}); err == nil {
			t.Errorf("BuildGenerateScript accepted file %q", file)
		}
		if _, err := BuildShowScript("s", file); err == nil {
			t.Errorf("BuildShowScript accepted file %q", file)
		}
	}
	for _, stack := range []string{"../etc", "s/../..", "S PACE", "'"} {
		if _, err := BuildGenerateScript(Options{Stack: stack, File: "f"}); err == nil {
			t.Errorf("BuildGenerateScript accepted stack %q", stack)
		}
	}
	// The shapes that are actually used must work.
	for _, file := range []string{"admin-password", "config/admin-password", "homepage.env"} {
		if _, err := BuildGenerateScript(Options{Stack: "technitium", File: file}); err != nil {
			t.Errorf("BuildGenerateScript rejected %q: %v", file, err)
		}
	}
}

// A generated secret must be created on the machine, not sent to it: that is
// the entire reason this exists rather than a local random string being pushed.
func TestGeneratedSecretsNeverCrossTheNetwork(t *testing.T) {
	script, err := BuildGenerateScript(Options{Stack: "technitium", File: "config/admin-password"})
	if err != nil {
		t.Fatalf("BuildGenerateScript: %v", err)
	}
	if !strings.Contains(script, "/dev/urandom") {
		t.Error("the value is not generated on the machine")
	}
	if !strings.Contains(script, "chmod 0600") {
		t.Error("the secret is not restricted to its owner")
	}
	// Replacing a credential by accident is worse than failing to write one.
	if !strings.Contains(script, "already exists") {
		t.Error("an existing secret would be overwritten silently")
	}
}

// An env file legitimately holds several keys, so writing one must not discard
// the others.
func TestEnvSecretsPreserveTheOtherKeys(t *testing.T) {
	script, err := BuildGenerateScript(Options{
		Stack: "homepage", File: "homepage.env", Key: "API_KEY",
	})
	if err != nil {
		t.Fatalf("BuildGenerateScript: %v", err)
	}
	if !strings.Contains(script, "grep -v") {
		t.Error("the env file is rewritten wholesale rather than having one key replaced")
	}
	if !strings.Contains(script, "API_KEY") {
		t.Error("the key is missing from the write")
	}
}

func TestEnvKeysAreValidated(t *testing.T) {
	// An empty key is not invalid — it means "not an env secret", which is the
	// whole-file path.
	for _, key := range []string{"lower", "with-dash", "1LEADING", "with space", "x';id;'"} {
		if _, err := BuildGenerateScript(Options{Stack: "s", File: "f.env", Key: key}); err == nil {
			t.Errorf("accepted env key %q", key)
		}
	}
	if _, err := BuildGenerateScript(Options{Stack: "s", File: "f.env", Key: "API_KEY_2"}); err != nil {
		t.Errorf("rejected a valid env key: %v", err)
	}
}

func TestGeneratedLengthIsBounded(t *testing.T) {
	for _, n := range []int{1, 7, 513, 100000} {
		if _, err := BuildGenerateScript(Options{Stack: "s", File: "f", Length: n}); err == nil {
			t.Errorf("accepted length %d", n)
		}
	}
}

// The listing must never carry a value, and must run privileged or it reports
// a machine full of credentials as having none.
func TestListingShowsNoValues(t *testing.T) {
	if strings.Contains(ListScript, "cat ") {
		t.Error("the listing reads file contents")
	}
	if !strings.Contains(ListScript, "$SUDO find") {
		t.Error("the listing walks unprivileged and will not see 0750 stack directories")
	}
	for _, want := range []string{"600|400", "*password*", "*.env"} {
		if !strings.Contains(ListScript, want) {
			t.Errorf("the listing does not recognise %q as a secret", want)
		}
	}
}

// The value must never share a stream with the script that stores it.
//
// Feeding both to `sudo -n sh -s` fails silently and dangerously: sudo hands
// the whole stream to the shell, which reads the appended value as another
// line of script and tries to execute it. The secret ends up empty and the
// credential has been handed to a shell as a command.
func TestSetReadsTheValueFromItsOwnStream(t *testing.T) {
	script, err := BuildSetScript(Options{Stack: "homeassistant", File: "homeassistant.env", Key: "HASS_TOKEN"})
	if err != nil {
		t.Fatalf("BuildSetScript: %v", err)
	}

	if !strings.Contains(script, "/tmp/hlab-secret-value") {
		t.Error("the value is not staged separately from the script")
	}
	// A bare `cat` with no file reads stdin, which is the stream the script
	// itself arrived on — the exact mistake this guards.
	if strings.Contains(script, "$(cat)\n") || strings.Contains(script, "VALUE=$(cat)\n") {
		t.Error("the value is read from the script's own stdin")
	}
	if !strings.Contains(StageCommand, "cat > /tmp/hlab-secret-value") {
		t.Error("the staging command does not receive the value on its own stdin")
	}
}

// An empty value must fail loudly rather than writing an empty secret that
// looks stored and silently authenticates nothing.
func TestSetRefusesAnEmptyValue(t *testing.T) {
	script, err := BuildSetScript(Options{Stack: "s", File: "f.env", Key: "K"})
	if err != nil {
		t.Fatalf("BuildSetScript: %v", err)
	}
	for _, want := range []string{"did not arrive", "is empty"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script does not refuse the %q case", want)
		}
	}
}

// The staged value is removed whatever happens, so a secret is not left
// readable in /tmp after a failure.
func TestStagedValueIsAlwaysRemoved(t *testing.T) {
	script, err := BuildSetScript(Options{Stack: "s", File: "f"})
	if err != nil {
		t.Fatalf("BuildSetScript: %v", err)
	}
	if !strings.Contains(script, `trap 'rm -f "$STAGED"' EXIT`) {
		t.Error("the staged value is not cleaned up on every exit path")
	}
}
