// Package config loads the hlab configuration file and turns it into a
// resolved inventory.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/cl-tim/hlab/internal/health"
	"github.com/cl-tim/hlab/internal/inventory"
)

// EnvConfigPath overrides the config file location.
const EnvConfigPath = "HLAB_CONFIG"

// EnvHome overrides the directory hlab stores everything in.
const EnvHome = "HLAB_HOME"

// Defaults are applied to every host that does not override them.
type Defaults struct {
	User           string   `yaml:"user"`
	Port           int      `yaml:"port"`
	IdentityFile   string   `yaml:"identityFile"`
	KnownHostsFile string   `yaml:"knownHostsFile"`
	ConnectTimeout Duration `yaml:"connectTimeout"`
	Concurrency    int      `yaml:"concurrency"`
}

// hostEntry is the on-disk shape of a host record.
//
// Managed is a pointer so an absent key can be told from an explicit false.
// Everything hlab writes states it outright; absence therefore means a
// hand-written entry, and those are managed by definition — a person does not
// list a machine in their config in order to not manage it. Defaulting the
// other way would silently make existing inventories unreachable.
type hostEntry struct {
	inventory.Host `yaml:",inline"`
	Managed        *bool `yaml:"managed"`
}

func (e hostEntry) resolve() inventory.Host {
	host := e.Host
	host.Managed = e.Managed == nil || *e.Managed
	return host
}

// File is the on-disk shape of the config file.
type File struct {
	Ignored   []string                     `yaml:"ignored,omitempty"`
	Endpoints map[string][]health.Endpoint `yaml:"endpoints,omitempty"`
	Defaults  Defaults                     `yaml:"defaults"`
	Hosts     []hostEntry                  `yaml:"hosts"`
	Groups    map[string][]string          `yaml:"groups"`

	// Discovered is a section written by earlier builds, when scan results
	// lived separately from the inventory. It is still read so those configs
	// keep working, and is folded into Hosts on the next write. Nothing
	// produces it any more.
	Discovered []LegacyDiscoveredHost `yaml:"discovered"`
}

// LegacyDiscoveredHost is an entry from the retired `discovered:` section.
type LegacyDiscoveredHost struct {
	Address   string    `yaml:"address"`
	Port      int       `yaml:"port,omitempty"`
	Hostname  string    `yaml:"hostname,omitempty"`
	OS        string    `yaml:"os,omitempty"`
	Banner    string    `yaml:"banner,omitempty"`
	FirstSeen time.Time `yaml:"firstSeen,omitempty"`
	LastSeen  time.Time `yaml:"lastSeen,omitempty"`
}

// Config is a loaded, validated configuration.
type Config struct {
	Ignored []string
	// Path is where the config was read from, or "" if defaults were used.
	Path      string
	Defaults  Defaults
	Inventory *inventory.Inventory
	Endpoints map[string][]health.Endpoint
}

// builtinDefaults are used for any field left unset in the file.
func builtinDefaults() Defaults {
	return Defaults{
		Port:           22,
		KnownHostsFile: defaultKnownHostsPath(),
		ConnectTimeout: Duration(10 * time.Second),
		Concurrency:    10,
	}
}

// Load reads the config from path, or from the default search locations when
// path is empty. A missing config file is not an error: it yields an empty
// inventory so that `hlab config init` can bootstrap one.
func Load(path string) (*Config, error) {
	resolved, err := resolvePath(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{Defaults: builtinDefaults()}

	var file File
	if resolved != "" {
		raw, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", resolved, err)
		}
		file, err = parseBytes(raw)
		if err != nil {
			return nil, fmt.Errorf("parse config %s: %w", resolved, err)
		}
		cfg.Path = resolved
	}

	cfg.Defaults = mergeDefaults(builtinDefaults(), file.Defaults)

	// A legacy `discovered:` section is folded in so older configs keep working.
	merged := migrateLegacyDiscovered(file)

	hosts := make([]inventory.Host, 0, len(merged))
	for _, h := range merged {
		hosts = append(hosts, applyHostDefaults(h, cfg.Defaults))
	}

	inv, err := inventory.New(hosts, file.Groups)
	if err != nil {
		where := resolved
		if where == "" {
			where = "<no config file>"
		}
		return nil, fmt.Errorf("invalid inventory in %s: %w", where, err)
	}
	cfg.Inventory = inv
	cfg.Endpoints = file.Endpoints
	cfg.Ignored = file.Ignored

	return cfg, nil
}

