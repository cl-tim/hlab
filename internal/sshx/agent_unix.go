//go:build !windows

package sshx

import (
	"errors"
	"net"
	"os"
)

// dialAgent connects to the ssh-agent listening on SSH_AUTH_SOCK.
func dialAgent() (net.Conn, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK is not set; no ssh-agent available")
	}
	return net.Dial("unix", sock)
}
