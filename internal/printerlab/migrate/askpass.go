package migrate

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	internalAskpassTokenEnv = "PLAB_INTERNAL_SSH_ASKPASS_TOKEN"
	askpassGateFileEnv      = "PLAB_INTERNAL_SSH_ASKPASS_GATE"
	askpassPasswordNameEnv  = "PLAB_INTERNAL_SSH_PASSWORD_ENV"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

var reservedPasswordEnvironmentNames = map[string]struct{}{
	"DISPLAY":               {},
	"SSH_ASKPASS":           {},
	"SSH_ASKPASS_REQUIRE":   {},
	internalAskpassTokenEnv: {},
	askpassGateFileEnv:      {},
	askpassPasswordNameEnv:  {},
}

// ValidatePasswordEnvironmentName accepts portable process environment names
// while rejecting the variables that control the askpass boundary itself.
func ValidatePasswordEnvironmentName(name string) error {
	if !environmentNamePattern.MatchString(name) {
		return fmt.Errorf(
			"SSH password environment name %q must match %s",
			name,
			environmentNamePattern.String(),
		)
	}
	for reserved := range reservedPasswordEnvironmentNames {
		if strings.EqualFold(name, reserved) {
			return fmt.Errorf("SSH password environment name %q is reserved", name)
		}
	}
	return nil
}

func passwordValue(name string) (string, error) {
	if err := ValidatePasswordEnvironmentName(name); err != nil {
		return "", err
	}
	value, present := os.LookupEnv(name)
	if !present || value == "" {
		return "", fmt.Errorf("SSH password environment variable %s is empty or unset", name)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("SSH password environment variable %s contains a line break", name)
	}
	if len(value) > 4096 {
		return "", fmt.Errorf("SSH password environment variable %s exceeds 4096 bytes", name)
	}
	return value, nil
}

func passwordEnvironmentFromArgs(args []string) (string, error) {
	name := ""
	seen := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		var candidate string
		switch {
		case argument == "--password-env":
			if index+1 >= len(args) {
				return "", fmt.Errorf("--password-env requires a value")
			}
			index++
			candidate = args[index]
		case strings.HasPrefix(argument, "--password-env="):
			candidate = strings.TrimPrefix(argument, "--password-env=")
		default:
			continue
		}
		if seen {
			return "", fmt.Errorf("--password-env was supplied more than once")
		}
		seen = true
		name = candidate
	}
	if !seen {
		return "", nil
	}
	if _, err := passwordValue(name); err != nil {
		return "", err
	}
	return name, nil
}

func randomAskpassToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate SSH askpass gate: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func configureAskpass(command *exec.Cmd, passwordEnvironment string) (func(), error) {
	if passwordEnvironment == "" {
		return func() {}, nil
	}
	if _, err := passwordValue(passwordEnvironment); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve plab executable for SSH askpass: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve plab askpass path: %w", err)
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("plab askpass executable is not a regular file: %s", executable)
	}
	token, err := randomAskpassToken()
	if err != nil {
		return nil, err
	}
	gate, err := os.CreateTemp("", "plab-ssh-askpass-*.gate")
	if err != nil {
		return nil, fmt.Errorf("create SSH askpass gate: %w", err)
	}
	gatePath := gate.Name()
	cleanup := func() { _ = os.Remove(gatePath) }
	if err := gate.Chmod(0o600); err != nil {
		_ = gate.Close()
		cleanup()
		return nil, fmt.Errorf("protect SSH askpass gate: %w", err)
	}
	if _, err := io.WriteString(gate, token); err != nil {
		_ = gate.Close()
		cleanup()
		return nil, fmt.Errorf("write SSH askpass gate: %w", err)
	}
	if err := gate.Close(); err != nil {
		cleanup()
		return nil, fmt.Errorf("close SSH askpass gate: %w", err)
	}

	command.Env = replaceEnvironment(os.Environ(), map[string]string{
		"DISPLAY":               "plab-ssh-askpass",
		"SSH_ASKPASS":           executable,
		"SSH_ASKPASS_REQUIRE":   "force",
		internalAskpassTokenEnv: token,
		askpassGateFileEnv:      gatePath,
		askpassPasswordNameEnv:  passwordEnvironment,
	})
	return cleanup, nil
}

func replaceEnvironment(base []string, replacements map[string]string) []string {
	result := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		replaced := false
		for replacement := range replacements {
			if name == replacement || (runtime.GOOS == "windows" && strings.EqualFold(name, replacement)) {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, entry)
		}
	}
	for name, value := range replacements {
		result = append(result, name+"="+value)
	}
	return result
}

// MaybeRunAskpass handles the private OpenSSH password callback.
// It validates the request token and returns passwords only for authorized requests.
func MaybeRunAskpass(args []string, writer io.Writer) (handled bool, exitCode int) {
	token := os.Getenv(internalAskpassTokenEnv)
	if token == "" {
		return false, 0
	}
	if len(args) != 1 || !strings.Contains(strings.ToLower(args[0]), "password") {
		return true, 1
	}
	gatePath := os.Getenv(askpassGateFileEnv)
	if !filepath.IsAbs(gatePath) {
		return true, 1
	}
	info, err := os.Lstat(gatePath)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, 1
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return true, 1
	}
	gateToken, err := os.ReadFile(gatePath)
	if err != nil || subtle.ConstantTimeCompare(gateToken, []byte(token)) != 1 {
		return true, 1
	}
	// Consume the capability before releasing the password. Even a second
	// matching prompt in the same child process must fail closed; the reviewed
	// bootstrap connection permits exactly one password prompt.
	if err := os.Remove(gatePath); err != nil {
		return true, 1
	}
	name := os.Getenv(askpassPasswordNameEnv)
	value, err := passwordValue(name)
	if err != nil {
		return true, 1
	}
	if _, err := fmt.Fprintln(writer, value); err != nil {
		return true, 1
	}
	return true, 0
}