// parseBytes decodes a config document, rejecting unknown keys so a typo fails
// loudly rather than being silently ignored. An empty document is valid.
func parseBytes(raw []byte) (File, error) {
	var file File

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
		return File{}, err
	}
	return file, nil
}

// mergeDefaults overlays any non-zero field of over onto base.
func mergeDefaults(base, over Defaults) Defaults {
	out := base
	if over.User != "" {
		out.User = over.User
	}
	if over.Port != 0 {
		out.Port = over.Port
	}
	if over.IdentityFile != "" {
		out.IdentityFile = expandHome(over.IdentityFile)
	}
	if over.KnownHostsFile != "" {
		out.KnownHostsFile = expandHome(over.KnownHostsFile)
	}
	if over.ConnectTimeout != 0 {
		out.ConnectTimeout = over.ConnectTimeout
	}
	if over.Concurrency != 0 {
		out.Concurrency = over.Concurrency
	}
	return out
}

func applyHostDefaults(h inventory.Host, d Defaults) inventory.Host {
	if h.Port == 0 {
		h.Port = d.Port
	}
	if h.Address == "" {
		h.Address = h.Name
	}

	// Credentials only mean anything for machines hlab manages. Applying them
	// to merely-observed records would make an unreachable machine look
	// configured.
	if !h.Managed {
		return h
	}

	if h.User == "" {
		h.User = d.User
	}
	if h.IdentityFile == "" {
		h.IdentityFile = d.IdentityFile
	} else {
		h.IdentityFile = expandHome(h.IdentityFile)
	}
	return h
}

// resolvePath picks the config file to read. An explicit path that does not
// exist is an error; a missing file at a default location is not.
func resolvePath(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if env := os.Getenv(EnvConfigPath); env != "" {
		if _, err := os.Stat(env); err != nil {
			return "", fmt.Errorf("config file %s (from %s): %w", env, EnvConfigPath, err)
		}
		return env, nil
	}
	for _, candidate := range SearchPaths() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", nil
}

// Home returns the one directory hlab owns: everything it stores — the
// config, its management key, and any future state — lives here.
//
// It deliberately sits under the user's home directory rather than the working
// directory, so running hlab inside a repository never leaves an inventory or a
// private key somewhere that could be committed.
//
// Override it with $HLAB_HOME.
func Home() string {
	if v := strings.TrimSpace(os.Getenv(EnvHome)); v != "" {
		return v
	}
	// UserConfigDir is the per-OS convention and always lives under the user's
	// home: ~/.config on Linux, ~/Library/Application Support on macOS,
	// %AppData% on Windows.
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "hlab")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".hlab")
	}
	return ".hlab"
}

// SearchPaths lists, in priority order, where hlab looks for its config when
// none is given explicitly.
//
// The working directory is not searched. A per-project inventory is still
// perfectly possible — point at it with --config or $HLAB_CONFIG — but it is
// never picked up implicitly, so hlab cannot silently adopt (or write beside) a
// file that happens to be in a checkout.
func SearchPaths() []string {
	home := Home()
	return []string{
		filepath.Join(home, "config.yaml"),
		filepath.Join(home, "config.yml"),
	}
}

// DefaultPath is where `hlab config init` writes a new config.
func DefaultPath() string {
	return filepath.Join(Home(), "config.yaml")
}

// ManagementKeyPath is where hlab keeps the private key it installs on managed
// machines.
//
// This is deliberately independent of where the config file lives: an explicit
// --config pointing into a repository must not drag the key in after it.
func ManagementKeyPath() string {
	return filepath.Join(Home(), "id_hlab")
}

func defaultKnownHostsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}

// expandHome resolves a leading ~ to the user's home directory.
func expandHome(p string) string {
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
