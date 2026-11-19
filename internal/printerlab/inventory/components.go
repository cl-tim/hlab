package inventory

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Component records an identified physical part, including upgrades not yet fitted.
// ConfigSection optionally links it to evidence in the latest Klipper snapshot.
type Component struct {
	Type          string       `yaml:"type" json:"type"`
	Model         string       `yaml:"model" json:"model"`
	Manufacturer  string       `yaml:"manufacturer,omitempty" json:"manufacturer,omitempty"`
	Status        string       `yaml:"status,omitempty" json:"status,omitempty"`
	Notes         string       `yaml:"notes,omitempty" json:"notes,omitempty"`
	Source        string       `yaml:"source,omitempty" json:"source,omitempty"`
	ConfigSection string       `yaml:"configSection,omitempty" json:"configSection,omitempty"`
	Maintenance   *Maintenance `yaml:"maintenance,omitempty" json:"maintenance,omitempty"`
}

// Maintenance stores a service interval and the print counter at last service.
type Maintenance struct {
	IntervalHours        float64    `yaml:"intervalHours" json:"intervalHours"`
	LastServicedAt       *time.Time `yaml:"lastServicedAt,omitempty" json:"lastServicedAt,omitempty"`
	BaselinePrintSeconds *float64   `yaml:"baselinePrintSeconds,omitempty" json:"baselinePrintSeconds,omitempty"`
}

func (m Maintenance) validate() error {
	if math.IsNaN(m.IntervalHours) || math.IsInf(m.IntervalHours, 0) || m.IntervalHours <= 0 {
		return fmt.Errorf("maintenance intervalHours must be a positive finite number")
	}
	if (m.LastServicedAt == nil) != (m.BaselinePrintSeconds == nil) {
		return fmt.Errorf("maintenance lastServicedAt and baselinePrintSeconds must be set together")
	}
	if m.LastServicedAt != nil && m.LastServicedAt.IsZero() {
		return fmt.Errorf("maintenance lastServicedAt must be a valid service timestamp")
	}
	if seconds := m.BaselinePrintSeconds; seconds != nil && (math.IsNaN(*seconds) || math.IsInf(*seconds, 0) || *seconds < 0) {
		return fmt.Errorf("maintenance baselinePrintSeconds must be finite and non-negative")
	}
	return nil
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
		if component.Maintenance != nil {
			if err := component.Maintenance.validate(); err != nil {
				return fmt.Errorf("component %q: %w", id, err)
			}
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
