package backup

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// output builds what a machine would print for a given payload.
func output(payload []byte) string {
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%s%s#%d#%d#\n%s\n%s\n",
		MarkerBegin, hex.EncodeToString(sum[:]), len(payload), 7,
		base64.StdEncoding.EncodeToString(payload), MarkerEnd)
}

func TestParseArchiveRoundTrips(t *testing.T) {
	payload := []byte("a tar file would be here, but any bytes will do\x00\xff\x01")

	arc, err := ParseArchive("apps-1", output(payload))
	if err != nil {
		t.Fatalf("ParseArchive: %v", err)
	}
	if string(arc.Data) != string(payload) {
		t.Error("the archive did not survive the round trip")
	}
	if arc.Files != 7 {
		t.Errorf("Files = %d, want 7", arc.Files)
	}
}

// The failure this exists for: an archive that arrives short looks like a
// perfectly good tar until somebody tries to restore from it, which is the
// worst possible moment to discover it.
func TestParseArchiveRejectsATruncatedTransfer(t *testing.T) {
	payload := []byte(strings.Repeat("x", 4096))
	raw := output(payload)

	// Cut some base64 out of the middle, keeping both markers intact.
	begin := strings.Index(raw, "\n") + 1
	damaged := raw[:begin+100] + raw[begin+200:]

	if _, err := ParseArchive("apps-1", damaged); err == nil {
		t.Error("a truncated archive was accepted")
	}
}

// A stray line of output in the middle of the payload corrupts it in a way
// nothing downstream would notice without the checksum.
func TestParseArchiveRejectsCorruption(t *testing.T) {
	payload := []byte(strings.Repeat("y", 1024))
	raw := output(payload)

	corrupt := strings.Replace(raw, "eXl5", "eXl6", 1)
	if corrupt == raw {
		t.Skip("test payload did not contain the expected base64 run")
	}
	if _, err := ParseArchive("apps-1", corrupt); err == nil {
		t.Error("a corrupted archive was accepted")
	}
}

// Without the end marker the output was cut off mid-transfer, and the base64
// that did arrive may still decode to something plausible.
func TestParseArchiveRequiresTheEndMarker(t *testing.T) {
	payload := []byte("some bytes")
	raw := strings.Replace(output(payload), MarkerEnd, "", 1)

	_, err := ParseArchive("apps-1", raw)
	if err == nil {
		t.Fatal("an archive with no end marker was accepted")
	}
	if !strings.Contains(err.Error(), "cut short") {
		t.Errorf("error does not explain the truncation: %v", err)
	}
}

func TestParseArchiveReportsAMachineWithNothingToSave(t *testing.T) {
	_, err := ParseArchive("apps-1", MarkerNothing+"\n")
	if err == nil || !strings.Contains(err.Error(), "nothing") {
		t.Errorf("err = %v, want a clear 'nothing to back up'", err)
	}
}
