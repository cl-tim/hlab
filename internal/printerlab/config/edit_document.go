package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

func editDocument(path, backupLabel string, edit func(*yaml.Node) error) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if exists && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("inventory must be a regular file: %s", path)
	}
	var original []byte
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	if exists {
		if _, err := Load(path); err != nil {
			return err
		}
		original, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(original, doc); err != nil {
			return err
		}
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("inventory must contain one YAML mapping")
	}
	if err := edit(doc.Content[0]); err != nil {
		return err
	}
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if bytes.Equal(original, buffer.Bytes()) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".plab-inventory-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(buffer.Bytes()); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if _, err := Load(temp.Name()); err != nil {
		return err
	}
	// Detect edits made during the prompts instead of discarding them.
	current, readErr := os.ReadFile(path)
	if exists && (readErr != nil || !bytes.Equal(current, original)) || !exists && !os.IsNotExist(readErr) {
		return fmt.Errorf("inventory changed during update; retry")
	}
	if exists {
		backup := path + ".before-" + backupLabel + "-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(original)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return os.Rename(temp.Name(), path)
}

func printerNode(root *yaml.Node, name string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "printers" {
			continue
		}
		for _, item := range root.Content[i+1].Content {
			for j := 0; j+1 < len(item.Content); j += 2 {
				if item.Content[j].Value == "name" && item.Content[j+1].Value == name {
					return item
				}
			}
		}
	}
	return nil
}

func scalarKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mappingChild(node *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	tag := "!!map"
	if kind == yaml.SequenceNode {
		tag = "!!seq"
	}
	child := &yaml.Node{Kind: kind, Tag: tag}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
	return child
}

func setScalar(node *yaml.Node, path, value string) {
	keys := strings.Split(path, ".")
	for _, key := range keys[:len(keys)-1] {
		node = mappingChild(node, key, yaml.MappingNode)
	}
	child := mappingChild(node, keys[len(keys)-1], yaml.ScalarNode)
	child.Kind, child.Tag, child.Value, child.Content = yaml.ScalarNode, "!!str", value, nil
}
