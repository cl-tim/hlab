package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/output"
)

func newBackupVerifyCmd(a *app) *cobra.Command {
	var root string
	var maxAge time.Duration
	cmd := &cobra.Command{
		Use:   "verify [selector]",
		Short: "Check the latest local backup for each selected host",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if maxAge < 0 {
				return fmt.Errorf("--max-age must be non-negative")
			}
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}
			if root == "" {
				root = filepath.Join(config.Home(), backupDirName)
			}
			type result struct {
				Host    string `json:"host" yaml:"host"`
				Archive string `json:"archive,omitempty" yaml:"archive,omitempty"`
				Age     string `json:"age,omitempty" yaml:"age,omitempty"`
				Error   string `json:"error,omitempty" yaml:"error,omitempty"`
			}
			results := make([]result, 0, len(hosts))
			tbl := output.Table{Headers: []string{"HOST", "ARCHIVE", "AGE", "STATUS"}}
			failed := false
			now := time.Now()
			for _, host := range hosts {
				path, age, err := checkLatestBackup(filepath.Join(root, host.Name), now, maxAge)
				r := result{Host: host.Name, Archive: path, Age: age}
				status := "OK"
				if err != nil {
					failed = true
					r.Error = err.Error()
					status = r.Error
				}
				results = append(results, r)
				tbl.AddRow(r.Host, orDash(r.Archive), orDash(r.Age), status)
			}
			if err := output.Write(a.out, a.format, results, tbl); err != nil {
				return err
			}
			if failed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "dir", "", "directory containing per-host backup folders")
	cmd.Flags().DurationVar(&maxAge, "max-age", 0, "fail if the latest backup is older than this (0 disables the limit)")
	return cmd
}

func checkLatestBackup(dir string, now time.Time, maxAge time.Duration) (string, string, error) {
	path, err := latestBackup(dir)
	if err != nil {
		return "", "", err
	}
	stamp, err := time.ParseInLocation("20060102-150405", strings.TrimSuffix(filepath.Base(path), ".tar.gz"), now.Location())
	if err != nil {
		return path, "", fmt.Errorf("invalid backup timestamp: %w", err)
	}
	age := now.Sub(stamp)
	displayAge := age.Round(time.Second).String()
	data, err := os.ReadFile(path)
	if err != nil {
		return path, displayAge, err
	}
	if _, err := verifyStoredArchive(path, data); err != nil {
		return path, displayAge, err
	}
	if err := checkArchiveReadable(data); err != nil {
		return path, displayAge, fmt.Errorf("read archive: %w", err)
	}
	if maxAge > 0 && age > maxAge {
		return path, displayAge, fmt.Errorf("backup exceeds maximum age %s", maxAge)
	}
	return path, displayAge, nil
}

func checkArchiveReadable(data []byte) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	entries := 0
	for {
		_, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		entries++
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return err
		}
	}
	if entries == 0 {
		return errors.New("archive contains no entries")
	}
	// Read through the gzip trailer to verify its checksum.
	_, err = io.Copy(io.Discard, gz)
	return err
}
