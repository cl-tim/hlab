package config

import (
	"fmt"
	"os"
	"slices"

	"go.yaml.in/yaml/v3"
)

// RememberIgnored records an SSH endpoint that should be skipped during onboarding.
func RememberIgnored(path, endpoint string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := parseBytes(raw)
	if err != nil {
		return err
	}
	if slices.Contains(file.Ignored, endpoint) {
		return nil
	}
	doc, root, err := documentRoot(raw)
	if err != nil {
		return err
	}
	seq := findOrCreateSequence(root, "ignored")
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: endpoint})
	updated, err := marshalDocument(doc)
	if err != nil {
		return err
	}
	if len(raw) > 0 {
		if err := os.WriteFile(path+".bak", raw, 0600); err != nil {
			return fmt.Errorf("back up config: %w", err)
		}
	}
	return writeAtomic(path, updated)
}
