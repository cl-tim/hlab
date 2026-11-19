package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/secrets"
)

// The script and the caller have to agree about how the value arrives.
//
// This is the check that was missing when `secret set` broke: the script was
// changed to read a staged file while the caller still appended the value to
// the script's own stdin. Both halves looked correct in isolation and the
// package tests passed, but together they could never work — the file the
// script waited for was never created, and the only symptom was a refusal
// saying the value had not arrived.
func TestSetStagesTheValueTheWayTheScriptExpects(t *testing.T) {
	source, err := os.ReadFile("secret.go")
	if err != nil {
		t.Fatalf("read secret.go: %v", err)
	}
	caller := string(source)

	if !strings.Contains(caller, "secrets.StageCommand") {
		t.Error("the caller does not stage the value through its own command")
	}

	// The old shape: value appended to the script and fed to one shell. sudo
	// gives the whole stream to that shell, which executes the value.
	if strings.Contains(caller, `script+"\n"+value`) {
		t.Error("the caller sends the value on the script's own stream, which a shell will execute")
	}

	script, err := secrets.BuildSetScript(secrets.Options{
		Stack: "s", File: "f.env", Key: "K",
	})
	if err != nil {
		t.Fatalf("BuildSetScript: %v", err)
	}

	// Whatever path the staging command writes must be the one the script
	// reads, or the two silently disagree.
	const staged = "/tmp/hlab-secret-value"
	if !strings.Contains(secrets.StageCommand, staged) {
		t.Errorf("StageCommand does not write %s", staged)
	}
	if !strings.Contains(script, staged) {
		t.Errorf("the script does not read %s", staged)
	}
}
