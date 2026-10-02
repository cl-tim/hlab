package config

import (
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration wraps time.Duration so the config file can express timeouts in the
// usual Go notation ("10s", "2m") instead of raw nanoseconds.
type Duration time.Duration

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML accepts either a duration string ("10s") or a plain integer,
// which is interpreted as seconds.
//
// The node tag is checked rather than attempting a string decode first: yaml
// happily decodes a bare `30` into the string "30", which would make the
// integer form unreachable.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a string like \"10s\" or a number of seconds", value.Line)
	}

	if value.Tag == "!!int" {
		var secs int64
		if err := value.Decode(&secs); err != nil {
			return fmt.Errorf("line %d: %w", value.Line, err)
		}
		*d = Duration(time.Duration(secs) * time.Second)
		return nil
	}

	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration must be a string like \"10s\" or a number of seconds", value.Line)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q: %w", value.Line, s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML renders the duration back out in string form.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }
