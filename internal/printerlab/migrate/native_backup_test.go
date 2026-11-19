package migrate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeBackupStreamsVerifiesAndPublishesArchiveLast(t *testing.T) {
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	archive.WriteHeader(&tar.Header{Name: "config/printer.cfg", Size: 4, Mode: 0600})
	archive.Write([]byte("test"))
	archive.Close()
	compressed.Close()
	for _, scenario := range []string{"success", "interrupted", "corrupt", "missing-manifest"} {
		t.Run(scenario, func(t *testing.T) {
			b := nativeTestBackend(t)
			output := t.TempDir()
			cleanup := false
			b.transport = func(command string, input []byte) ([]byte, int, error) {
				if strings.HasPrefix(command, "rm -f") {
					cleanup = true
					return nil, 0, nil
				}
				if strings.HasPrefix(command, "set -eu") {
					if !bytes.Contains(input, []byte("tar")) {
						t.Error("backup helper missing")
					}
					return nil, 0, nil
				}
				if strings.HasPrefix(command, "cat --") {
					if scenario == "missing-manifest" {
						return nil, 0, nil
					}
					return []byte("config/printer.cfg\n"), 0, nil
				}
				if scenario == "interrupted" {
					return nil, 255, fmt.Errorf("connection lost")
				}
				if scenario == "corrupt" {
					return []byte("broken archive"), 0, nil
				}
				return buffer.Bytes(), 0, nil
			}
			err := b.execute([]string{"backup", "--printer", "plus4.invalid", "--name", "test-printer", "--output", output})
			if (err == nil) != (scenario == "success") {
				t.Fatalf("backup err %v", err)
			}
			if !cleanup {
				t.Fatal("remote staging was not cleaned")
			}
			entries, _ := os.ReadDir(filepath.Join(output, "test-printer"))
			if scenario != "success" {
				if len(entries) != 0 {
					t.Fatal("failed backup left final/partial artifacts")
				}
				return
			}
			if len(entries) != 3 {
				t.Fatalf("got %d files", len(entries))
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".tar.gz") {
					path := filepath.Join(output, "test-printer", entry.Name())
					digest, _ := hashFile(path)
					checksum, _ := os.ReadFile(path + ".sha256")
					if !bytes.HasPrefix(checksum, []byte(digest+"  ")) {
						t.Fatal("checksum not bound to archive")
					}
					if err := verifyBackupArchive(path); err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}
