package migrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

const maxKlippeeDiffOutputBytes = 64 * 1024

// KlippeeDiffSSHOutput runs only the installed physical printer account's
// read-only Klippee check --diff command and returns bounded output.
func (r Runner) KlippeeDiffSSHOutput(
	ctx context.Context,
	printer inventory.Printer,
	connectTimeout time.Duration,
) ([]byte, error) {
	sshExecutable, args, err := r.klippeeDiffSSHCommand(printer, connectTimeout)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, sshExecutable, args...)
	command.Env = bootstrapOnlyEnvironment(nil, r.BootstrapPasswordEnvironments, "")
	stdout := tailBuffer{limit: maxKlippeeDiffOutputBytes}
	stderr := tailBuffer{limit: maxCapturedDiagnosticBytes}
	command.Stdout = &stdout
	command.Stderr = &stderr
	if r.ErrOut != nil {
		command.Stderr = io.MultiWriter(r.ErrOut, &stderr)
	}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.data, ctx.Err()
		}
		if exitError, ok := err.(*exec.ExitError); ok {
			diagnostic := SafeDiagnosticSummary(stderr.data)
			if diagnostic != "" {
				return stdout.data, fmt.Errorf("SSH Klippee diff exited %d: %s", exitError.ExitCode(), diagnostic)
			}
			return stdout.data, fmt.Errorf("SSH Klippee diff exited %d", exitError.ExitCode())
		}
		return stdout.data, fmt.Errorf("run SSH Klippee diff: %w", err)
	}
	return bytes.Clone(stdout.data), nil
}

func (r Runner) klippeeDiffSSHCommand(
	printer inventory.Printer,
	connectTimeout time.Duration,
) (string, []string, error) {
	sshExecutable, args, err := r.fixedSSHCommand(printer, connectTimeout)
	if err != nil {
		return "", nil, err
	}
	args = append(args, klippeeFixedAccountArgs(printer, "check", "--diff")...)
	return sshExecutable, args, nil
}
