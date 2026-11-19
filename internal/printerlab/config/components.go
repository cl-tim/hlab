package config

import (
	"fmt"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"go.yaml.in/yaml/v3"
)

// EditComponent patches fields on an existing component, or creates one. IDs
// are literal YAML keys; dots and spaces never become nested field paths.
func EditComponent(path, printer, id string, fields map[string]string, remove bool) error {
	return editDocument(path, "components", func(root *yaml.Node) error {
		target := printerNode(root, printer)
		if target == nil {
			return fmt.Errorf("unknown printer %q", printer)
		}
		components := mappingChild(target, "components", yaml.MappingNode)
		if components.Kind != yaml.MappingNode {
			return fmt.Errorf("components must be a YAML mapping")
		}
		if remove {
			for i := 0; i+1 < len(components.Content); i += 2 {
				if components.Content[i].Value == id {
					components.Content = append(components.Content[:i], components.Content[i+2:]...)
					return nil
				}
			}
			return fmt.Errorf("printer %q has no component %q", printer, id)
		}
		node := mappingChild(components, id, yaml.MappingNode)
		for _, key := range scalarKeys(fields) {
			switch key {
			case "type", "model", "manufacturer", "status", "notes", "source", "configSection":
				setScalar(node, key, fields[key])
			default:
				return fmt.Errorf("unknown component field %q", key)
			}
		}
		return nil
	})
}

// SaveComponentSnapshots publishes a complete collection in one local update.
// Recorded identities are preserved, including planned and removed components.
func SaveComponentSnapshots(path string, snapshots map[string]inventory.ComponentSnapshot) error {
	return editDocument(path, "components", func(root *yaml.Node) error {
		for name, snapshot := range snapshots {
			target := printerNode(root, name)
			if target == nil {
				return fmt.Errorf("unknown printer %q", name)
			}
			node := mappingChild(target, "componentSnapshot", yaml.MappingNode)
			if err := node.Encode(snapshot); err != nil {
				return err
			}
		}
		return nil
	})
}
