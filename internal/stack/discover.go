package stack

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project is one Docker Compose project as the machine reports it.
type Project struct {
	Name        string `json:"Name"`
	Status      string `json:"Status"`
	ConfigFiles string `json:"ConfigFiles"`
}

// Files splits the comma-separated config file list compose reports.
func (p Project) Files() []string {
	var out []string
	for _, f := range strings.Split(p.ConfigFiles, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Dir is the directory the project's first config file lives in, which is what
// its relative bind mounts resolve against.
func (p Project) Dir() string {
	files := p.Files()
	if len(files) == 0 {
		return ""
	}
	if i := strings.LastIndex(files[0], "/"); i > 0 {
		return files[0][:i]
	}
	return ""
}

// Adopted reports whether hlab already owns this project.
func (p Project) Adopted() bool {
	return strings.HasPrefix(p.Dir(), RemoteDir+"/")
}

// ListScript reports the compose projects on a machine. It only reads.
const ListScript = `
set -eu
if ! command -v docker >/dev/null 2>&1; then
  echo '__M_NO_DOCKER__'
  exit 0
fi
docker compose ls --all --format json 2>/dev/null || echo '[]'
`

// BuildListScript renders the project listing script.
func BuildListScript() string {
	return strings.NewReplacer("__M_NO_DOCKER__", MarkerNoDocker).Replace(ListScript)
}

// ParseProjects reads the JSON array `docker compose ls` prints.
//
// Output is taken from the first line that looks like the array, because a
// sudo banner or a deprecation notice ahead of it is common and would
// otherwise make an ordinary machine look like it has no stacks.
func ParseProjects(out string) ([]Project, error) {
	line := ""
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "[") {
			line = l
			break
		}
	}
	if line == "" {
		return nil, nil
	}

	var projects []Project
	if err := json.Unmarshal([]byte(line), &projects); err != nil {
		return nil, fmt.Errorf("parse compose project list: %w", err)
	}

	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

// FileMarker delimits one file in the read script's output.
const FileMarker = "##HLAB#FILE#"

// BuildReadScript emits the contents of the given paths, each behind a marker.
//
// Paths come from docker's own project listing rather than from the operator,
// but they are quoted regardless: this runs as root, and a path is exactly the
// sort of value that stops being trustworthy the moment anything else can
// influence it.
func BuildReadScript(paths []string) string {
	var b strings.Builder
	b.WriteString("set -eu\n")
	for _, p := range paths {
		fmt.Fprintf(&b, "printf '%%s%%s\\n' %s %s\n",
			shellQuote(FileMarker), shellQuote(p))
		fmt.Fprintf(&b, "cat %s 2>/dev/null || true\n", shellQuote(p))
	}
	return b.String()
}

// ParseFiles splits the read script's output back into path/content pairs.
func ParseFiles(out string) map[string]string {
	files := map[string]string{}

	var path string
	var lines []string
	flush := func() {
		if path != "" {
			files[path] = strings.Join(lines, "\n")
		}
		lines = nil
	}

	// The output's own trailing newline is an artefact of the transport, not a
	// blank last line of the final file. Dropping it here rather than from each
	// value keeps the last file from being treated differently to the rest.
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if rest, ok := strings.CutPrefix(line, FileMarker); ok {
			flush()
			path = rest
			continue
		}
		if path == "" {
			continue
		}
		lines = append(lines, line)
	}
	flush()

	return files
}

// BindMount is a host path a stack mounts into a container.
type BindMount struct {
	Service string
	Source  string
}

// volumesFile is the subset of a compose document describing what it mounts.
type volumesFile struct {
	Services map[string]struct {
		Volumes []yaml.Node `yaml:"volumes"`
	} `yaml:"services"`
}

// RelativeBindMounts reports mounts whose source is relative to the compose
// file's own directory.
//
// This is what makes adopting a stack more than copying one file: a project
// that keeps its database in ./appdata carries that directory with it, and a
// compose file relocated without it comes up empty. Absolute sources
// (/var/run/docker.sock) and named volumes are not returned — they do not move.
func RelativeBindMounts(compose []byte) ([]BindMount, error) {
	var doc volumesFile
	if err := yaml.Unmarshal(compose, &doc); err != nil {
		return nil, fmt.Errorf("parse compose file: %w", err)
	}

	var out []BindMount
	for name, svc := range doc.Services {
		for _, node := range svc.Volumes {
			if src, ok := bindSourceOf(node); ok && strings.HasPrefix(src, ".") {
				out = append(out, BindMount{Service: name, Source: src})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Service < out[j].Service
	})
	return out, nil
}

// bindSourceOf extracts the host side of a compose volume entry, which may be
// a string ("./appdata:/appdata:ro") or the long mapping form.
func bindSourceOf(node yaml.Node) (string, bool) {
	if node.Kind == yaml.MappingNode {
		var m struct {
			Type   string `yaml:"type"`
			Source string `yaml:"source"`
		}
		if err := node.Decode(&m); err != nil || m.Source == "" {
			return "", false
		}
		if m.Type != "" && m.Type != "bind" {
			return "", false
		}
		return m.Source, true
	}

	var spec string
	if err := node.Decode(&spec); err != nil || spec == "" {
		return "", false
	}
	// "./appdata:/appdata:ro" — the host side is everything before the first
	// colon. A bare "/data" with no colon is an anonymous volume, not a bind.
	source, _, found := strings.Cut(spec, ":")
	if !found {
		return "", false
	}
	return source, true
}
