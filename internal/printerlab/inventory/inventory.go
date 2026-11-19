// Package inventory models printers and selector expressions.
package inventory

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/cl-tim/hlab/internal/selector"
)

const (
	FirmwareAuto     = "auto"
	FirmwareStock    = "stock"
	FirmwareMainline = "mainline"
)

type Printer struct {
	Name        string   `yaml:"name" json:"name"`
	Address     string   `yaml:"address" json:"address"`
	Model       string   `yaml:"model,omitempty" json:"model,omitempty"`
	Firmware    string   `yaml:"firmware,omitempty" json:"firmware,omitempty"`
	Tags        []string `yaml:"tags,omitempty" json:"tags,omitempty"`
	Description string   `yaml:"description,omitempty" json:"description,omitempty"`
	// User is the SSH account. PrinterUser owns the Klipper installation.
	// A management account uses sudo to operate on that installation.
	User         string `yaml:"user,omitempty" json:"user,omitempty"`
	PrinterUser  string `yaml:"printerUser,omitempty" json:"printerUser,omitempty"`
	Port         int    `yaml:"port,omitempty" json:"port,omitempty"`
	IdentityFile string `yaml:"identityFile,omitempty" json:"identityFile,omitempty"`
	// SSHPasswordEnv names the environment variable holding the bootstrap password.
	// Remove this setting after setup-ssh installs the IdentityFile key.
	SSHPasswordEnv string `yaml:"sshPasswordEnv,omitempty" json:"sshPasswordEnv,omitempty"`
	MoonrakerURL   string `yaml:"moonrakerURL,omitempty" json:"moonrakerURL,omitempty"`
	MoonrakerPort  int    `yaml:"moonrakerPort,omitempty" json:"moonrakerPort,omitempty"`
	APIKeyEnv      string `yaml:"apiKeyEnv,omitempty" json:"apiKeyEnv,omitempty"`
	Profile        string `yaml:"profile,omitempty" json:"profile,omitempty"`

	Migration         Migration            `yaml:"migration,omitempty" json:"migration,omitempty"`
	Klippee           Klippee              `yaml:"klippee,omitempty" json:"klippee,omitempty"`
	Components        map[string]Component `yaml:"components,omitempty" json:"components,omitempty"`
	ComponentSnapshot *ComponentSnapshot   `yaml:"componentSnapshot,omitempty" json:"componentSnapshot,omitempty"`
}

// Migration holds per-printer firmware settings.
// BeaconSerial identifies that printer's probe and is accepted only here.
type Migration struct {
	BeaconRef    string `yaml:"beaconRef,omitempty" json:"beaconRef,omitempty"`
	KlipperRef   string `yaml:"klipperRef,omitempty" json:"klipperRef,omitempty"`
	MoonrakerRef string `yaml:"moonrakerRef,omitempty" json:"moonrakerRef,omitempty"`
	KatapultRef  string `yaml:"katapultRef,omitempty" json:"katapultRef,omitempty"`
	BeaconSerial string `yaml:"beaconSerial,omitempty" json:"beaconSerial,omitempty"`
	// ArtifactDir holds built MCU images, normally under PLAB_HOME/artifacts.
	ArtifactDir string `yaml:"artifactDir,omitempty" json:"artifactDir,omitempty"`
	// ConfigOutput is where a rendered configuration is written. The tool protects managed
	// replacement with ownership metadata.
	ConfigOutput string `yaml:"configOutput,omitempty" json:"configOutput,omitempty"`
	// ProfileDir selects a custom runtime profile directory. Omit it
	// to use the profile embedded in plab.
	ProfileDir string `yaml:"profileDir,omitempty" json:"profileDir,omitempty"`
	// SDCard is the FAT32 microSD root that receives qd_mcu.bin.
	SDCard string `yaml:"sdcard,omitempty" json:"sdcard,omitempty"`
}

// Klippee selects the local desktop configuration workspace.
type Klippee struct {
	Workspace string `yaml:"workspace,omitempty" json:"workspace,omitempty"`
}

func (p Printer) HasTag(tag string) bool {
	for _, candidate := range p.Tags {
		if strings.EqualFold(candidate, tag) {
			return true
		}
	}
	return false
}

type Inventory struct {
	printers []Printer
	byName   map[string]Printer
	groups   map[string][]string
}

func New(printers []Printer, groups map[string][]string) (*Inventory, error) {
	inv := &Inventory{
		printers: make([]Printer, 0, len(printers)),
		byName:   make(map[string]Printer, len(printers)),
		groups:   make(map[string][]string, len(groups)),
	}
	workspaces := make(map[string]string, len(printers))
	for _, printer := range printers {
		if printer.Name == "" {
			return nil, fmt.Errorf("printer with address %q has no name", printer.Address)
		}
		if printer.Address == "" {
			return nil, fmt.Errorf("printer %q has no address", printer.Name)
		}
		if _, exists := inv.byName[printer.Name]; exists {
			return nil, fmt.Errorf("duplicate printer name %q", printer.Name)
		}
		switch printer.Firmware {
		case FirmwareAuto, FirmwareStock, FirmwareMainline:
		default:
			return nil, fmt.Errorf("printer %q has invalid firmware %q", printer.Name, printer.Firmware)
		}
		if printer.Klippee.Workspace != "" {
			workspace := filepath.Clean(printer.Klippee.Workspace)
			if absolute, err := filepath.Abs(workspace); err == nil {
				workspace = filepath.Clean(absolute)
			}
			key := workspace
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			if existing, ok := workspaces[key]; ok {
				return nil, fmt.Errorf(
					"printers %q and %q share Klippee workspace %q",
					existing, printer.Name, workspace,
				)
			}
			workspaces[key] = printer.Name
		}
		if err := validateComponents(printer); err != nil {
			return nil, fmt.Errorf("printer %q: %w", printer.Name, err)
		}
		inv.printers = append(inv.printers, printer)
		inv.byName[printer.Name] = printer
	}
	for name, members := range groups {
		if name == "all" {
			return nil, fmt.Errorf("group name %q is reserved", name)
		}
		for _, member := range members {
			if _, exists := inv.byName[member]; !exists {
				return nil, fmt.Errorf("group %q refers to unknown printer %q", name, member)
			}
		}
		inv.groups[name] = append([]string(nil), members...)
	}
	sort.Slice(inv.printers, func(i, j int) bool {
		return inv.printers[i].Name < inv.printers[j].Name
	})
	return inv, nil
}

func (i *Inventory) Printers() []Printer {
	return append([]Printer(nil), i.printers...)
}

func (i *Inventory) Printer(name string) (Printer, bool) {
	printer, ok := i.byName[name]
	return printer, ok
}

// Select resolves a selector expression. The expression language lives in
// internal/selector, shared with the host inventory.
func (i *Inventory) Select(expression string) ([]Printer, error) {
	return selector.Set[Printer]{
		Noun:   "printer",
		Items:  i.printers,
		Groups: i.groups,
		Name:   func(p Printer) string { return p.Name },
		HasTag: Printer.HasTag,
	}.Select(expression)
}
