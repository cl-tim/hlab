package config

import (
	"fmt"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
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

// EditComponentMaintenance changes only the maintenance fields of an existing part.
func EditComponentMaintenance(path, printer, id string, update func(inventory.Component) (inventory.Maintenance, error)) error {
	return editDocument(path, "maintenance", func(root *yaml.Node) error {
		target := printerNode(root, printer)
		if target == nil {
			return fmt.Errorf("unknown printer %q", printer)
		}
		components := mappingChild(target, "components", yaml.MappingNode)
		for i := 0; i+1 < len(components.Content); i += 2 {
			if components.Content[i].Value != id {
				continue
			}
			node := components.Content[i+1]
			if node.Kind != yaml.MappingNode {
				return fmt.Errorf("component %q must be a YAML mapping to edit maintenance", id)
			}
			var component inventory.Component
			if err := node.Decode(&component); err != nil {
				return err
			}
			maintenance, err := update(component)
			if err != nil {
				return err
			}
			var encoded yaml.Node
			if err := encoded.Encode(maintenance); err != nil {
				return err
			}
			current := mappingChild(node, "maintenance", yaml.MappingNode)
			if current.Kind != yaml.MappingNode {
				current.Kind, current.Tag, current.Value, current.Alias = yaml.MappingNode, "!!map", "", nil
			}
			for j := 0; j+1 < len(encoded.Content); j += 2 {
				field := mappingChild(current, encoded.Content[j].Value, yaml.ScalarNode)
				value := encoded.Content[j+1]
				field.Kind, field.Tag, field.Value = value.Kind, value.Tag, value.Value
			}
			return nil
		}
		return fmt.Errorf("printer %q has no component %q", printer, id)
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
