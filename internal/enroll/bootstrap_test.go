package enroll

import (
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/shellword"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITEST hlab management key"

func TestValidateUsername(t *testing.T) {
	valid := []string{"hlab", "_svc", "hlab-mgmt", "a", "user123"}
	for _, name := range valid {
		if err := ValidateUsername(name); err != nil {
			t.Errorf("ValidateUsername(%q) = %v, want nil", name, err)
		}
	}

	// Anything that could break out of the shell word, or that useradd would
	// reject, must be refused before it reaches the remote machine.
	invalid := []string{
		"", "Hlab", "1abc", "-hlab", "hl ab", "hlab;rm -rf /", "hlab'", "hlab$(id)",
		"hlab\nroot", "hlab/../root", strings.Repeat("a", 33),
	}
	for _, name := range invalid {
		if err := ValidateUsername(name); err == nil {
			t.Errorf("ValidateUsername(%q) = nil, want an error", name)
		}
	}
}

func TestBuildBootstrapScriptRejectsBadInput(t *testing.T) {
	if _, err := BuildBootstrapScript("bad name", testKey, SudoNoPassword); err == nil {
		t.Error("want error for an invalid username")
	}
	if _, err := BuildBootstrapScript("hlab", "", SudoNoPassword); err == nil {
		t.Error("want error for an empty key")
	}
	// A newline in the key would let it append extra authorized_keys entries.
	if _, err := BuildBootstrapScript("hlab", "ssh-ed25519 AAAA\nssh-rsa BBBB", SudoNoPassword); err == nil {
		t.Error("want error for a key containing a line break")
	}
}

func TestBuildBootstrapScriptSudoModes(t *testing.T) {
	tests := []struct {
		mode        SudoMode
		wantContain string
	}{
		{SudoNoPassword, "NOPASSWD:ALL"},
		{SudoPassword, `printf '%s ALL=(ALL) ALL\n'`},
		{SudoNone, "rm -f /etc/sudoers.d/60-hlab"},
	}

	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			script, err := BuildBootstrapScript("hlab", testKey, tc.mode)
			if err != nil {
				t.Fatalf("BuildBootstrapScript: %v", err)
			}
			if !strings.Contains(script, tc.wantContain) {
				t.Errorf("script missing %q", tc.wantContain)
			}
			if !strings.Contains(script, "SUDO_MODE='"+string(tc.mode)+"'") {
				t.Errorf("script does not set SUDO_MODE to %q", tc.mode)
			}
		})
	}
}

func TestBuildBootstrapScriptValidatesSudoersBeforeInstalling(t *testing.T) {
	// Installing a malformed sudoers file can make sudo unusable, so the
	// fragment is checked with visudo and only then moved into place.
	script, err := BuildBootstrapScript("hlab", testKey, SudoNoPassword)
	if err != nil {
		t.Fatalf("BuildBootstrapScript: %v", err)
	}

	visudoAt := strings.Index(script, "visudo -c -f")
	moveAt := strings.Index(script, `mv "$SUDO_TMP" /etc/sudoers.d/60-hlab`)

	if visudoAt < 0 {
		t.Fatal("script does not validate the sudoers fragment with visudo")
	}
	if moveAt < 0 {
		t.Fatal("script does not install the sudoers fragment")
	}
	if visudoAt > moveAt {
		t.Error("visudo validation runs after installation; it must run before")
	}
}

func TestBuildBootstrapScriptQuotesValues(t *testing.T) {
	// The key is remote-influenced only in the sense that it is generated
	// locally, but quoting must still be airtight.
	script, err := BuildBootstrapScript("hlab", testKey, SudoNoPassword)
	if err != nil {
		t.Fatalf("BuildBootstrapScript: %v", err)
	}

	if !strings.Contains(script, "HLAB_USER='hlab'") {
		t.Error("username is not single-quoted")
	}
	if !strings.Contains(script, "HLAB_KEY='"+testKey+"'") {
		t.Error("key is not single-quoted")
	}
	// No placeholder may survive into the rendered script.
	if strings.Contains(script, "__") {
		t.Errorf("unreplaced placeholder in script:\n%s", script)
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", `'plain'`},
		{"it's", `'it'\''s'`},
		{"a'b'c", `'a'\''b'\''c'`},
		{"", `''`},
	}
	for _, tc := range tests {
		if got := shellword.Quote(tc.in); got != tc.want {
			t.Errorf("shellword.Quote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestParseSudoMode(t *testing.T) {
	for _, s := range []string{"nopasswd", "NOPASSWD", " password ", "none"} {
		if _, err := ParseSudoMode(s); err != nil {
			t.Errorf("ParseSudoMode(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"", "root", "yes"} {
		if _, err := ParseSudoMode(s); err == nil {
			t.Errorf("ParseSudoMode(%q) = nil, want an error", s)
		}
	}
}
