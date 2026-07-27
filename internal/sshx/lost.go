package sshx

import (
	"errors"
	"io"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ConnectionLost reports whether an error means the session ended before the
// command could report a verdict, rather than the command itself failing.
//
// The distinction matters for changes that cut the connection as part of doing
// their job — reconfiguring the interface hlab is connected over is the case
// this exists for. Treating a lost session as a failure there reports a change
// that worked as one that did not; the caller should instead reconnect and look
// at the machine to decide.
func ConnectionLost(err error) bool {
	if err == nil {
		return false
	}

	var missing *ssh.ExitMissingError
	if errors.As(err, &missing) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}

	// The transport reports a dropped link as an unwrapped error, so the text
	// is all there is to go on for these.
	text := err.Error()
	for _, fragment := range []string{
		"exited without exit status",
		"connection lost",
		"connection reset",
		"broken pipe",
		"use of closed network connection",
	} {
		if strings.Contains(text, fragment) {
			return true
		}
	}
	return false
}
