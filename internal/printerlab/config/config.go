// Package config loads printerlab's inventory and defaults.
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

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

const (
	EnvConfigPath = "PLAB_CONFIG"
	EnvHome       = "PLAB_HOME"
)

type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	value, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	*d = Duration(value)
	return nil
}

func (d Duration) Duration() time.Duration { return time.Duration(d) }

type Defaults struct {
	User           string   `yaml:"user"`
	PrinterUser    string   `yaml:"printerUser"`
	Port           int      `yaml:"port"`
	IdentityFile   string   `yaml:"identityFile"`
	KnownHostsFile string   `yaml:"knownHostsFile"`
	MoonrakerPort  int      `yaml:"moonrakerPort"`
	APIKeyEnv      string   `yaml:"apiKeyEnv"`
	ConnectTimeout Duration `yaml:"connectTimeout"`
	Concurrency    int      `yaml:"concurrency"`
	FirmwareRepo   string   `yaml:"firmwareRepo"`

	Migration MigrationDefaults `yaml:"migration"`
	Klippee   KlippeeDefaults   `yaml:"klippee"`
}

// MigrationDefaults holds workstation build settings.
// Per-printer settings such as BeaconSerial belong in inventory.Printer.
type MigrationDefaults struct {
	KlipperRef   string `yaml:"klipperRef"`
	MoonrakerRef string `yaml:"moonrakerRef"`
	KatapultRef  string `yaml:"katapultRef"`
	WSLDistro    string `yaml:"wslDistro"`
	KlipperDir   string `yaml:"klipperDir"`
	KatapultDir  string `yaml:"katapultDir"`
	ArtifactDir  string `yaml:"artifactDir"`
	ConfigOutput string `yaml:"configOutput"`
	ProfileDir   string `yaml:"profileDir"`
	SDCard       string `yaml:"sdcard"`
}

// KlippeeDefaults configures the desktop client. WorkspaceRoot derives a folder
// for each printer; klippee.workspace overrides that folder.
// Klippee stores credentials in the workspace's connection file.
type KlippeeDefaults struct {
	Executable    string `yaml:"executable"`
	WorkspaceRoot string `yaml:"workspaceRoot"`
}

type File struct {
	Defaults Defaults            `yaml:"defaults"`
	Printers []inventory.Printer `yaml:"printers"`
	Groups   map[string][]string `yaml:"groups"`
}

type Config struct {
	Path      string
	Defaults  Defaults
	Inventory *inventory.Inventory
}

func builtinDefaults() Defaults {
	return Defaults{
		User:           "mks",
		Port:           22,
		MoonrakerPort:  7125,
		ConnectTimeout: Duration(10 * time.Second),
		Concurrency:    4,
		KnownHostsFile: defaultKnownHostsPath(),
		Klippee:        KlippeeDefaults{Executable: "klippee"},
	}
}

func Load(explicit string) (*Config, error) {
	path, err := resolvePath(explicit)
	if err != nil {
		return nil, err
	}
	cfg := &Config{Defaults: builtinDefaults()}
	var file File
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&file); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		cfg.Path = path
	}
	cfg.Defaults = deriveMigrationPaths(mergeDefaults(cfg.Defaults, file.Defaults))
	printers := make([]inventory.Printer, len(file.Printers))
	for i, printer := range file.Printers {
		printers[i] = applyDefaults(printer, cfg.Defaults)
	}
	cfg.Inventory, err = inventory.New(printers, file.Groups)
	if err != nil {
		return nil, fmt.Errorf("invalid inventory: %w", err)
	}
	return cfg, nil
}

func mergeDefaults(base, override Defaults) Defaults {
	if override.User != "" {
		base.User = override.User
	}
	if override.PrinterUser != "" {
		base.PrinterUser = override.PrinterUser
	}
	if override.Port != 0 {
		base.Port = override.Port
	}
	if override.IdentityFile != "" {
		base.IdentityFile = expandHome(override.IdentityFile)
	}
	if override.KnownHostsFile != "" {
		base.KnownHostsFile = expandHome(override.KnownHostsFile)
	}
	if override.MoonrakerPort != 0 {
		base.MoonrakerPort = override.MoonrakerPort
	}
	if override.APIKeyEnv != "" {
		base.APIKeyEnv = override.APIKeyEnv
	}
	if override.ConnectTimeout != 0 {
		base.ConnectTimeout = override.ConnectTimeout
	}
	if override.Concurrency != 0 {
		base.Concurrency = override.Concurrency
	}
	if override.FirmwareRepo != "" {
		base.FirmwareRepo = expandHome(override.FirmwareRepo)
	}
	base.Migration = mergeMigration(base.Migration, override.Migration)
	base.Klippee = mergeKlippee(base.Klippee, override.Klippee)
	return base
}

func mergeKlippee(base, override KlippeeDefaults) KlippeeDefaults {
	if override.Executable != "" {
		base.Executable = expandHome(override.Executable)
	}
	if override.WorkspaceRoot != "" {
		base.WorkspaceRoot = expandHome(override.WorkspaceRoot)
	}
	return base
}

