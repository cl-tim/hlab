package klippee

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const helperEnvironment = "PLAB_KLIPPEE_RUNNER_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnvironment) == "1" {
		cwd, _ := os.Getwd()
		stdin, _ := io.ReadAll(os.Stdin)
		var encodedArgs bytes.Buffer
		encoder := json.NewEncoder(&encodedArgs)
		encoder.SetEscapeHTML(false)
		_ = encoder.Encode(os.Args[1:])
		fmt.Printf("cwd=%s\nargs=%s\nstdin=%s", cwd, strings.TrimSpace(encodedArgs.String()), stdin)
		fmt.Fprintln(os.Stderr, "helper stderr")
		code, _ := strconv.Atoi(os.Getenv("PLAB_KLIPPEE_RUNNER_EXIT"))
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func writeConnection(t *testing.T, workspace, mode, configDir string) {
	t.Helper()
	state := filepath.Join(workspace, connectionDirectory)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]string{
		"base_url":      "https://klippee.example",
		"client_mode":   mode,
		"config_dir":    configDir,
		"repository_id": "repo-test",
		"token":         "must-never-be-printed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, connectionFile), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func validRunner(t *testing.T) (Runner, string) {
	t.Helper()
	workspace := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConnection(t, workspace, "desktop", workspace)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Runner{Executable: executable, Workspace: workspace}, workspace
}

func TestValidateRequiresFolderScopedConnection(t *testing.T) {
	runner, workspace := validRunner(t)
	if err := os.Remove(filepath.Join(workspace, connectionDirectory, connectionFile)); err != nil {
		t.Fatal(err)
	}

	// A connection elsewhere represents Klippee's legacy global fallback. Its
	// presence must not make this workspace valid.
	global := filepath.Join(t.TempDir(), ".klippee")
	if err := os.MkdirAll(global, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, connectionFile), []byte(`{"client_mode":"desktop"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	err := runner.Validate()
	if err == nil || !strings.Contains(err.Error(), "folder-scoped Klippee connection not found") {
		t.Fatalf("got %v, want folder-scoped connection error", err)
	}
}

func TestValidateRequiresDesktopMode(t *testing.T) {
	runner, workspace := validRunner(t)
	writeConnection(t, workspace, "printer", workspace)
	if err := runner.Validate(); err == nil || !strings.Contains(err.Error(), "requires desktop mode") {
		t.Fatalf("got %v, want desktop mode error", err)
	}
}

func TestValidateRequiresMatchingConfigDirectory(t *testing.T) {
	runner, workspace := validRunner(t)
	other := filepath.Join(t.TempDir(), "another printer")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConnection(t, workspace, "desktop", other)
	if err := runner.Validate(); err == nil || !strings.Contains(err.Error(), "not workspace") {
		t.Fatalf("got %v, want workspace mismatch", err)
	}
}

func TestValidateRejectsNonAbsoluteConfigDirectory(t *testing.T) {
	runner, workspace := validRunner(t)
	writeConnection(t, workspace, "desktop", "another-printer")
	if err := runner.Validate(); err == nil || !strings.Contains(err.Error(), "non-absolute config_dir") {
		t.Fatalf("got %v, want non-absolute config_dir error", err)
	}
}

func TestValidateRejectsMalformedConnection(t *testing.T) {
	runner, workspace := validRunner(t)
	path := filepath.Join(workspace, connectionDirectory, connectionFile)
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runner.Validate(); err == nil || !strings.Contains(err.Error(), "parse Klippee connection") {
		t.Fatalf("got %v, want connection parse error", err)
	}
}

func TestValidateUsesNativeClientWithoutExecutable(t *testing.T) {
	runner, _ := validRunner(t)
	runner.Executable = ""
	if err := runner.Validate(); err != nil {
		t.Fatalf("native client should not require an executable: %v", err)
	}
}

func TestDefaultRunnerSyncsWithoutPythonOrKlippeeExecutable(t *testing.T) {
	runner, workspace := validRunner(t)
	runner.Executable = "klippee"
	runner.Out = io.Discard
	t.Setenv("PATH", t.TempDir())
	if err := runner.Run(context.Background(), "variables", "set", "fixture", "native-value"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(workspace, "klippee_variables.cfg"))
	if err != nil || !strings.Contains(string(data), "# KLIPPEE_VARIABLE fixture=native-value") {
		t.Fatalf("native variable write failed: %s %v", data, err)
	}
}

func TestResolveWorkspaceDoesNotRequireInitializedDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-created")
	resolved, err := ResolveWorkspace(path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != filepath.Clean(path) {
		t.Fatalf("got %q, want %q", resolved, filepath.Clean(path))
	}
}

func TestRunStreamsStdioAndUsesWorkspace(t *testing.T) {
	runner, workspace := validRunner(t)
	var stdout, stderr bytes.Buffer
	runner.In = strings.NewReader("yes\n")
	runner.Out = &stdout
	runner.ErrOut = &stderr
	t.Setenv(helperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_RUNNER_EXIT", "0")

	if err := runner.Run(context.Background(), "capture", "--note", "safe & literal"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "cwd="+workspace) {
		t.Fatalf("stdout did not report workspace: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `args=["capture","--note","safe & literal"]`) {
		t.Fatalf("arguments were not preserved: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "stdin=yes") {
		t.Fatalf("stdin was not streamed: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "helper stderr") {
		t.Fatalf("stderr was not streamed: %q", stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "must-never-be-printed") {
		t.Fatal("runner exposed the connection token")
	}
}

func TestRunPreservesExitCode(t *testing.T) {
	runner, _ := validRunner(t)
	runner.Out = io.Discard
	runner.ErrOut = io.Discard
	t.Setenv(helperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_RUNNER_EXIT", "7")

	err := runner.Run(context.Background(), "check")
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.Code != 7 {
		t.Fatalf("got %v, want command exit 7", err)
	}
}

func TestWindowsBatchLauncherUsesAdjacentPythonClient(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows launcher behavior")
	}
	runner, _ := validRunner(t)
	bin := t.TempDir()
	launcher := filepath.Join(bin, "klippee.cmd")
	script := filepath.Join(bin, "klippee.py")
	if err := os.WriteFile(launcher, []byte("@echo off\r\nexit /b 99\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(
		"import json, sys\nprint('python_args=' + json.dumps(sys.argv[1:]))\n",
	), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	// Exercise interpreter selection with the Go subprocess helper so the
	// ordinary Go test suite does not require an installed Python interpreter.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "python.exe"), raw, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv(helperEnvironment, "1")
	runner.Executable = launcher
	runner.Out = &stdout
	runner.ErrOut = io.Discard
	if err := runner.Run(context.Background(), "target"); err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal([]string{script, "target"})
	if !strings.Contains(stdout.String(), "args="+string(expected)) {
		t.Fatalf("adjacent Python client did not run: %q", stdout.String())
	}
}
