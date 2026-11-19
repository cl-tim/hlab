package migrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

const maxKlippeeDiagnosticsOutputBytes = 16 * 1024

var sshHostLabel = regexp.MustCompile(
	`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`,
)

// KlippeeDiagnosticsSSHOutput runs the printer's upload-diagnostics command through SSH.
// It returns bounded output.
func (r Runner) KlippeeDiagnosticsSSHOutput(
	ctx context.Context,
	printer inventory.Printer,
	connectTimeout time.Duration,
) ([]byte, error) {
	return r.klippeeDiagnosticsSSHOutput(ctx, printer, connectTimeout, false)
}

// KlippeePreviousDiagnosticsSSHOutput uploads the newest rotated klippy.log
// through the installed Klippee client. The remote command is fixed: callers
// cannot supply a path, glob, or shell fragment, and Printerlab never receives
// the log contents.
func (r Runner) KlippeePreviousDiagnosticsSSHOutput(
	ctx context.Context,
	printer inventory.Printer,
	connectTimeout time.Duration,
) ([]byte, error) {
	return r.klippeeDiagnosticsSSHOutput(ctx, printer, connectTimeout, true)
}

func (r Runner) klippeeDiagnosticsSSHOutput(
	ctx context.Context,
	printer inventory.Printer,
	connectTimeout time.Duration,
	previous bool,
) ([]byte, error) {
	sshExecutable, args, err := r.klippeeDiagnosticsSSHCommand(
		printer, connectTimeout, previous,
	)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, sshExecutable, args...)
	command.Env = bootstrapOnlyEnvironment(
		nil, r.BootstrapPasswordEnvironments, "",
	)
	stdout := tailBuffer{limit: maxKlippeeDiagnosticsOutputBytes}
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
				return stdout.data, fmt.Errorf(
					"SSH diagnostics exited %d: %s",
					exitError.ExitCode(),
					diagnostic,
				)
			}
			return stdout.data, fmt.Errorf(
				"SSH diagnostics exited %d", exitError.ExitCode(),
			)
		}
		return stdout.data, fmt.Errorf("run SSH diagnostics: %w", err)
	}
	return bytes.Clone(stdout.data), nil
}

func (r Runner) klippeeDiagnosticsSSHCommand(
	printer inventory.Printer,
	connectTimeout time.Duration,
	previous bool,
) (string, []string, error) {
	sshExecutable, args, err := r.fixedSSHCommand(printer, connectTimeout)
	if err != nil {
		return "", nil, err
	}

	account := printer.User
	if printer.PrinterUser != "" {
		account = printer.PrinterUser
	}
	executable := "/home/" + account + "/.local/bin/klippee"
	if previous {
		logPattern := "/home/" + account + "/printer_data/logs/klippy.log.*"
		python := "import glob,os;" +
			"paths=[p for p in glob.glob(\"" + logPattern + "\") if os.path.isfile(p)];" +
			"latest=max(paths,key=os.path.getmtime);" +
			"os.execv(\"" + executable + "\",[\"klippee\",\"upload-diagnostics\",latest])"
		remoteCommand := "python3 -c '" + python + "'"
		if account != printer.User {
			remoteCommand = "sudo -n -H -u " + account + " -- " + remoteCommand
		}
		args = append(args, remoteCommand)
		return sshExecutable, args, nil
	}
	if account == printer.User {
		args = append(args, executable, "upload-diagnostics")
	} else {
		args = append(
			args,
			"sudo", "-n", "-H", "-u", account, "--",
			executable, "upload-diagnostics",
		)
	}
	return sshExecutable, args, nil
}

func (r Runner) fixedSSHCommand(
	printer inventory.Printer,
	connectTimeout time.Duration,
) (string, []string, error) {
	if err := validateKlippeeDiagnosticsSSHPrinter(printer); err != nil {
		return "", nil, err
	}
	sshExecutable := r.sshExecutable
	if sshExecutable == "" {
		var err error
		sshExecutable, err = exec.LookPath("ssh")
		if err != nil {
			return "", nil, fmt.Errorf("required command ssh was not found")
		}
	} else {
		absolute, err := filepath.Abs(sshExecutable)
		if err != nil {
			return "", nil, fmt.Errorf("resolve SSH executable: %w", err)
		}
		info, err := os.Stat(absolute)
		if err != nil || info.IsDir() {
			return "", nil, fmt.Errorf("SSH executable not found at %s", absolute)
		}
		sshExecutable = absolute
	}

	seconds := int(math.Ceil(connectTimeout.Seconds()))
	if seconds <= 0 {
		seconds = 10
	}
	if seconds > 60 {
		seconds = 60
	}
	args := []string{
		"-p", fmt.Sprint(printer.Port),
		"-o", fmt.Sprintf("ConnectTimeout=%d", seconds),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
	}
	if printer.IdentityFile != "" {
		if strings.ContainsAny(printer.IdentityFile, "\x00\r\n") {
			return "", nil, fmt.Errorf("SSH identity path contains a control character")
		}
		identity, err := filepath.Abs(printer.IdentityFile)
		if err != nil {
			return "", nil, fmt.Errorf("resolve SSH identity: %w", err)
		}
		info, err := os.Stat(identity)
		if err != nil || info.IsDir() {
			return "", nil, fmt.Errorf("SSH identity not found at %s", identity)
		}
		args = append(args, "-i", identity, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, "--", printer.User+"@"+printer.Address)
	return sshExecutable, args, nil
}

func validateKlippeeDiagnosticsSSHPrinter(printer inventory.Printer) error {
	if !plrAccountPattern.MatchString(printer.User) {
		return fmt.Errorf("invalid SSH account %q", printer.User)
	}
	account := printer.User
	if printer.PrinterUser != "" {
		account = printer.PrinterUser
	}
	if !plrAccountPattern.MatchString(account) {
		return fmt.Errorf("invalid Klippee printer account %q", account)
	}
	if printer.Port < 1 || printer.Port > 65535 {
		return fmt.Errorf("SSH port must be between 1 and 65535")
	}
	host := printer.Address
	if host == "" || len(host) > 253 || strings.TrimSpace(host) != host {
		return fmt.Errorf("invalid SSH host %q", host)
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) == 0 {
		return fmt.Errorf("invalid SSH host %q", host)
	}
	for _, label := range labels {
		if !sshHostLabel.MatchString(label) {
			return fmt.Errorf("invalid SSH host %q", host)
		}
	}
	return nil
}