func mergeMigration(base, override MigrationDefaults) MigrationDefaults {
	if override.KlipperRef != "" {
		base.KlipperRef = override.KlipperRef
	}
	if override.MoonrakerRef != "" {
		base.MoonrakerRef = override.MoonrakerRef
	}
	if override.KatapultRef != "" {
		base.KatapultRef = override.KatapultRef
	}
	if override.WSLDistro != "" {
		base.WSLDistro = override.WSLDistro
	}
	if override.KlipperDir != "" {
		base.KlipperDir = expandHome(override.KlipperDir)
	}
	if override.KatapultDir != "" {
		base.KatapultDir = expandHome(override.KatapultDir)
	}
	if override.ArtifactDir != "" {
		base.ArtifactDir = expandHome(override.ArtifactDir)
	}
	if override.ConfigOutput != "" {
		base.ConfigOutput = expandHome(override.ConfigOutput)
	}
	if override.ProfileDir != "" {
		base.ProfileDir = expandHome(override.ProfileDir)
	}
	if override.SDCard != "" {
		base.SDCard = override.SDCard
	}
	return base
}

// deriveMigrationPaths keeps bundled output outside the versioned tooling cache.
// Existing custom asset inventories retain their historical artifact defaults.
func deriveMigrationPaths(defaults Defaults) Defaults {
	artifacts := filepath.Join(Home(), "artifacts")
	if defaults.FirmwareRepo != "" {
		artifacts = filepath.Join(defaults.FirmwareRepo, "artifacts")
	}
	if defaults.Migration.ArtifactDir == "" {
		defaults.Migration.ArtifactDir = filepath.Join(artifacts, "mainline-firmware")
	}
	if defaults.Migration.ConfigOutput == "" {
		defaults.Migration.ConfigOutput = filepath.Join(artifacts, "generated-configs")
	}
	return defaults
}

func applyDefaults(printer inventory.Printer, defaults Defaults) inventory.Printer {
	if printer.Firmware == "" {
		printer.Firmware = inventory.FirmwareAuto
	}
	if printer.User == "" {
		printer.User = defaults.User
	}
	if printer.PrinterUser == "" {
		printer.PrinterUser = defaults.PrinterUser
	}
	if printer.Port == 0 {
		printer.Port = defaults.Port
	}
	if printer.IdentityFile == "" {
		printer.IdentityFile = defaults.IdentityFile
	} else {
		printer.IdentityFile = expandHome(printer.IdentityFile)
	}
	if printer.MoonrakerPort == 0 {
		printer.MoonrakerPort = defaults.MoonrakerPort
	}
	if printer.APIKeyEnv == "" {
		printer.APIKeyEnv = defaults.APIKeyEnv
	}
	printer.Migration = applyMigrationDefaults(printer.Migration, defaults, printer.Name)
	printer.Klippee = applyKlippeeDefaults(printer.Klippee, defaults, printer.Name)
	return printer
}

func applyKlippeeDefaults(
	klippee inventory.Klippee,
	defaults Defaults,
	name string,
) inventory.Klippee {
	if klippee.Workspace != "" {
		klippee.Workspace = expandHome(klippee.Workspace)
	} else if defaults.Klippee.WorkspaceRoot != "" {
		klippee.Workspace = filepath.Join(defaults.Klippee.WorkspaceRoot, name)
	}
	return klippee
}

func applyMigrationDefaults(
	migration inventory.Migration,
	defaults Defaults,
	name string,
) inventory.Migration {
	if migration.KlipperRef == "" {
		migration.KlipperRef = defaults.Migration.KlipperRef
	}
	if migration.MoonrakerRef == "" {
		migration.MoonrakerRef = defaults.Migration.MoonrakerRef
	}
	if migration.KatapultRef == "" {
		migration.KatapultRef = defaults.Migration.KatapultRef
	}
	if migration.ArtifactDir == "" {
		migration.ArtifactDir = defaults.Migration.ArtifactDir
	} else {
		migration.ArtifactDir = expandHome(migration.ArtifactDir)
	}
	switch {
	case migration.ConfigOutput != "":
		migration.ConfigOutput = expandHome(migration.ConfigOutput)
	case defaults.Migration.ConfigOutput != "":
		// Each printer renders into its own directory. render-config refuses to
		// overwrite an existing output, and two machines must never share a
		// configuration that carries a machine-specific Beacon serial.
		migration.ConfigOutput = filepath.Join(defaults.Migration.ConfigOutput, name)
	}
	if migration.ProfileDir == "" {
		migration.ProfileDir = defaults.Migration.ProfileDir
	} else {
		migration.ProfileDir = expandHome(migration.ProfileDir)
	}
	if migration.SDCard == "" {
		migration.SDCard = defaults.Migration.SDCard
	}
	// BeaconSerial has no site-wide default by design.
	return migration
}

func Home() string {
	if value := strings.TrimSpace(os.Getenv(EnvHome)); value != "" {
		return value
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "plab")
	}
	if dir, err := os.UserHomeDir(); err == nil {
		return filepath.Join(dir, ".plab")
	}
	return ".plab"
}

func DefaultPath() string { return filepath.Join(Home(), "config.yaml") }

func resolvePath(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("config file %s: %w", explicit, err)
		}
		return explicit, nil
	}
	if fromEnv := os.Getenv(EnvConfigPath); fromEnv != "" {
		if _, err := os.Stat(fromEnv); err != nil {
			return "", fmt.Errorf("config file %s: %w", fromEnv, err)
		}
		return fromEnv, nil
	}
	path := DefaultPath()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	return "", nil
}

func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	rest := strings.TrimLeft(path[1:], "/\\")
	if runtime.GOOS == "windows" {
		return filepath.Join(home, filepath.FromSlash(rest))
	}
	return filepath.Join(home, rest)
}

func defaultKnownHostsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}
