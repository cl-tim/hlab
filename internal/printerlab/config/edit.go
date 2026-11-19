package config

import (
	"fmt"
	"go.yaml.in/yaml/v3"
)

// Edit updates only the supplied scalar fields, preserving other printers,
// groups, comments and settings. It validates the result before publishing and
// retains an exact copy of the previous file for recovery.
func Edit(path, printer string, defaults, fields map[string]string) error {
	return editDocument(path, "firmware", func(root *yaml.Node) error {
		if len(defaults) > 0 {
			node := mappingChild(root, "defaults", yaml.MappingNode)
			for _, key := range scalarKeys(defaults) {
				setScalar(node, key, defaults[key])
			}
		}
		printers := mappingChild(root, "printers", yaml.SequenceNode)
		if printers.Kind != yaml.SequenceNode {
			return fmt.Errorf("printers must be a YAML sequence")
		}
		target := printerNode(root, printer)
		if target == nil {
			target = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			printers.Content = append(printers.Content, target)
			setScalar(target, "name", printer)
		}
		for _, key := range scalarKeys(fields) {
			setScalar(target, key, fields[key])
		}
		return nil
	})
}
