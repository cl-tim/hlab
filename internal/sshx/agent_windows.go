//go:build windows

package sshx

import (
	"errors"
	"net"
	"os"

	"github.com/Microsoft/go-winio"
)

// defaultAgentPipe is where the Windows OpenSSH agent service listens.
const defaultAgentPipe = `\\.\pipe\openssh-ssh-agent`

// dialAgent connects to the OpenSSH agent. On Windows the agent is exposed as
// a named pipe rather than a Unix socket, so SSH_AUTH_SOCK (if set) is treated
// as a pipe path.
func dialAgent() (net.Conn, error) {
	pipe := os.Getenv("SSH_AUTH_SOCK")
	if pipe == "" {
		pipe = defaultAgentPipe
	}

	conn, err := winio.DialPipe(pipe, nil)
	if err != nil {
		return nil, errors.Join(errors.New("connect to ssh-agent named pipe "+pipe), err)
	}
	return conn, nil
}
