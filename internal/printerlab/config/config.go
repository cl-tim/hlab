// Package config loads the printer inventory and defaults.
//
// Printers live in hlab's own config.yaml, under `printerlab:`, so one file
// describes everything on the network. A file holding only a printer
// inventory — the layout from before the merge — is still read and edited
// when it is the one named.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/cl-tim/hlab/internal/cfgkit"
	hostconfig "github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

const (
	EnvConfigPath = "PLAB_CONFIG"
	EnvHome       = "PLAB_HOME"
)

// Duration is the YAML duration shared with the host config: "10s", or a bare
// integer of seconds.
type Duration = cfgkit.Duration

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
	// Path is the file the inventory was read from, or "" if none exists.
	Path string
	// Layout is how that file is laid out, and so how it must be edited.
	Layout Layout
	// Present reports whether the file holds a printer inventory at all:
	// hlab's config before any printer has been added does not.
	Present bool
	// Legacy is set when the inventory came from the pre-merge standalone
	// location because hlab's config has no printer section yet.
	Legacy bool

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
		KnownHostsFile: cfgkit.KnownHostsPath(),
		Klippee:        KlippeeDefaults{Executable: "klippee"},
	}
}

// Load reads the printer inventory.
//
// An explicit path, or one named by $PLAB_CONFIG, is read whichever layout it
// has. Otherwise the inventory is the `printerlab:` section of hlab's own
// config. A standalone inventory left at the pre-merge location is still read
// when hlab's config has no such section, and Legacy is set so the caller can
// say how to fold it in.
func Load(explicit string) (*Config, error) {
	named, err := cfgkit.ResolvePath(explicit, EnvConfigPath, nil)
	if err != nil {
		return nil, err
	}
	if named != "" {
		return loadFile(named)
	}

	unified, err := cfgkit.ResolvePath("", hostconfig.EnvConfigPath, hostconfig.SearchPaths())
	if err != nil {
		return nil, err
	}
	cfg, err := loadFile(unified)
	if err != nil || cfg.Present {
		return cfg, err
	}
	if _, statErr := os.Stat(LegacyPath()); statErr == nil {
		legacy, err := loadFile(LegacyPath())
		if err != nil {
			return nil, err
		}
		legacy.Legacy = true
		return legacy, nil
	}
	return cfg, nil
}

// unifiedFile reads the printer section out of hlab's config and lets every
// other key through untouched: those belong to the host loader, which applies
// its own strictness to them.
type unifiedFile struct {
	Printerlab *File                `yaml:"printerlab"`
	Other      map[string]yaml.Node `yaml:",inline"`
}

// loadFile reads one file in whichever layout it has. An empty path yields
// built-in defaults and no printers.
func loadFile(path string) (*Config, error) {
	cfg := &Config{Defaults: builtinDefaults()}
	var file File
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		_, root, err := parseRoot(raw)
		if err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		cfg.Layout, err = layoutOf(root)
		if err != nil {
			return nil, fmt.Errorf("config %s: %w", path, err)
		}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if cfg.Layout == LayoutStandalone {
			err = decoder.Decode(&file)
			cfg.Present = true
		} else {
			var whole unifiedFile
			err = decoder.Decode(&whole)
			if whole.Printerlab != nil {
				file, cfg.Present = *whole.Printerlab, true
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		cfg.Path = path
	}
	cfg.Defaults = deriveMigrationPaths(mergeDefaults(cfg.Defaults, file.Defaults))
	printers := make([]inventory.Printer, len(file.Printers))
	for i, printer := range file.Printers {
		printers[i] = applyDefaults(printer, cfg.Defaults)
	}
	var err error
	cfg.Inventory, err = inventory.New(printers, file.Groups)
	if err != nil {
		return nil, fmt.Errorf("invalid printer inventory: %w", err)
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
		base.IdentityFile = cfgkit.ExpandHome(override.IdentityFile)
	}
	if override.KnownHostsFile != "" {
		base.KnownHostsFile = cfgkit.ExpandHome(override.KnownHostsFile)
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
		base.FirmwareRepo = cfgkit.ExpandHome(override.FirmwareRepo)
	}
	base.Migration = mergeMigration(base.Migration, override.Migration)
	base.Klippee = mergeKlippee(base.Klippee, override.Klippee)
	return base
}

func mergeKlippee(base, override KlippeeDefaults) KlippeeDefaults {
	if override.Executable != "" {
		base.Executable = cfgkit.ExpandHome(override.Executable)
	}
	if override.WorkspaceRoot != "" {
		base.WorkspaceRoot = cfgkit.ExpandHome(override.WorkspaceRoot)
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
		base.KlipperDir = cfgkit.ExpandHome(override.KlipperDir)
	}
	if override.KatapultDir != "" {
		base.KatapultDir = cfgkit.ExpandHome(override.KatapultDir)
	}
	if override.ArtifactDir != "" {
		base.ArtifactDir = cfgkit.ExpandHome(override.ArtifactDir)
	}
	if override.ConfigOutput != "" {
		base.ConfigOutput = cfgkit.ExpandHome(override.ConfigOutput)
	}
	if override.ProfileDir != "" {
		base.ProfileDir = cfgkit.ExpandHome(override.ProfileDir)
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
		printer.IdentityFile = cfgkit.ExpandHome(printer.IdentityFile)
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
		klippee.Workspace = cfgkit.ExpandHome(klippee.Workspace)
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
		migration.ArtifactDir = cfgkit.ExpandHome(migration.ArtifactDir)
	}
	switch {
	case migration.ConfigOutput != "":
		migration.ConfigOutput = cfgkit.ExpandHome(migration.ConfigOutput)
	case defaults.Migration.ConfigOutput != "":
		// Each printer renders into its own directory. render-config refuses to
		// overwrite an existing output, and two machines must never share a
		// configuration that carries a machine-specific Beacon serial.
		migration.ConfigOutput = filepath.Join(defaults.Migration.ConfigOutput, name)
	}
	if migration.ProfileDir == "" {
		migration.ProfileDir = defaults.Migration.ProfileDir
	} else {
		migration.ProfileDir = cfgkit.ExpandHome(migration.ProfileDir)
	}
	if migration.SDCard == "" {
		migration.SDCard = defaults.Migration.SDCard
	}
	// BeaconSerial has no site-wide default by design.
	return migration
}

// Home is where printer journals, backups and build artifacts live. It
// predates the merge into hlab and is kept so existing data is read in place.
// The inventory itself now lives in hlab's config; see Load.
func Home() string { return cfgkit.Home(EnvHome, "plab") }

// LegacyPath is where the standalone printer inventory lived before printers
// moved into hlab's config. It is read as a fallback and never created.
func LegacyPath() string { return filepath.Join(Home(), "config.yaml") }
