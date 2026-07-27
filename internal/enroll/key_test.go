package enroll

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestLoadOrCreateKeyGeneratesUsableKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id_hlab")

	key, err := LoadOrCreateKey(path, "hlab management key")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}
	if !key.Created {
		t.Error("Created = false, want true on first call")
	}

	// The private key must be parseable by the SSH library that will use it.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatalf("generated key is not a usable SSH private key: %v", err)
	}

	// The authorized_keys line must match the private key's public half.
	wantPub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if !strings.HasPrefix(key.AuthorizedKey, wantPub) {
		t.Errorf("AuthorizedKey %q does not match the private key", key.AuthorizedKey)
	}
	if !strings.HasSuffix(key.AuthorizedKey, "hlab management key") {
		t.Errorf("AuthorizedKey missing the comment: %q", key.AuthorizedKey)
	}
	if !strings.HasPrefix(key.AuthorizedKey, "ssh-ed25519 ") {
		t.Errorf("want an ed25519 key, got %q", key.AuthorizedKey)
	}
	if key.Fingerprint != ssh.FingerprintSHA256(signer.PublicKey()) {
		t.Error("Fingerprint does not match the key")
	}
}

func TestLoadOrCreateKeyIsStableAcrossCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id_hlab")

	first, err := LoadOrCreateKey(path, "hlab management key")
	if err != nil {
		t.Fatalf("first LoadOrCreateKey: %v", err)
	}

	second, err := LoadOrCreateKey(path, "hlab management key")
	if err != nil {
		t.Fatalf("second LoadOrCreateKey: %v", err)
	}

	if second.Created {
		t.Error("Created = true on second call, want false")
	}
	if second.AuthorizedKey != first.AuthorizedKey {
		t.Error("key changed between calls; enrolled machines would stop working")
	}
	if second.Fingerprint != first.Fingerprint {
		t.Error("fingerprint changed between calls")
	}
}

func TestLoadOrCreateKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not meaningful on Windows")
	}

	path := filepath.Join(t.TempDir(), "id_hlab")
	if _, err := LoadOrCreateKey(path, "hlab"); err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("private key mode = %o, want 600", perm)
	}
}

func TestLoadOrCreateKeyWritesPublicHalf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id_hlab")

	key, err := LoadOrCreateKey(path, "hlab")
	if err != nil {
		t.Fatalf("LoadOrCreateKey: %v", err)
	}

	pub, err := os.ReadFile(path + ".pub")
	if err != nil {
		t.Fatalf("read public key: %v", err)
	}
	if strings.TrimSpace(string(pub)) != key.AuthorizedKey {
		t.Error(".pub file does not match the authorized_keys line")
	}
}

func TestLoadOrCreateKeyRejectsUnreadableKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "id_hlab")
	if err := os.WriteFile(path, []byte("this is not a private key"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadOrCreateKey(path, "hlab")
	if err == nil {
		t.Fatal("want an error for an unparseable key file")
	}
	// The message must tell the operator how to recover rather than silently
	// overwriting a file that might be someone's real key.
	if !strings.Contains(err.Error(), "Move it aside") {
		t.Errorf("error = %q, want recovery guidance", err)
	}
}

func TestLoadOrCreateKeyRequiresPath(t *testing.T) {
	if _, err := LoadOrCreateKey("", "hlab"); err == nil {
		t.Fatal("want an error for an empty path")
	}
}
