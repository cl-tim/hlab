package enroll

import (
	"strings"
	"testing"
)

const testBlob = "AAAAC3NzaC1lZDI1NTE5AAAAIGt1c3RvbWtleWJvZHlmb3J0ZXN0aW5nb25seQ=="

func TestRevokeScriptRejectsUnsafeInput(t *testing.T) {
	for _, opts := range []RevokeOptions{
		{User: "hlab; rm -rf /", KeyBlob: testBlob},
		{User: "hlab", KeyBlob: "AAAA' ; reboot ; '"},
		{User: "hlab", KeyBlob: "AAAA\nssh-ed25519 BBBB"},
	} {
		if _, err := BuildRevokeScript(opts); err == nil {
			t.Errorf("BuildRevokeScript(%+v) accepted unsafe input", opts)
		}
	}
}

// An account is only ever deleted when the machine says hlab made it. On an
// adopted machine the recorded account is the operator's own.
func TestBootstrapAndRevokeAgreeOnWhatHlabCreated(t *testing.T) {
	boot, err := BuildBootstrapScript("hlab", "ssh-ed25519 "+testBlob, SudoNoPassword)
	if err != nil {
		t.Fatalf("BuildBootstrapScript: %v", err)
	}
	revoke, err := BuildRevokeScript(RevokeOptions{User: "hlab", KeyBlob: testBlob, RemoveAccount: true})
	if err != nil {
		t.Fatalf("BuildRevokeScript: %v", err)
	}

	quoted := shellQuote(AccountComment)
	for name, script := range map[string]string{"bootstrap": boot, "revoke": revoke} {
		if !strings.Contains(script, quoted) {
			t.Errorf("%s does not use the account comment %s", name, quoted)
		}
		if !strings.Contains(script, SudoersFragment) {
			t.Errorf("%s does not use %s", name, SudoersFragment)
		}
		if strings.Contains(script, "__") {
			t.Errorf("%s has an unreplaced placeholder", name)
		}
	}
}

// A refusal must leave the machine untouched, so every check that can refuse
// comes before the first change.
func TestRevokeRefusesBeforeChangingAnything(t *testing.T) {
	script, err := BuildRevokeScript(RevokeOptions{User: "hlab", KeyBlob: testBlob, RemoveAccount: true})
	if err != nil {
		t.Fatalf("BuildRevokeScript: %v", err)
	}

	firstChange := strings.Index(script, `cat "$tmp" > "$f"`)
	for _, refusal := range []string{"exit 20", "exit 21", "exit 22", "exit 24"} {
		if i := strings.Index(script, refusal); i < 0 || i > firstChange {
			t.Errorf("%s is missing or comes after the first change", refusal)
		}
	}
}

// Key first: a failure between the two steps then leaves a machine the key
// cannot open, rather than a key that still logs in.
func TestRevokeRemovesTheKeyBeforeTheSudoRule(t *testing.T) {
	script, _ := BuildRevokeScript(RevokeOptions{User: "hlab", KeyBlob: testBlob})
	key := strings.Index(script, MarkerKeyRemoved)
	sudo := strings.Index(script, "rm -f "+SudoersFragment)
	if key < 0 || sudo < 0 || key > sudo {
		t.Errorf("key removal at %d, sudoers removal at %d; the key must go first", key, sudo)
	}
}

// The account is deleted from a detached process: userdel refuses while the
// session doing the deleting is logged in as that account.
func TestAccountRemovalIsDetached(t *testing.T) {
	with, _ := BuildRevokeScript(RevokeOptions{User: "hlab", RemoveAccount: true})
	if !strings.Contains(with, "setsid sh -c") || !strings.Contains(with, "userdel") {
		t.Error("account removal is not detached from the session")
	}
	without, _ := BuildRevokeScript(RevokeOptions{User: "hlab"})
	if !strings.Contains(without, "REMOVE_ACCOUNT=0") {
		t.Error("account removal is not off by default")
	}
}

func TestParseRevocation(t *testing.T) {
	out := strings.Join([]string{
		MarkerKeyRemoved + " /home/hlab/.ssh/authorized_keys",
		MarkerKeyRemoved + " /root/.ssh/authorized_keys",
		MarkerSudoRemoved,
		MarkerAccountRemoving,
		MarkerRevoked,
	}, "\n")
	r := ParseRevocation(out)
	if len(r.KeyFiles) != 2 || !r.SudoRemoved || !r.AccountRemoving || !r.Complete {
		t.Errorf("ParseRevocation = %+v", r)
	}

	// Without the final marker the script stopped part way.
	if ParseRevocation(MarkerKeyRemoved + " /x\n").Complete {
		t.Error("a truncated run was reported complete")
	}
}

func TestKeyBlob(t *testing.T) {
	key, err := LoadOrCreateKey(t.TempDir()+"/id_hlab", "hlab@workstation")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	got, err := KeyBlob(key.AuthorizedKey)
	if err != nil || got != strings.Fields(key.AuthorizedKey)[1] {
		t.Errorf("KeyBlob = %q, %v; want the second field of %q", got, err, key.AuthorizedKey)
	}
	if _, err := KeyBlob("not a key"); err == nil {
		t.Error("KeyBlob accepted a non-key")
	}
}
