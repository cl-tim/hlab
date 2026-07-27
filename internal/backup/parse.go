package backup

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrNothing means the machine has no hlab-managed state. That is an ordinary
// answer, not a failure: most machines in a fleet run no stacks, and treating
// them as errors would make a healthy run exit non-zero and be useless to
// anything scheduled.
var ErrNothing = errors.New("nothing to back up")

// Archive is one machine's backup, already verified.
type Archive struct {
	Host   string
	Data   []byte
	SHA256 string
	Files  int
}

// ParseArchive extracts and verifies the archive from a machine's output.
//
// Both the checksum and the length are checked, against the values the machine
// computed on the file it actually wrote. That is the whole point: a truncated
// transfer produces a tar that looks plausible and fails only when somebody is
// trying to restore from it, which is the worst possible moment to find out.
func ParseArchive(host, raw string) (Archive, error) {
	if strings.Contains(raw, MarkerNothing) {
		return Archive{}, fmt.Errorf("%s has no %s: %w", host, RemoteDir, ErrNothing)
	}

	start := strings.Index(raw, MarkerBegin)
	if start < 0 {
		return Archive{}, fmt.Errorf("%s produced no archive", host)
	}
	header, rest, ok := strings.Cut(raw[start+len(MarkerBegin):], "\n")
	if !ok {
		return Archive{}, fmt.Errorf("%s produced a truncated archive header", host)
	}

	fields := strings.Split(strings.TrimSuffix(strings.TrimSpace(header), "#"), "#")
	if len(fields) != 3 {
		return Archive{}, fmt.Errorf("%s produced an unreadable archive header %q", host, header)
	}
	wantSum := fields[0]
	wantSize, err := strconv.Atoi(fields[1])
	if err != nil {
		return Archive{}, fmt.Errorf("%s reported an unreadable archive size %q", host, fields[1])
	}
	files, _ := strconv.Atoi(fields[2])

	end := strings.Index(rest, MarkerEnd)
	if end < 0 {
		return Archive{}, fmt.Errorf(
			"%s: the archive was cut short — the end marker never arrived, so it is incomplete", host)
	}

	data, err := base64.StdEncoding.DecodeString(stripWhitespace(rest[:end]))
	if err != nil {
		return Archive{}, fmt.Errorf("%s: the archive could not be decoded: %w", host, err)
	}

	if len(data) != wantSize {
		return Archive{}, fmt.Errorf(
			"%s: the archive is %d bytes but the machine wrote %d; it did not survive the transfer",
			host, len(data), wantSize)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSum {
		return Archive{}, fmt.Errorf(
			"%s: the archive checksum does not match what the machine computed (%s vs %s)",
			host, got, wantSum)
	}

	return Archive{Host: host, Data: data, SHA256: wantSum, Files: files}, nil
}

// stripWhitespace removes the line breaks base64 output carries.
func stripWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r != '\n' && r != '\r' && r != ' ' && r != '\t' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
