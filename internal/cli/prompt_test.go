package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newTestPrompter(input string) (*prompter, *bytes.Buffer) {
	var out bytes.Buffer
	return newPrompter(strings.NewReader(input), &out), &out
}

func TestPrompterConfirm(t *testing.T) {
	tests := []struct {
		input string
		def   bool
		want  bool
	}{
		{"y\n", false, true},
		{"Y\n", false, true},
		{"yes\n", false, true},
		{"n\n", true, false},
		{"no\n", true, false},
		{"\n", true, true},       // empty takes the default
		{"\n", false, false},     // ...either way
		{"maybe\n", true, false}, // anything unrecognised is a no
	}

	for _, tc := range tests {
		p, _ := newTestPrompter(tc.input)
		got, err := p.Confirm("Proceed?", tc.def)
		if err != nil {
			t.Fatalf("Confirm(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("Confirm(%q, default %v) = %v, want %v", tc.input, tc.def, got, tc.want)
		}
	}
}

func TestPrompterLineUsesDefaultWhenBlank(t *testing.T) {
	p, out := newTestPrompter("\n")
	got, err := p.Line("Inventory name:", "pi-dns")
	if err != nil {
		t.Fatalf("Line: %v", err)
	}
	if got != "pi-dns" {
		t.Errorf("Line = %q, want the default", got)
	}
	if !strings.Contains(out.String(), "[pi-dns]") {
		t.Errorf("prompt did not show the default: %q", out.String())
	}

	p, _ = newTestPrompter("  nas-01  \n")
	if got, _ := p.Line("Inventory name:", "pi-dns"); got != "nas-01" {
		t.Errorf("Line = %q, want nas-01 trimmed", got)
	}
}

func TestPrompterLineAllowsEmptyWhenNoDefault(t *testing.T) {
	// The description prompt is optional, so a blank answer must come back
	// blank rather than being rejected.
	p, _ := newTestPrompter("\n")
	got, err := p.Line("Description (optional):", "")
	if err != nil {
		t.Fatalf("Line: %v", err)
	}
	if got != "" {
		t.Errorf("Line = %q, want empty", got)
	}
}

func TestPrompterChoice(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"c\n", "c"},
		{"E\n", "e"},
		{"\n", "c"},         // default
		{"nonsense\n", "c"}, // unrecognised falls back to the default
	}

	for _, tc := range tests {
		p, _ := newTestPrompter(tc.input)
		got, err := p.Choice("Create or use existing?", []string{"c", "e"}, "c")
		if err != nil {
			t.Fatalf("Choice(%q): %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("Choice(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPrompterAbortsOnEOF(t *testing.T) {
	// Closed input must abort the walk rather than looping on empty answers.
	p, _ := newTestPrompter("")
	if _, err := p.Confirm("Proceed?", false); !errors.Is(err, ErrPromptAborted) {
		t.Errorf("Confirm on EOF = %v, want ErrPromptAborted", err)
	}
}

func TestPrompterPasswordRefusesWithoutTerminal(t *testing.T) {
	// Echo cannot be suppressed on a non-terminal, so a password must never be
	// read from one.
	p, _ := newTestPrompter("hunter2\n")
	if _, err := p.Password("Password:"); err == nil {
		t.Fatal("Password succeeded without a terminal")
	}
}
