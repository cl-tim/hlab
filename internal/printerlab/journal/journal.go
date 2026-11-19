// Package journal records migration attempts on the workstation.
// The journal survives replacement of the printer's host eMMC.
// Stages are recorded as started before execution so interrupted operations
// remain visible when migration resumes.
package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cl-tim/homelab-cli/internal/printerlab/stages"
)

const schemaVersion = 1

type Entry struct {
	Attempt  stages.Attempt `json:"attempt"`
	Started  time.Time      `json:"started,omitempty"`
	Finished time.Time      `json:"finished,omitempty"`
	Error    string         `json:"error,omitempty"`
}

type Journal struct {
	Schema  int              `json:"schema"`
	Printer string           `json:"printer"`
	Recipe  string           `json:"recipe"`
	Digest  string           `json:"recipeDigest"`
	Created time.Time        `json:"created"`
	Updated time.Time        `json:"updated"`
	Entries map[string]Entry `json:"entries"`

	path string
}

// Path returns the journal file for a printer under printerlab's home.
func Path(home, printer string) string {
	return filepath.Join(home, "runs", safeName(printer)+".json")
}

// safeName keeps an inventory name from escaping the runs directory.
func safeName(name string) string {
	replaced := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, name)
	trimmed := strings.Trim(replaced, ".-")
	if trimmed == "" {
		return "printer"
	}
	return trimmed
}

// Load reads a printer's journal, returning an empty one when none exists.
func Load(path, printer, recipe, digest string) (*Journal, error) {
	journal := &Journal{
		Schema: schemaVersion, Printer: printer, Recipe: recipe, Digest: digest,
		Created: time.Now().UTC(), Entries: map[string]Entry{}, path: path,
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return journal, nil
		}
		return nil, fmt.Errorf("read journal %s: %w", path, err)
	}
	var stored Journal
	if err := json.Unmarshal(raw, &stored); err != nil {
		return nil, fmt.Errorf("parse journal %s: %w", path, err)
	}
	if stored.Schema != schemaVersion {
		return nil, fmt.Errorf(
			"journal %s uses unsupported schema %d", path, stored.Schema)
	}
	stored.path = path
	if stored.Entries == nil {
		stored.Entries = map[string]Entry{}
	}
	return &stored, nil
}

// Stale reports whether the stage graph changed since this journal was
// written. Resuming across a changed sequence would silently reinterpret every
// recorded stage, so the caller must decide explicitly.
func (j *Journal) Stale(digest string) bool {
	return j.Digest != "" && j.Digest != digest
}

func (j *Journal) Attempts() map[string]stages.Attempt {
	attempts := make(map[string]stages.Attempt, len(j.Entries))
	for id, entry := range j.Entries {
		attempts[id] = entry.Attempt
	}
	return attempts
}

func (j *Journal) Entry(stage string) (Entry, bool) {
	entry, ok := j.Entries[stage]
	return entry, ok
}

// Start records an attempt before its command runs.
func (j *Journal) Start(stage string) {
	j.Entries[stage] = Entry{Attempt: stages.AttemptStarted, Started: time.Now().UTC()}
}

func (j *Journal) Complete(stage string) {
	entry := j.Entries[stage]
	entry.Attempt = stages.AttemptCompleted
	entry.Finished = time.Now().UTC()
	entry.Error = ""
	j.Entries[stage] = entry
}

func (j *Journal) Fail(stage string, err error) {
	entry := j.Entries[stage]
	entry.Attempt = stages.AttemptFailed
	entry.Finished = time.Now().UTC()
	if err != nil {
		entry.Error = err.Error()
	}
	j.Entries[stage] = entry
}

// Adopt records the current graph digest, accepting a changed sequence.
func (j *Journal) Adopt(digest string) { j.Digest = digest }

// Save writes the journal atomically so an interrupted write cannot destroy
// the record of a flash that already happened.
func (j *Journal) Save() error {
	if j.path == "" {
		return fmt.Errorf("journal has no path")
	}
	j.Updated = time.Now().UTC()
	if j.Created.IsZero() {
		j.Created = j.Updated
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return fmt.Errorf("create journal directory: %w", err)
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return fmt.Errorf("encode journal: %w", err)
	}
	raw = append(raw, '\n')
	temporary := j.path + ".tmp"
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		return fmt.Errorf("write journal: %w", err)
	}
	if err := os.Rename(temporary, j.path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace journal: %w", err)
	}
	return nil
}
