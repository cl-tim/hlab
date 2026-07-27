// Package enroll brings an unmanaged machine under hlab's control: it logs in
// with an operator-supplied credential, creates a dedicated management account,
// installs hlab's own key, and verifies the result.
package enroll

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ManagementKey is hlab's own SSH identity: the key it installs on managed
// machines and authenticates with from then on.
type ManagementKey struct {
	// PrivatePath is where the private key lives on this machine.
	PrivatePath string
	// AuthorizedKey is the public key in authorized_keys form.
	AuthorizedKey string
	// Fingerprint identifies the key for display.
	Fingerprint string
	// Created reports whether this call generated the key rather than
	// loading an existing one.
	Created bool
}

// LoadOrCreateKey returns hlab's management key, generating an ed25519 pair on
// first use. The private key is written 0600 and never leaves this machine.
func LoadOrCreateKey(path, comment string) (*ManagementKey, error) {
	if path == "" {
		return nil, fmt.Errorf("no management key path configured")
	}

	if raw, err := os.ReadFile(path); err == nil {
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf(
				"management key %s could not be read: %w\n"+
					"Move it aside to have hlab generate a new one", path, err)
		}
		return &ManagementKey{
			PrivatePath:   path,
			AuthorizedKey: authorizedKeyLine(signer.PublicKey(), comment),
			Fingerprint:   ssh.FingerprintSHA256(signer.PublicKey()),
		}, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read management key %s: %w", path, err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate management key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, fmt.Errorf("encode management key: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	// O_EXCL so a key that appeared since the read above is never clobbered.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create management key %s: %w", path, err)
	}
	if err := pem.Encode(f, block); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write management key %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("write management key %s: %w", path, err)
	}

	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("derive public key: %w", err)
	}

	// Write the public half alongside it, purely for the operator's
	// convenience — nothing reads it back.
	_ = os.WriteFile(path+".pub", []byte(authorizedKeyLine(sshPub, comment)+"\n"), 0o644)

	return &ManagementKey{
		PrivatePath:   path,
		AuthorizedKey: authorizedKeyLine(sshPub, comment),
		Fingerprint:   ssh.FingerprintSHA256(sshPub),
		Created:       true,
	}, nil
}

// authorizedKeyLine renders a public key as a single authorized_keys entry.
func authorizedKeyLine(key ssh.PublicKey, comment string) string {
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if comment != "" {
		line += " " + comment
	}
	return line
}
