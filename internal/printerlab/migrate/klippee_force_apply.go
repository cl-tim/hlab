package migrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
)

// KlippeeForceApplySSHOutput is the explicit fallback for a physical workspace
// whose reviewed local changes have already been merged into desired state.
// The remote operation is fixed to `klippee pull --force`.
func (r Runner) KlippeeForceApplySSHOutput(
	ctx context.Context,
	printer inventory.Printer,
	connectTimeout time.Duration,
) ([]byte, error) {
	sshExecutable, args, err := r.fixedSSHCommand(printer, connectTimeout)
	if err != nil {
		return nil, err
	}
	args = append(args, klippeeFixedAccountArgs(printer, "pull", "--force")...)
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
				return stdout.data, fmt.Errorf("SSH Klippee force apply exited %d: %s", exitError.ExitCode(), diagnostic)
			}
			return stdout.data, fmt.Errorf("SSH Klippee force apply exited %d", exitError.ExitCode())
		}
		return stdout.data, fmt.Errorf("run SSH Klippee force apply: %w", err)
	}
	return bytes.Clone(stdout.data), nil
}
