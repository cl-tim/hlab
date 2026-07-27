package sshx

import (
	"fmt"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	// A fixed ed25519 host key; the value is irrelevant, only its type is read.
	const line = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJZ0Z0dm+7HRxKPBBaHrPPfEGFcBLYqLYbBsGSF9pFDN"
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatalf("parse test key: %v", err)
	}
	return key
}

// The failure this guards: hlab dialled without constraining the host key
// algorithm, Go negotiated a type known_hosts had no entry for, and the
// resulting "key mismatch" read as an intercepted connection on a machine that
// plain ssh reached without complaint.
func TestRecordedAlgorithmsAreOfferedOnMismatch(t *testing.T) {
	err := &knownhosts.KeyError{Want: []knownhosts.KnownKey{{Key: testKey(t)}}}

	got := recordedAlgorithms(fmt.Errorf("ssh: handshake failed: %w", err))
	if len(got) != 1 || got[0] != ssh.KeyAlgoED25519 {
		t.Errorf("recordedAlgorithms = %v, want [%s]", got, ssh.KeyAlgoED25519)
	}
}

// An unknown host carries an empty Want. Retrying there would offer no
// algorithms at all, which fails the handshake outright and would break
// trust-on-first-use enrolment.
func TestRecordedAlgorithmsIgnoresUnknownHosts(t *testing.T) {
	if got := recordedAlgorithms(&knownhosts.KeyError{}); got != nil {
		t.Errorf("recordedAlgorithms = %v, want nil for an unknown host", got)
	}
	if got := recordedAlgorithms(fmt.Errorf("connection refused")); got != nil {
		t.Errorf("recordedAlgorithms = %v, want nil for an unrelated error", got)
	}
}
