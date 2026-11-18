package facts

import (
	"slices"
	"strings"
	"time"
)

// Change records a difference between two complete collections.
type Change struct {
	At     time.Time `json:"at" yaml:"at"`
	Field  string    `json:"field" yaml:"field"`
	Before string    `json:"before" yaml:"before"`
	After  string    `json:"after" yaml:"after"`
}

const historyLimit = 100

func observedState(f Facts) map[string]string {
	if f.Partial {
		return nil
	}
	var addresses, services []string
	if f.Network != nil {
		for _, iface := range f.Network.Interfaces {
			addresses = append(addresses, iface.Addresses...)
		}
	}
	for _, svc := range f.Services {
		services = append(services, svc.Name)
	}
	slices.Sort(addresses)
	slices.Sort(services)
	return map[string]string{
		"kernel":    f.Kernel,
		"addresses": strings.Join(slices.Compact(addresses), ", "),
		"services":  strings.Join(slices.Compact(services), ", "),
	}
}

func recordChanges(previous, current Snapshot) Snapshot {
	current.History = previous.History
	if current.Observed == nil {
		current.Observed = previous.Observed
		return current
	}
	if previous.Observed == nil {
		return current
	}
	for _, field := range []string{"kernel", "addresses", "services"} {
		before, after := previous.Observed[field], current.Observed[field]
		if before != after {
			current.History = append(current.History, Change{current.CollectedAt, field, before, after})
		}
	}
	if extra := len(current.History) - historyLimit; extra > 0 {
		current.History = current.History[extra:]
	}
	return current
}
