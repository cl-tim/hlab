// Package migration embeds Plus4 migration tools and profiles.
package migration

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Runtime source and notices are embedded; tests and generated outputs are not.
//
//go:embed tools/migration/apply-qidi-fixes.py tools/migration/update-klipper.py tools/migration/update-qidi.py tools/migration/upgrade-klippee-agent.py tools/migration/*.sh tools/migration/*.cfg tools/migration/*.json firmware-configs mainline-configs plr-hardening qidi-fixes LICENSE.* SOURCES.md
var files embed.FS

var bundleID = digest()

// ID identifies the exact tools, profiles and notices in this executable.
func ID() string { return bundleID }

// QIDIReleaseCatalog returns reviewed model-specific vendor release metadata.
func QIDIReleaseCatalog() ([]byte, error) {
	return files.ReadFile("tools/migration/qidi-releases.json")
}

// Root locates this executable's private, versioned tooling cache.
func Root(home string) string {
	return filepath.Join(home, "tooling", "mainline-"+ID())
}

func digest() string {
	hash := sha256.New()
	err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", name, len(data))
		_, _ = hash.Write(data)
		return nil
	})
	if err != nil {
		panic("read embedded migration assets: " + err.Error())
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}

// Ensure atomically extracts and verifies the versioned asset bundle.
// A changed cache requires a fresh extraction directory.
func Ensure(home string) (string, error) {
	if home == "" {
		return "", fmt.Errorf("Printerlab home is required for bundled migration tools")
	}
	root, err := filepath.Abs(Root(home))
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(root); err == nil {
		return root, verify(root)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	staged, err := os.MkdirTemp(parent, ".mainline-stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staged)
	if err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path := filepath.Join(staged, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(path, 0o700)
		}
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o600)
	}); err != nil {
		return "", fmt.Errorf("extract bundled migration tools: %w", err)
	}
	if err := verify(staged); err != nil {
		return "", err
	}
	if err := os.Rename(staged, root); err != nil {
		// A concurrent invocation may have published the same complete bundle.
		if otherErr := verify(root); otherErr != nil {
			return "", fmt.Errorf("publish bundled migration tools: %w", err)
		}
	}
	return root, verify(root)
}

func verify(root string) error {
	return fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("bundled migration asset %s: %w", path, err)
		}
		if entry.IsDir() {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("bundled migration directory is not a real directory: %s", path)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("bundled migration asset is not a regular file: %s", path)
		}
		want, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return fmt.Errorf("bundled migration asset was changed: %s; move this tooling cache aside and rerun plab", path)
		}
		return nil
	})
}
