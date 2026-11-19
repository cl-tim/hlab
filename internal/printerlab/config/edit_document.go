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

	hostconfig "github.com/cl-tim/hlab/internal/config"
)

// editDocument changes the printer inventory in path, wherever in the file it
// lives: edit receives the mapping that holds defaults, printers and groups.
// A file that does not exist yet is created in hlab's unified layout.
func editDocument(path, backupLabel string, edit func(*yaml.Node) error) error {
	return editRoot(path, backupLabel, func(root *yaml.Node) error {
		inventory, _, err := section(root, true)
		if err != nil {
			return err
		}
		return edit(inventory)
	})
}

// editRoot applies edit to the document's root mapping and publishes the
// result only if it still loads, keeping an exact copy of the previous file.
//
// The file is shared with the host inventory once printers live in hlab's
// config, so the result must load for both readers: an edit to a printer may
// not leave `hlab run` with a config it cannot parse.
func editRoot(path, backupLabel string, edit func(*yaml.Node) error) error {
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
	if exists {
		if _, err := loadFile(path); err != nil {
			return err
		}
		original, err = os.ReadFile(path)
		if err != nil {
			return err
		}
	}
	doc, root, err := parseRoot(original)
	if err != nil {
		return err
	}
	if err := edit(root); err != nil {
		return err
	}
	layout, err := layoutOf(root)
	if err != nil {
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
	temp, err := os.CreateTemp(filepath.Dir(path), ".hlab-config-*.yaml")
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
	if _, err := loadFile(temp.Name()); err != nil {
		return err
	}
	if layout == LayoutUnified {
		if _, err := hostconfig.Load(temp.Name()); err != nil {
			return fmt.Errorf("the host inventory in %s would no longer load: %w", path, err)
		}
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
