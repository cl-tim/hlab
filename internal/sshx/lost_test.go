package sshx

import (
	"errors"
	"fmt"
	"io"
	"testing"
)

// The failure this guards: applying a static address made NetworkManager
// reactivate the connection hlab was talking over, the session died without an
// exit status, and hlab called a change that had actually worked a failure.
func TestConnectionLostRecognisesASessionThatDiedWithoutAVerdict(t *testing.T) {
	for _, err := range []error{
		errors.New("wait: remote command exited without exit status or exit signal"),
		fmt.Errorf("run: %w", io.EOF),
		errors.New("read tcp 198.51.100.2:22: connection reset by peer"),
		errors.New("write: broken pipe"),
	} {
		if !ConnectionLost(err) {
			t.Errorf("ConnectionLost(%v) = false, want true", err)
		}
	}
}

// A command that ran and failed must stay a failure: treating it as
// inconclusive would push hlab on to confirm a change that never happened.
func TestConnectionLostIgnoresRealFailures(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("sudo: a password is required"),
		errors.New("ssh: handshake failed: knownhosts: key mismatch"),
		errors.New("dial tcp 198.51.100.2:22: connection refused"),
	} {
		if ConnectionLost(err) {
			t.Errorf("ConnectionLost(%v) = true, want false", err)
		}
	}
}
