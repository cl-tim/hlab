package facts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"go.yaml.in/yaml/v3"
)

// Snapshot is one host's collected state, as stored.
//
// This is the volatile half of what a collection learns: what was running at a
// point in time. The durable attributes — kernel, arch, CPU, memory, network —
// live on the host record in the config instead, so that file stays readable.
type Snapshot struct {
	CollectedAt time.Time `yaml:"collectedAt" json:"collectedAt"`

	UptimeSeconds int64     `yaml:"uptimeSeconds,omitempty" json:"uptimeSeconds,omitempty"`
	LoadAverage   []float64 `yaml:"loadAverage,omitempty,flow" json:"loadAverage,omitempty"`

	Disks    []Disk    `yaml:"disks,omitempty" json:"disks,omitempty"`
	Services []Service `yaml:"services,omitempty" json:"services,omitempty"`
	Ports    []Port    `yaml:"ports,omitempty" json:"ports,omitempty"`

	// Partial records that the host answered but the snapshot may be
	// incomplete, so a thin result is not mistaken for a quiet machine.
	Partial bool `yaml:"partial,omitempty" json:"partial,omitempty"`
}

// Store maps host name to its most recent snapshot.
type Store map[string]Snapshot

// SnapshotOf extracts the storable half of a collection result.
func SnapshotOf(f Facts, collectedAt time.Time) Snapshot {
	return Snapshot{
		CollectedAt:   collectedAt.Truncate(time.Second),
		UptimeSeconds: f.UptimeSeconds,
		LoadAverage:   f.LoadAverage,
		Disks:         f.Disks,
		Services:      f.Services,
		Ports:         f.Ports,
		Partial:       f.Partial,
	}
}

// LoadStore reads the facts store. A missing file is an empty store, not an
// error: nothing has been collected yet.
func LoadStore(path string) (Store, error) {
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		return Store{}, nil
	default:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	store := Store{}
	if err := yaml.Unmarshal(raw, &store); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if store == nil {
		store = Store{}
	}
	return store, nil
}

// SaveStore writes the facts store, replacing entries for the hosts collected
// and leaving the rest alone. Writing is atomic.
func SaveStore(path string, updates Store) error {
	if path == "" {
		return errors.New("no facts store path given")
	}

	store, err := LoadStore(path)
	if err != nil {
		return err
	}
	for host, snapshot := range updates {
		store[host] = snapshot
	}

	return writeStore(path, store)
}

// writeStore renders the whole store to disk, replacing whatever was there.
func writeStore(path string, store Store) error {
	// Sort keys so the file is stable across runs and diffs cleanly.
	names := make([]string, 0, len(store))
	for name := range store {
		names = append(names, name)
	}
	sort.Strings(names)

	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, name := range names {
		var value yaml.Node
		if err := value.Encode(store[name]); err != nil {
			return fmt.Errorf("render snapshot for %s: %w", name, err)
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name},
			&value)
	}

	var body bytes.Buffer
	enc := yaml.NewEncoder(&body)
	// Match the config's indentation so the two files read alike.
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return fmt.Errorf("render facts store: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("render facts store: %w", err)
	}

	header := "# Collected state, written by `hlab discover services`.\n" +
		"# Machine-owned: edits here are overwritten. Durable attributes live\n" +
		"# on the host records in config.yaml instead.\n"

	return writeFileAtomic(path, append([]byte(header), body.Bytes()...))
}

// writeFileAtomic writes via a temporary file and a rename, so an interrupted
// write cannot leave a truncated store behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	tmp, err := os.CreateTemp(dir, ".hlab-facts-*")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write %s: %w", tmpName, err)
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

// RenameInStore moves a host's collected state to a new name, so a renamed
// machine does not look like one that has never been collected from.
//
// A host with nothing stored is not an error: it simply has no snapshot yet.
func RenameInStore(path, oldName, newName string) error {
	store, err := LoadStore(path)
	if err != nil {
		return err
	}

	snapshot, ok := store[oldName]
	if !ok {
		return nil
	}
	if _, taken := store[newName]; taken {
		return fmt.Errorf("the facts store already has an entry for %q", newName)
	}

	delete(store, oldName)
	store[newName] = snapshot

	// SaveStore merges rather than replaces, so the removal has to be written
	// against the full set rather than handed over as an update.
	return writeStore(path, store)
}

// RemoveFromStore drops a host's collected state, for a record that no longer
// exists. A host with nothing stored is not an error.
func RemoveFromStore(path, name string) error {
	store, err := LoadStore(path)
	if err != nil {
		return err
	}
	if _, ok := store[name]; !ok {
		return nil
	}
	delete(store, name)
	return writeStore(path, store)
}
