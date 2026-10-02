package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/cl-tim/homelab-cli/internal/inventory"
)

// AddHost records a host in the config, creating the file if it does not exist.
//
// It is an upsert keyed on address and port: a machine a scan already recorded
// as unmanaged is promoted in place rather than duplicated, which is exactly
// what enrolment needs. Observed fields and the original FirstSeen survive the
// promotion.
//
// The existing document is edited through a yaml.Node tree rather than
// re-marshalled from structs, so comments and unrelated settings survive. The
// previous contents are copied to <path>.bak first, and the write is atomic.
func AddHost(path string, host inventory.Host) error {
	if path == "" {
		return errors.New("no config path given")
	}
	if host.Name == "" {
		return errors.New("host has no name")
	}

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
	case os.IsNotExist(err):
		raw = nil
	default:
		return fmt.Errorf("read %s: %w", path, err)
	}

	updated, err := addHostToDocument(raw, host)
	if err != nil {
		return err
	}

	if len(raw) > 0 {
		if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
			return fmt.Errorf("write backup %s.bak: %w", path, err)
		}
	}

	return writeAtomic(path, updated)
}

// addHostToDocument performs the edit on the YAML document itself.
func addHostToDocument(raw []byte, host inventory.Host) ([]byte, error) {
	file, err := parseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse existing config: %w", err)
	}

	existing := migrateLegacyDiscovered(file)
	merged, err := upsertHost(existing, host)
	if err != nil {
		return nil, err
	}

	return replaceHostsSection(raw, merged)
}

// upsertHost replaces the record for the same machine, or appends a new one.
func upsertHost(hosts []inventory.Host, host inventory.Host) ([]inventory.Host, error) {
	if host.Port == 0 {
		host.Port = 22
	}

	for i, existing := range hosts {
		if addressKey(existing.Address, existing.Port) != addressKey(host.Address, host.Port) {
			continue
		}

		// Carry forward what the scans learned; the caller only knows how to
		// reach the machine, not when it was first seen.
		host.FirstSeen = existing.FirstSeen
		if host.Hostname == "" {
			host.Hostname = existing.Hostname
		}
		if host.OS == "" {
			host.OS = existing.OS
		}
		if host.Banner == "" {
			host.Banner = existing.Banner
		}
		if host.LastSeen.IsZero() {
			host.LastSeen = existing.LastSeen
		}

		// A rename is fine, but not onto a name another machine already holds.
		if err := checkNameFree(hosts, host.Name, i); err != nil {
			return nil, err
		}
		hosts[i] = host
		return hosts, nil
	}

	if err := checkNameFree(hosts, host.Name, -1); err != nil {
		return nil, err
	}
	return append(hosts, host), nil
}

// checkNameFree rejects a name already used by a different record.
func checkNameFree(hosts []inventory.Host, name string, ignoreIndex int) error {
	for i, h := range hosts {
		if i != ignoreIndex && h.Name == name {
			return fmt.Errorf("host %q is already in the config at a different address (%s)", name, h.Address)
		}
	}
	return nil
}

// findOrCreateSequence returns the sequence node for key, adding an empty one
// if the document does not have it yet.
func findOrCreateSequence(root *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == key {
			value := root.Content[i+1]
			if value.Kind != yaml.SequenceNode {
				value.Kind = yaml.SequenceNode
				value.Tag = "!!seq"
				value.Value = ""
				value.Content = nil
			}
			return value
		}
	}

	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root.Content = append(root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		seq,
	)
	return seq
}

// mappingValue reads a scalar value out of a mapping node.
func mappingValue(node *yaml.Node, key string) string {
	if node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1].Value
		}
	}
	return ""
}

// hostNode renders a host as a YAML mapping node, omitting anything that would
// just restate a default. Field order is chosen for readability: identity,
// then whether hlab manages it, then how to reach it, then what was observed.
func hostNode(host inventory.Host) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}

	add := func(key string, value *yaml.Node) {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			value)
	}
	scalar := func(v string) *yaml.Node {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	}
	str := func(key, value string) {
		if value != "" {
			add(key, scalar(value))
		}
	}
	stamp := func(key string, t time.Time) {
		if !t.IsZero() {
			add(key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp",
				Value: t.Format(time.RFC3339)})
		}
	}

	add("name", scalar(host.Name))
	add("address", scalar(host.Address))
	if host.Port != 0 && host.Port != 22 {
		add("port", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(host.Port)})
	}

	// Always written, including when false: whether hlab manages a machine is
	// the most important thing about the record, so it should never be implied
	// by absence.
	add("managed", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool",
		Value: strconv.FormatBool(host.Managed)})

	str("user", host.User)
	str("identityFile", host.IdentityFile)

	if len(host.Tags) > 0 {
		tags := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
		for _, t := range host.Tags {
			tags.Content = append(tags.Content, scalar(t))
		}
		add("tags", tags)
	}

	str("description", host.Description)
	str("hostname", host.Hostname)
	str("os", host.OS)
	str("banner", host.Banner)

	// Durable attributes learned by collection.
	str("kernel", host.Kernel)
	str("arch", host.Arch)
	if host.CPUs > 0 {
		add("cpus", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(host.CPUs)})
	}
	if host.MemoryMB > 0 {
		add("memoryMB", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int",
			Value: strconv.FormatInt(host.MemoryMB, 10)})
	}
	if host.Network != nil {
		var netNode yaml.Node
		if err := netNode.Encode(host.Network); err != nil {
			return nil, fmt.Errorf("render network for %s: %w", host.Name, err)
		}
		add("network", &netNode)
	}
	if len(host.Containers) > 0 {
		var containerNode yaml.Node
		if err := containerNode.Encode(host.Containers); err != nil {
			return nil, fmt.Errorf("render containers for %s: %w", host.Name, err)
		}
		add("containers", &containerNode)
	}

	stamp("firstSeen", host.FirstSeen)
	stamp("lastSeen", host.LastSeen)
	stamp("lastCollected", host.LastCollected)

	return node, nil
}

// documentRoot parses a config into a document plus its root mapping, creating
// an empty one when the file is absent or holds only comments.
func documentRoot(raw []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node

	if len(raw) > 0 {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return nil, nil, fmt.Errorf("parse existing config: %w", err)
		}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}
	}

	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, nil, errors.New("config root is not a mapping")
	}
	return &doc, root, nil
}

// marshalDocument renders the edited tree back to YAML.
func marshalDocument(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("render config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render config: %w", err)
	}
	return buf.Bytes(), nil
}

// writeAtomic writes data to path via a temporary file and a rename, so a
// failure part-way through cannot leave the config truncated.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".hlab-config-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		// Best effort: some filesystems do not support it.
		_ = err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
	}

	// Windows will not rename onto an existing file.
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			_ = os.Remove(tmpName)
			return fmt.Errorf("replace %s: %w", path, err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
