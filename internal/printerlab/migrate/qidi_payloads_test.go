package migrate

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkstationQIDIPayloadsRequireBothExactReviewedFiles(t *testing.T) {
	dir := t.TempDir()
	host, screen := []byte("reviewed host"), []byte("reviewed screen")
	release := QIDIRelease{Host: QIDIArtifact{URL: "https://vendor.example/QD_Model_SOC", Bytes: int64(len(host)), SHA256: hashBytes(host)}, Screen: QIDIArtifact{URL: "https://vendor.example/QD_Model_UI", Bytes: int64(len(screen)), SHA256: hashBytes(screen)}}
	if err := os.WriteFile(filepath.Join(dir, "QD_Model_SOC"), host, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedQIDIFiles(dir, release, false); err == nil {
		t.Fatal("missing screen accepted for combined upgrade")
	}
	if files, err := verifiedQIDIFiles(dir, release, true); err != nil || len(files) != 1 {
		t.Fatalf("host-only: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "QD_Model_UI"), screen, 0600); err != nil {
		t.Fatal(err)
	}
	files, err := verifiedQIDIFiles(dir, release, false)
	if err != nil || !bytes.Equal(files["screen.tft"], screen) {
		t.Fatalf("verified payloads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "QD_Model_SOC"), []byte("corrupt! host"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifiedQIDIFiles(dir, release, false); err == nil {
		t.Fatal("corrupted local firmware accepted")
	}
}

func TestWorkstationQIDIStagingUsesBinaryStdinAndFixedNames(t *testing.T) {
	b := nativeTestBackend(t)
	b.values = map[string]string{"printer": "fixture.example", "user": "mks", "port": "22"}
	files := map[string][]byte{"host.deb": []byte("host fixture"), "screen.tft": []byte("screen fixture")}
	release := QIDIRelease{Host: QIDIArtifact{Bytes: 12, SHA256: hashBytes(files["host.deb"])}, Screen: QIDIArtifact{Bytes: 14, SHA256: hashBytes(files["screen.tft"])}}
	staged, cleaned := false, false
	b.transport = func(command string, payload []byte) ([]byte, int, error) {
		if !strings.HasPrefix(command, "sudo -n python3 -I -c ") {
			t.Fatalf("unexpected command: %s", command)
		}
		if strings.Contains(command, "invalid staging cleanup target") {
			cleaned = true
			return nil, 0, nil
		}
		if strings.Contains(command, "host fixture") {
			t.Fatal("firmware bytes exposed in command line")
		}
		archive := tar.NewReader(bytes.NewReader(payload))
		seen := 0
		for {
			member, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(archive)
			if !bytes.Equal(raw, files[member.Name]) {
				t.Fatalf("wrong payload for %s", member.Name)
			}
			seen++
		}
		if seen != 2 {
			t.Fatal("missing staged payload")
		}
		staged = true
		return []byte("staged\n"), 0, nil
	}
	directory, cleanup, err := b.stageQIDIFiles(files, release)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(directory, "/var/lib/hlab/qidi-payloads/") || !staged {
		t.Fatalf("invalid staging: %s", directory)
	}
	cleanup()
	if !cleaned {
		t.Fatal("staging cleanup omitted")
	}
}
