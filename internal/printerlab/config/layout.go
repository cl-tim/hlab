package config

import (
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"

	hostconfig "github.com/cl-tim/hlab/internal/config"
)

// SectionKey is where the printer inventory lives inside hlab's config.yaml.
// What sits beneath it — defaults, printers, groups — is exactly what a
// standalone printer inventory holds at its top level, so moving one into the
// other is an indentation change and nothing else.
const SectionKey = "printerlab"

// Layout says where in a file the printer inventory is.
type Layout int

const (
	// LayoutUnified is hlab's config.yaml: hosts at the top level, printers
	// under SectionKey.
	LayoutUnified Layout = iota
	// LayoutStandalone is a file holding only a printer inventory, as the
	// tool wrote before it was folded into hlab. Still read, and still
	// written when it is the file the operator pointed at.
	LayoutStandalone
)

// hostKeys are top-level keys only hlab's host config has. `defaults` and
// `groups` are deliberately absent: both layouts use them.
var hostKeys = []string{"hosts", "ignored", "endpoints", "discovered"}

func hasKey(root *yaml.Node, key string) bool {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			return true
		}
	}
	return false
}

func hasHostKeys(root *yaml.Node) bool {
	for _, key := range hostKeys {
		if hasKey(root, key) {
			return true
		}
	}
	return false
}

// layoutOf decides how a document's root mapping is laid out.
//
// A file is standalone when it lists printers at the top level, or when it
// has content and nothing marks it as hlab's config — a standalone inventory
// that so far sets only defaults. An empty document is unified, so a file
// created from nothing is created in the layout everything else uses.
//
// Printers listed at the top level beside hosts is neither, and is refused
// rather than guessed at: the host loader would reject the same file for a
// reason that does not mention printers at all.
func layoutOf(root *yaml.Node) (Layout, error) {
	switch {
	case hasKey(root, "printers") && (hasHostKeys(root) || hasKey(root, SectionKey)):
		return 0, fmt.Errorf("top-level `printers:` beside host settings; move defaults, printers and groups for printers under `%s:`", SectionKey)
	case hasKey(root, "printers"):
		return LayoutStandalone, nil
	case hasKey(root, SectionKey) || hasHostKeys(root) || len(root.Content) == 0:
		return LayoutUnified, nil
	default:
		return LayoutStandalone, nil
	}
}

// section returns the mapping holding defaults, printers and groups: the root
// itself for a standalone file, the SectionKey child for a unified one. It is
// nil when a unified file has no printer section and create is false.
func section(root *yaml.Node, create bool) (*yaml.Node, Layout, error) {
	layout, err := layoutOf(root)
	if err != nil {
		return nil, 0, err
	}
	if layout == LayoutStandalone {
		return root, layout, nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != SectionKey {
			continue
		}
		child := root.Content[i+1]
		if child.Kind == yaml.ScalarNode && child.Tag == "!!null" {
			// `printerlab:` with nothing under it yet.
			child.Kind, child.Tag, child.Value = yaml.MappingNode, "!!map", ""
		}
		if child.Kind != yaml.MappingNode {
			return nil, 0, fmt.Errorf("`%s:` must be a YAML mapping", SectionKey)
		}
		return child, layout, nil
	}
	if !create {
		return nil, layout, nil
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: SectionKey}, child)
	return child, layout, nil
}

// parseRoot parses a document and returns it with its root mapping. An empty
// or comment-only document yields an empty mapping.
func parseRoot(raw []byte) (*yaml.Node, *yaml.Node, error) {
	doc := &yaml.Node{}
	if err := yaml.Unmarshal(raw, doc); err != nil {
		return nil, nil, err
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("config must contain one YAML mapping")
	}
	return doc, doc.Content[0], nil
}

// UnifiedPath is hlab's config file: the one named by $HLAB_CONFIG, else the
// first default location that exists, else where a new one would be created.
// It does not check that an environment path exists; Load does.
func UnifiedPath() string {
	if env := os.Getenv(hostconfig.EnvConfigPath); env != "" {
		return env
	}
	for _, candidate := range hostconfig.SearchPaths() {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return hostconfig.DefaultPath()
}

// WritePath is the file a command should create or change the printer
// inventory in: the explicit path, else $PLAB_CONFIG, else wherever the
// inventory is read from today — which is the old standalone file for as long
// as it has not been migrated — else hlab's config.
func WritePath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if env := os.Getenv(EnvConfigPath); env != "" {
		return env
	}
	if cfg, err := Load(""); err == nil && cfg.Present {
		return cfg.Path
	}
	return UnifiedPath()
}
