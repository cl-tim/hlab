// Package migrate runs printer migration operations.
package migrate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migration"
)

const nativeBackendFlag = "--internal-migration-backend"

const (
	KlipperRef                 = "214fdb2877ff4640e9316a663c1b8b2e232c86fd"
	MoonrakerRef               = "1cfb0c41e468645951a371621f06d32777b6107c"
	KatapultRef                = "ec59b9bb9ad6c2ec8d4dc6831fbc77f0b308e29e"
	maxCapturedDiagnosticBytes = 8 * 1024
	maxDiagnosticSummaryBytes  = 2 * 1024
)

// tailBuffer retains the last limit bytes of subprocess output for diagnostics.
type tailBuffer struct {
	data  []byte
	limit int
}

func (buffer *tailBuffer) Write(input []byte) (int, error) {
	written := len(input)
	if buffer.limit <= 0 {
		return written, nil
	}
	if len(input) >= buffer.limit {
		buffer.data = append(buffer.data[:0], input[len(input)-buffer.limit:]...)
		return written, nil
	}
	overflow := len(buffer.data) + len(input) - buffer.limit
	if overflow > 0 {
		copy(buffer.data, buffer.data[overflow:])
		buffer.data = buffer.data[:len(buffer.data)-overflow]
	}
	buffer.data = append(buffer.data, input...)
	return written, nil
}

// SafeDiagnosticSummary bounds subprocess diagnostics and removes terminal controls.
func SafeDiagnosticSummary(raw []byte) string {
	text := strings.ToValidUTF8(string(raw), "\uFFFD")
	text = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= maxDiagnosticSummaryBytes {
		return text
	}
	tail := strings.ToValidUTF8(
		text[len(text)-maxDiagnosticSummaryBytes:], "\uFFFD",
	)
	return "..." + tail
}

// Image file names written by the native backend's build stage. An artifact
// directory is enough to locate every image without further configuration.
const (
	MainboardImage        = "plus4-mainboard-klipper.bin"
	ToolheadImage         = "plus4-toolhead-klipper.bin"
	ToolheadKatapultImage = "plus4-toolhead-katapult.bin"
)

type Runner struct {
	Repo string
	// executable is a private subprocess test seam. Production reexecutes plab.
	executable string
	Home       string
	// sshExecutable is an internal test seam for fixed guarded SSH operations.
	// Production configuration never sets it and resolves the system ssh.
	sshExecutable                 string
	Out                           io.Writer
	ErrOut                        io.Writer
	BootstrapPasswordEnvironments []string
}

func (r Runner) Validate() error {
	repo, err := r.ToolRoot()
	if err != nil {
		return err
	}
	if info, err := os.Stat(repo); err != nil || !info.IsDir() {
		return fmt.Errorf("migration assets not found at %s", repo)
	}
	helper := filepath.Join(repo, "tools", "migration", "qidi-plus4-remote.sh")
	if info, err := os.Stat(helper); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("migration remote helper not found at %s", helper)
	}
	return nil
}

// ToolRoot uses the embedded backend unless a custom checkout was selected.
func (r Runner) ToolRoot() (string, error) {
	if r.Repo == "" {
		return migration.Ensure(r.Home)
	}
	repo, err := filepath.Abs(r.Repo)
	if err != nil {
		return "", fmt.Errorf("resolve custom migration tool path: %w", err)
	}
	return repo, nil
}

// Output captures stage stdout and streams diagnostics to stderr.
func (r Runner) Output(ctx context.Context, args ...string) ([]byte, error) {
	command, cleanup, err := r.command(ctx, args...)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	var stdout bytes.Buffer
	stderr := tailBuffer{limit: maxCapturedDiagnosticBytes}
	command.Stdout = &stdout
	command.Stderr = &stderr
	if r.ErrOut != nil {
		command.Stderr = io.MultiWriter(r.ErrOut, &stderr)
	}
	if err := command.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			diagnostic := SafeDiagnosticSummary(stderr.data)
			if diagnostic != "" {
				return stdout.Bytes(), fmt.Errorf(
					"migration stage failed with exit code %d: %s",
					exitError.ExitCode(), diagnostic,
				)
			}
			return stdout.Bytes(), fmt.Errorf(
				"migration stage failed with exit code %d", exitError.ExitCode(),
			)
		}
		return stdout.Bytes(), fmt.Errorf("run migration utility: %w", err)
	}
	return stdout.Bytes(), nil
}

func (r Runner) command(ctx context.Context, args ...string) (*exec.Cmd, func(), error) {
	if err := r.Validate(); err != nil {
		return nil, nil, err
	}
	repo, err := r.ToolRoot()
	if err != nil {
		return nil, nil, err
	}
	passwordEnvironment, err := passwordEnvironmentFromArgs(args)
	if err != nil {
		return nil, nil, err
	}
	if passwordEnvironment != "" && (len(args) == 0 || args[0] != "setup-ssh") {
		return nil, nil, fmt.Errorf(
			"--password-env is permitted only for the setup-ssh bootstrap command",
		)
	}
	executable := r.executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return nil, nil, err
		}
	}
	command := exec.CommandContext(ctx, executable, append([]string{nativeBackendFlag}, args...)...)
	cleanup, err := configureAskpass(command, passwordEnvironment)
	if err != nil {
		return nil, nil, err
	}
	command.Env = bootstrapOnlyEnvironment(
		command.Env, r.BootstrapPasswordEnvironments, passwordEnvironment,
	)
	command.Env = bootstrapOnlyEnvironment(command.Env,
		[]string{"PLAB_MIGRATION_HOME", "PLAB_MIGRATION_ROOT", "PLAB_INTERNAL_MIGRATION"}, "")
	if r.Home != "" {
		home, err := filepath.Abs(r.Home)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		command.Env = append(command.Env, "PLAB_MIGRATION_HOME="+home)
	}
	command.Env = append(command.Env, "PLAB_MIGRATION_ROOT="+repo, "PLAB_INTERNAL_MIGRATION=1")
	return command, cleanup, nil
}

func bootstrapOnlyEnvironment(
	base []string, sensitive []string, allowed string,
) []string {
	if base == nil {
		base = os.Environ()
	}
	result := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		remove := false
		for _, candidate := range sensitive {
			matches := name == candidate ||
				(runtime.GOOS == "windows" && strings.EqualFold(name, candidate))
			isAllowed := allowed != "" && (name == allowed ||
				(runtime.GOOS == "windows" && strings.EqualFold(name, allowed)))
			if matches && !isAllowed {
				remove = true
				break
			}
		}
		if !remove {
			result = append(result, entry)
		}
	}
	return result
}

func (r Runner) Run(ctx context.Context, args ...string) error {
	command, cleanup, err := r.command(ctx, args...)
	if err != nil {
		return err
	}
	defer cleanup()
	command.Stdin = os.Stdin
	command.Stdout = r.Out
	command.Stderr = r.ErrOut
	if err := command.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("migration stage failed with exit code %d", exitError.ExitCode())
		}
		return fmt.Errorf("run migration utility: %w", err)
	}
	return nil
}

func ConnectionArgs(printer inventory.Printer) []string {
	args := []string{
		"--printer", printer.Address,
		"--user", printer.User,
		"--port", fmt.Sprint(printer.Port),
	}
	// Only sent when it differs, so stages keep their existing behaviour on
	// printers that are managed through the Klipper account itself.
	if printer.PrinterUser != "" && printer.PrinterUser != printer.User {
		args = append(args, "--printer-user", printer.PrinterUser)
	}
	if printer.IdentityFile != "" {
		args = append(args, "--identity", printer.IdentityFile)
	}
	return args
}

// BootstrapConnectionArgs explicitly opts setup-ssh into the one-time password
// path. Every ordinary connection uses ConnectionArgs and therefore cannot
// inherit a bootstrap secret from inventory by accident.
func BootstrapConnectionArgs(printer inventory.Printer) []string {
	args := ConnectionArgs(printer)
	if printer.SSHPasswordEnv != "" {
		args = append(args, "--password-env", printer.SSHPasswordEnv)
	}
	return args
}

// ValidateBootstrapConnection checks SSH settings before setup-ssh starts.
// The password value is supplied through the gated OpenSSH askpass callback.
func ValidateBootstrapConnection(printer inventory.Printer) error {
	if printer.SSHPasswordEnv == "" {
		return nil
	}
	_, err := passwordValue(printer.SSHPasswordEnv)
	if err != nil {
		return fmt.Errorf("printer %q: %w", printer.Name, err)
	}
	return nil
}

// ValidateConnectionArguments lets orchestration reject any attempt to attach
// a bootstrap secret to an ordinary stage before recording or spawning it.
func ValidateConnectionArguments(printer inventory.Printer, args []string) error {
	name, err := passwordEnvironmentFromArgs(args)
	if err != nil {
		return err
	}
	if name == "" {
		return nil
	}
	if len(args) == 0 || args[0] != "setup-ssh" {
		return fmt.Errorf(
			"--password-env is permitted only for the setup-ssh bootstrap command",
		)
	}
	if name != printer.SSHPasswordEnv {
		return fmt.Errorf(
			"printer %q connection requests password environment %q, inventory specifies %q",
			printer.Name,
			name,
			printer.SSHPasswordEnv,
		)
	}
	return ValidateBootstrapConnection(printer)
}

func RequirePlus4(printer inventory.Printer) error {
	if printer.Model != "plus4" {
		return fmt.Errorf(
			"printer %q model is %q; mainline flashing is currently implemented only for model plus4",
			printer.Name, printer.Model)
	}
	return nil
}
