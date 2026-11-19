package inventory

import (
	"fmt"
	"strings"
	"time"
)

// Component records an identified physical part, including upgrades not yet fitted.
// ConfigSection optionally links it to evidence in the latest Klipper snapshot.
type Component struct {
	Type          string `yaml:"type" json:"type"`
	Model         string `yaml:"model" json:"model"`
	Manufacturer  string `yaml:"manufacturer,omitempty" json:"manufacturer,omitempty"`
	Status        string `yaml:"status,omitempty" json:"status,omitempty"`
	Notes         string `yaml:"notes,omitempty" json:"notes,omitempty"`
	Source        string `yaml:"source,omitempty" json:"source,omitempty"`
	ConfigSection string `yaml:"configSection,omitempty" json:"configSection,omitempty"`
}

func (c Component) EffectiveStatus() string {
	if c.Status == "" {
		return "installed"
	}
	return c.Status
}

// ComponentSnapshot records configuration evidence, not proof of physical identity.
// Captures replace this snapshot without changing manually recorded parts.
type ComponentSnapshot struct {
	CapturedAt string                         `yaml:"capturedAt" json:"capturedAt"`
	Source     string                         `yaml:"source" json:"source"`
	Components map[string]ConfiguredComponent `yaml:"components" json:"components"`
}

type ConfiguredComponent struct {
	Type     string            `yaml:"type" json:"type"`
	Settings map[string]string `yaml:"settings,omitempty" json:"settings,omitempty"`
}

func validateComponents(printer Printer) error {
	for id, component := range printer.Components {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) {
			return fmt.Errorf("invalid component ID %q", id)
		}
		if strings.TrimSpace(component.Type) == "" || strings.TrimSpace(component.Model) == "" {
			return fmt.Errorf("component %q requires type and model", id)
		}
		switch component.EffectiveStatus() {
		case "installed", "planned", "spare", "removed":
		default:
			return fmt.Errorf("component %q has invalid status %q", id, component.Status)
		}
	}
	if snapshot := printer.ComponentSnapshot; snapshot != nil {
		if _, err := time.Parse(time.RFC3339Nano, snapshot.CapturedAt); err != nil {
			return fmt.Errorf("component snapshot has invalid capturedAt: %w", err)
		}
		if snapshot.Source != "moonraker:configfile" || snapshot.Components == nil {
			return fmt.Errorf("component snapshot requires source moonraker:configfile and components")
		}
		for section, component := range snapshot.Components {
			if strings.TrimSpace(section) == "" || strings.TrimSpace(component.Type) == "" {
				return fmt.Errorf("component snapshot requires section names and component types")
			}
		}
	}
	return nil
}
