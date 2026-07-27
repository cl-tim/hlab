package sshx

import (
	"bytes"
	"context"
	"errors"
	"io"

	"golang.org/x/crypto/ssh"
)

// ExecResult is the outcome of running one command on an existing connection.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// OK reports whether the command exited zero.
func (r ExecResult) OK() bool { return r.ExitCode == 0 }

// Exec runs a single command on an already-open client, optionally feeding it
// stdin, and returns its output.
//
// A returned error means the command could not be run at all. A command that
// ran and exited non-zero is reported through ExitCode with a nil error, the
// same distinction the fan-out executor makes.
func Exec(ctx context.Context, client *ssh.Client, command string, stdin io.Reader) (ExecResult, error) {
	session, err := client.NewSession()
	if err != nil {
		return ExecResult{ExitCode: -1}, err
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		session.Stdin = stdin
	}

	// Close the session if the context is cancelled, which unblocks Run.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGTERM)
			_ = session.Close()
		case <-done:
		}
	}()

	runErr := session.Run(command)

	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}

	if runErr != nil {
		var exitErr *ssh.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		res.ExitCode = -1
		return res, runErr
	}

	return res, nil
}
