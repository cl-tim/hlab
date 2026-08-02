package cli

import (
	"os"
	"strings"
	"testing"
)

// A flag can be declared, registered and completely ignored, and the compiler
// will not say a word — the BoolVar call counts as a use. That is how --self
// shipped once doing nothing at all, and how the same shape of half-applied
// edit broke `secret set`. So assert the command body actually acts on it.
func TestBackupActsOnItsFlags(t *testing.T) {
	source, err := os.ReadFile("backup.go")
	if err != nil {
		t.Fatalf("read backup.go: %v", err)
	}
	body := string(source)

	for flag, use := range map[string]string{
		"self":       "a.backupSelf(",
		"includeKey": "includeKey)",
	} {
		if !strings.Contains(body, use) {
			t.Errorf("the %s flag is registered but never acted on (looked for %q)", flag, use)
		}
	}
	if !strings.Contains(body, "if self {") {
		t.Error("--self is not branched on")
	}
}

// The management key is root-equivalent on every enrolled machine, and these
// archives are routinely pointed at a synced folder. Including it must be a
// deliberate act, never a default.
func TestSelfArchiveExcludesTheKeyByDefault(t *testing.T) {
	for _, name := range selfFiles {
		if strings.Contains(name, keyFile) {
			t.Errorf("%q is archived by default and it is the management key", name)
		}
	}
	if keyFile != "id_hlab" {
		t.Errorf("keyFile = %q, which is not the management key this guards", keyFile)
	}
	// The inventory must be there, or a replacement machine has nothing to be
	// rebuilt from.
	var hasInventory bool
	for _, name := range selfFiles {
		if name == "config.yaml" {
			hasInventory = true
		}
	}
	if !hasInventory {
		t.Error("the inventory is not archived, so a rebuild has no addressing to work from")
	}
}
