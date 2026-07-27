// Package stack deploys Docker Compose stacks to managed machines.
package stack

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// RemoteDir is where stacks live on a managed machine.
const RemoteDir = "/opt/hlab/stacks"

// LocalDirName is the directory under hlab's home where pulled stacks are
// kept. Never the working directory: a compose file routinely carries secrets
// and hlab is routinely run from inside a checkout.
const LocalDirName = "stacks"

// Markers the remote script prints so the outcome can be reported precisely.
const (
	MarkerNoDocker = "hlab:no-docker"
	MarkerDeployed = "hlab:deployed"
)

// PublishedPort is a host port a compose file would bind.
type PublishedPort struct {
	Port    int
	Service string
}

// composeFile is the subset of a compose document that matters for pre-flight.
type composeFile struct {
	Services map[string]struct {
		Ports []yaml.Node `yaml:"ports"`
	} `yaml:"services"`
}

// PublishedPorts reports the host ports a compose file would bind.
//
// This exists so a deployment can be checked against what a machine is already
// listening on: a stack that silently fails to bind because something else
// holds the port is worse than one that refuses up front.
func PublishedPorts(compose []byte) ([]PublishedPort, error) {
	var doc composeFile
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, fmt.Errorf("parse compose file: %w", err)
	}

	var out []PublishedPort
	for name, svc := range doc.Services {
		for _, node := range svc.Ports {
			for _, port := range hostPortsOf(node) {
				out = append(out, PublishedPort{Port: port, Service: name})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Service < out[j].Service
	})
	return out, nil
}

// hostPortsOf extracts the host side of a compose port entry, which may be a
// string ("53:53/udp", "0.0.0.0:80:80") or the long mapping form.
func hostPortsOf(node yaml.Node) []int {
	// Long form: { target: 53, published: 53, protocol: udp }
	if node.Kind == yaml.MappingNode {
		var m struct {
			Published yaml.Node `yaml:"published"`
		}
		if err := node.Decode(&m); err == nil {
			if p, ok := parsePort(m.Published.Value); ok {
				return []int{p}
			}
		}
		return nil
	}

	var spec string
	if err := node.Decode(&spec); err != nil {
		return nil
	}

	// Strip any protocol suffix, then take the host side of host:container.
	spec, _, _ = strings.Cut(spec, "/")
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		// "8080" alone publishes a random host port, so nothing is claimed.
		return nil
	case 2:
		// "80:80"
		if p, ok := parsePort(parts[0]); ok {
			return []int{p}
		}
	default:
		// "0.0.0.0:80:80" — the host port is second from the end.
		if p, ok := parsePort(parts[len(parts)-2]); ok {
			return []int{p}
		}
	}
	return nil
}

// parsePort accepts a single port, and expands nothing: a range like "80-90"
// is deliberately not treated as a claim on 80, since guessing wrong here
// would produce a false conflict.
func parsePort(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "-") {
		return 0, false
	}
	p, err := strconv.Atoi(s)
	if err != nil || p <= 0 || p > 65535 {
		return 0, false
	}
	return p, true
}

// deployTemplate writes a compose file and brings the stack up.
//
// The file is written first and compared: an unchanged stack still runs
// `compose up -d`, which is a no-op when nothing has changed, so the command is
// safe to repeat.
const deployTemplate = `
set -eu

if ! command -v docker >/dev/null 2>&1; then
  echo '__M_NO_DOCKER__'
  exit 0
fi
if ! docker compose version >/dev/null 2>&1; then
  echo 'hlab:error docker compose plugin is not installed' >&2
  exit 11
fi

DIR=__DIR__
mkdir -p "$DIR"
chmod 0750 "$DIR"

printf '%s' __COMPOSE__ > "$DIR/compose.yaml"
chmod 0640 "$DIR/compose.yaml"
__ASSETS__
cd "$DIR"
docker compose up -d --remove-orphans

echo '__M_DEPLOYED__'
docker compose ps --format '{{.Name}}|{{.Service}}|{{.State}}' 2>/dev/null || true
`

// Asset is a file deployed alongside the compose file, for the configuration a
// stack mounts rather than embeds — a Caddyfile, a resolver config, a rules
// file. Compose can carry environment variables, but not a multi-line config,
// so without these a stack is only half reproducible.
type Asset struct {
	// Name is the file's name inside the stack directory, with no path.
	Name string
	// Content is written verbatim.
	Content []byte
}

// BuildDeployScript renders the script that installs and starts a stack,
// writing any assets alongside the compose file before bringing it up.
func BuildDeployScript(name string, compose []byte, assets []Asset) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	if len(compose) == 0 {
		return "", fmt.Errorf("compose file is empty")
	}

	var b strings.Builder
	seen := map[string]struct{}{}
	for _, asset := range assets {
		if err := ValidateAssetName(asset.Name); err != nil {
			return "", err
		}
		if _, dup := seen[asset.Name]; dup {
			return "", fmt.Errorf("two assets are both named %q", asset.Name)
		}
		seen[asset.Name] = struct{}{}

		fmt.Fprintf(&b, "printf '%%s' %s > \"$DIR\"/%s\n",
			shellQuote(string(asset.Content)), shellQuote(asset.Name))
		fmt.Fprintf(&b, "chmod 0640 \"$DIR\"/%s\n", shellQuote(asset.Name))
	}

	// Assets are substituted in the same pass as everything else, and
	// NewReplacer does not rescan what it has written, so content that happens
	// to contain a marker is left alone.
	return strings.NewReplacer(
		"__DIR__", shellQuote(RemoteDir+"/"+name),
		"__COMPOSE__", shellQuote(string(compose)),
		"__ASSETS__", b.String(),
		"__M_NO_DOCKER__", MarkerNoDocker,
		"__M_DEPLOYED__", MarkerDeployed,
	).Replace(deployTemplate), nil
}

// ValidateAssetName rejects anything that would write outside the stack
// directory. The name becomes a path on a remote machine running the script as
// root, so this is a security control, not a nicety.
func ValidateAssetName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("an asset needs a name")
	case name == "." || name == "..":
		return fmt.Errorf("invalid asset name %q", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("invalid asset name %q: it must be a file name, not a path", name)
	case strings.HasPrefix(name, "-"):
		// Otherwise it reads as an option to whatever later touches it.
		return fmt.Errorf("invalid asset name %q: it must not start with a dash", name)
	case name == "compose.yaml":
		return fmt.Errorf("an asset cannot be named compose.yaml; that is the stack itself")
	case strings.ContainsAny(name, "\x00\n"):
		return fmt.Errorf("invalid asset name %q", name)
	}
	return nil
}

// ValidateName rejects stack names that would escape the stack directory.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("a stack needs a name")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return fmt.Errorf(
				"invalid stack name %q: use lowercase letters, digits, dash and underscore", name)
		}
	}
	return nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ContainerState is one container's state as compose reports it.
type ContainerState struct {
	Name    string
	Service string
	State   string
}

// ParseContainerStates reads the `compose ps` lines the script emits.
func ParseContainerStates(out string) []ContainerState {
	var states []ContainerState
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		states = append(states, ContainerState{
			Name: parts[0], Service: parts[1], State: parts[2],
		})
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Name < states[j].Name })
	return states
}
