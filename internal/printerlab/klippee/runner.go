// Package klippee delegates desktop configuration sync to Klippee's own
// dependency-light client while enforcing printerlab's workspace boundary.
package klippee

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const connectionDirectory = ".klippee"
const connectionFile = "config.json"

// Runner executes the installed Klippee client in one printer's desktop
// workspace. Service identity, repository identity, and the token remain in
// Klippee's folder-scoped connection file and are never copied into plab.
type Runner struct {
	Executable string
	Workspace  string
	In         io.Reader
	Out        io.Writer
	ErrOut     io.Writer
}

// CommandError reports the Klippee process's exit status without discarding
// the diagnostics it already streamed to the configured output writers.
type CommandError struct {
	Code int
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("Klippee exited with status %d", e.Code)
}

type connection struct {
	ClientMode string `json:"client_mode"`
	ConfigDir  string `json:"config_dir"`
}

// ResolveWorkspace returns an absolute clean workspace path. It intentionally
// does not require the directory to exist so `hlab printer klippee path` can be used
// while setting up a new folder-scoped connection.
func ResolveWorkspace(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("Klippee workspace is not configured for this printer")
	}
	resolved, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve Klippee workspace %q: %w", value, err)
	}
	return filepath.Clean(resolved), nil
}

// Validate verifies the local executable and, critically, the folder-scoped
// desktop connection. Klippee itself has a legacy global fallback; plab never
// accepts that fallback because it could silently select another printer.
func (r Runner) Validate() error {
	_, _, err := r.validated()
	return err
}

func (r Runner) validated() (string, string, error) {
	workspace, err := ResolveWorkspace(r.Workspace)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(workspace)
	if err != nil {
		return "", "", fmt.Errorf("Klippee workspace %s: %w", workspace, err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("Klippee workspace %s is not a directory", workspace)
	}
	if err := validateConnection(workspace); err != nil {
		return "", "", err
	}

	executable := strings.TrimSpace(r.Executable)
	if executable == "" {
		return "", "", fmt.Errorf("defaults.klippee.executable is empty")
	}
	resolvedExecutable, err := exec.LookPath(executable)
	if err != nil {
		return "", "", fmt.Errorf("find Klippee executable %q: %w", executable, err)
	}
	return workspace, resolvedExecutable, nil
}

func validateConnection(workspace string) error {
	stateDirectory := filepath.Join(workspace, connectionDirectory)
	if info, err := os.Lstat(stateDirectory); err != nil {
		return fmt.Errorf(
			"folder-scoped Klippee connection not found at %s: %w",
			filepath.Join(stateDirectory, connectionFile), err,
		)
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("Klippee state path %s must be a real directory", stateDirectory)
	}

	path := filepath.Join(stateDirectory, connectionFile)
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("folder-scoped Klippee connection not found at %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("Klippee connection %s must be a regular file", path)
	}

	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read Klippee connection %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var configured connection
	if err := json.NewDecoder(file).Decode(&configured); err != nil {
		return fmt.Errorf("parse Klippee connection %s: %w", path, err)
	}
	if strings.TrimSpace(configured.ClientMode) != "desktop" {
		return fmt.Errorf(
			"Klippee connection %s has client_mode %q; plab local sync requires desktop mode",
			path, configured.ClientMode,
		)
	}
	if strings.TrimSpace(configured.ConfigDir) == "" {
		return fmt.Errorf("Klippee connection %s has no config_dir", path)
	}
	if !filepath.IsAbs(configured.ConfigDir) {
		return fmt.Errorf("Klippee connection %s has non-absolute config_dir %q", path, configured.ConfigDir)
	}
	configuredWorkspace, err := filepath.Abs(configured.ConfigDir)
	if err != nil {
		return fmt.Errorf("resolve Klippee config_dir %q: %w", configured.ConfigDir, err)
	}
	if !samePath(workspace, configuredWorkspace) {
		return fmt.Errorf(
			"Klippee connection %s targets config_dir %q, not workspace %q",
			path, filepath.Clean(configuredWorkspace), workspace,
		)
	}
	return nil
}

func samePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// Run validates the workspace, then streams a Klippee command through the
// caller's stdin, stdout, and stderr. This preserves Klippee's target summary,
// change list, confirmation prompt, and backup reporting.
func (r Runner) Run(ctx context.Context, args ...string) error {
	workspace, executable, err := r.validated()
	if err != nil {
		return err
	}
	command, err := commandContext(ctx, executable, args...)
	if err != nil {
		return err
	}
	command.Dir = workspace
	command.Stdin = r.In
	command.Stdout = r.Out
	command.Stderr = r.ErrOut
	if err := command.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return &CommandError{Code: exitError.ExitCode()}
		}
		return fmt.Errorf("run Klippee: %w", err)
	}
	return nil
}

func commandContext(ctx context.Context, executable string, args ...string) (*exec.Cmd, error) {
	if runtime.GOOS != "windows" {
		return exec.CommandContext(ctx, executable, args...), nil
	}

	extension := strings.ToLower(filepath.Ext(executable))
	if extension != ".cmd" && extension != ".bat" && extension != ".py" {
		return exec.CommandContext(ctx, executable, args...), nil
	}

	script := executable
	if extension == ".cmd" || extension == ".bat" {
		script = strings.TrimSuffix(executable, filepath.Ext(executable)) + ".py"
		if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf(
				"Klippee launcher %s has no adjacent Python client at %s",
				executable, script,
			)
		}
	}

	if launcher, err := exec.LookPath("py"); err == nil {
		commandArgs := append([]string{"-3", script}, args...)
		return exec.CommandContext(ctx, launcher, commandArgs...), nil
	}
	launcher, err := exec.LookPath("python")
	if err != nil {
		return nil, fmt.Errorf("run Klippee Python client: find py or python: %w", err)
	}
	commandArgs := append([]string{script}, args...)
	return exec.CommandContext(ctx, launcher, commandArgs...), nil
}
