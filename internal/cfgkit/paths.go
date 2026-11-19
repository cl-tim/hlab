// Package cfgkit holds what the host config and the printer config both need
// and neither should own: where a tool's home directory is, how a config path
// is chosen, how ~ is expanded, and how a duration is written in YAML.
//
// The two configs remain separate files with separate schemas. This is the
// part that must not drift between them, because it carries a security
// property — the working directory is never searched and never written to.
package cfgkit

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Home returns the directory a tool stores everything in: the value of envVar
// when set, otherwise <user config dir>/<name>.
//
// It deliberately sits under the user's home directory rather than the working
// directory, so running inside a repository never leaves an inventory or a
// private key somewhere that could be committed.
func Home(envVar, name string) string {
	if v := strings.TrimSpace(os.Getenv(envVar)); v != "" {
		return v
	}
	// UserConfigDir is the per-OS convention and always lives under the user's
	// home: ~/.config on Linux, ~/Library/Application Support on macOS,
	// %AppData% on Windows.
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, name)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "."+name)
	}
	return "." + name
}

// ResolvePath picks the config file to read: the explicit path, else the one
// named by envVar, else the first of candidates that exists. An explicit or
// environment path that does not exist is an error, so a typo cannot silently
// become "no config"; a missing file at a default location is not, and yields
// "".
func ResolvePath(explicit, envVar string, candidates []string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if env := os.Getenv(envVar); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("config file %s (from %s): %w", env, envVar, err)
		}
		return env, nil
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", nil
}

// KnownHostsPath is the OpenSSH default known_hosts file.
func KnownHostsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}

// ExpandHome resolves a leading ~ to the user's home directory.
func ExpandHome(p string) string {
	if p == "" || (p[0] != '~') {
		return p
	}
	if len(p) > 1 && p[1] != '/' && p[1] != filepath.Separator {
		// ~otheruser is not supported; leave it alone.
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	rest := strings.TrimLeft(p[1:], "/\\")
	if runtime.GOOS == "windows" {
		return filepath.Join(home, filepath.FromSlash(rest))
	}
	return filepath.Join(home, rest)
}
