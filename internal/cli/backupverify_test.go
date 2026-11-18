package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVerifyLatestBackupValidatesArchiveAndAge(t *testing.T) {
	var data bytes.Buffer
	gz := gzip.NewWriter(&data)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "hlab", Typeflag: tar.TypeDir, Mode: 0700}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "20260101-120000.tar.gz")
	write := func(body []byte) {
		t.Helper()
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+".sha256", []byte(sha256Hex(body)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(data.Bytes())
	now := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	if _, age, err := checkLatestBackup(dir, now, 2*time.Hour); age != "1h0m0s" || err != nil {
		t.Fatalf("age = %q, error = %v", age, err)
	}
	if _, _, err := checkLatestBackup(dir, now, 30*time.Minute); err == nil {
		t.Fatal("stale backup passed")
	}
	write(data.Bytes()[:data.Len()-5])
	if _, _, err := checkLatestBackup(dir, now, 0); err == nil {
		t.Fatal("archive with truncated gzip trailer passed")
	}
}
