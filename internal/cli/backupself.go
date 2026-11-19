package cli

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/cl-tim/hlab/internal/config"
)

// selfFiles are the parts of hlab's own home worth keeping.
//
// The inventory is the recovery-critical one and is easy to overlook: a
// replacement machine gets its address, user and static network configuration
// from there, not from the archive taken off the machine that died.
var selfFiles = []string{"config.yaml", "facts.yaml"}

// keyFile is deliberately not in selfFiles.
const keyFile = "id_hlab"

// backupSelf archives hlab's own configuration alongside the fleet's.
//
// The management key is excluded unless asked for. It is root-equivalent on
// every enrolled machine, and these archives are routinely pointed at a synced
// folder — which means a cloud account. Whether that is an acceptable place
// for it is a decision the operator should make deliberately rather than one
// that happens as a side effect of backing up some YAML.
func (a *app) backupSelf(root, stamp string, includeKey bool) (string, error) {
	home := config.Home()

	want := append([]string{}, selfFiles...)
	if includeKey {
		want = append(want, keyFile, keyFile+".pub")
	}

	var present []string
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			present = append(present, name)
		}
	}
	if len(present) == 0 {
		return "", fmt.Errorf("nothing to archive in %s", home)
	}

	dir := filepath.Join(root, "hlab-home")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, stamp+".tar.gz")

	// 0600, because with --include-key this file is the keys to the fleet.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	for _, name := range present {
		if err := addFile(tw, home, name); err != nil {
			return "", err
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// addFile copies one file into the archive.
func addFile(tw *tar.Writer, dir, name string) error {
	full := filepath.Join(dir, name)
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return err
	}

	header := &tar.Header{
		Name:    name,
		Mode:    0o600,
		Size:    int64(len(body)),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = tw.Write(body)
	return err
}

// selfStamp is the timestamp shared by one run's archives.
func selfStamp() string { return time.Now().Format("20060102-150405") }
