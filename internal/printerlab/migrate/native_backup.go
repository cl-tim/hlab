package migrate

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

func verifyBackupArchive(path string) error {
	stream, err := os.Open(path)
	if err != nil {
		return err
	}
	defer stream.Close()
	compressed, err := gzip.NewReader(stream)
	if err != nil {
		return err
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	members := 0
	for {
		_, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		members++
		if _, err := io.Copy(io.Discard, archive); err != nil {
			return err
		}
	}
	if members == 0 {
		return fmt.Errorf("backup archive is empty")
	}
	_, err = io.Copy(io.Discard, compressed)
	return err
}
func (b *nativeBackend) backupPrinter() error {
	if _, err := b.connection(); err != nil {
		return err
	}
	if _, err := b.authOptions(); err != nil {
		return err
	}
	name := b.value("name")
	if name == "" {
		name = b.value("printer")
	}
	name = strings.Trim(regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(name, "-"), "-")
	if name == "" || name == "." || name == ".." {
		name = "printer"
	}
	base := "qidi-plus4-" + name + "-" + time.Now().Format("20060102-150405") + "-" + randomToken()
	output := b.value("output")
	if output == "" {
		output = filepath.Join(b.home, "backups")
	}
	output, err := localPath(output)
	if err != nil {
		return err
	}
	destination := filepath.Join(output, name)
	if err := os.MkdirAll(destination, 0700); err != nil {
		return err
	}
	archive := filepath.Join(destination, base+".tar.gz")
	manifest := filepath.Join(destination, base+"-manifest.txt")
	checksum := archive + ".sha256"
	finals := []string{manifest, checksum, archive}
	partials := []string{manifest + ".partial", checksum + ".partial", archive + ".partial"}
	for _, path := range append(append([]string{}, finals...), partials...) {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("refusing to overwrite existing backup artifact: %s", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	remoteHelper := "/tmp/" + base + "-backup.sh"
	remoteManifest := "/tmp/" + base + "-manifest.txt"
	helper, err := b.helper("backup-remote.sh")
	if err != nil {
		return err
	}
	if _, _, err := b.sshCapture("set -eu; umask 077; set -C; cat > "+quote(remoteHelper)+"; chmod 700 "+quote(remoteHelper), helper); err != nil {
		return err
	}
	defer b.sshCapture("rm -f -- "+quoted(remoteHelper, remoteManifest), nil)
	owned := []string{}
	published := []string{}
	complete := false
	defer func() {
		for _, path := range owned {
			os.Remove(path)
		}
		if !complete {
			for _, path := range published {
				os.Remove(path)
			}
		}
	}()
	stream, err := os.OpenFile(partials[2], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	owned = append(owned, partials[2])
	parts := []string{remoteHelper, remoteManifest}
	for _, key := range []string{"include-gcodes", "include-logs", "include-environments"} {
		if b.switches[key] {
			parts = append(parts, "1")
		} else {
			parts = append(parts, "0")
		}
	}
	fmt.Fprintf(b.out, "Streaming backup to %s\n", partials[2])
	runErr := b.sshWrite(quoted(parts...), nil, stream)
	syncErr := stream.Sync()
	closeErr := stream.Close()
	if runErr != nil {
		return runErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	raw, _, err := b.sshCapture("cat -- "+quote(remoteManifest), nil)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("remote backup omitted its manifest")
	}
	if err := exclusiveWrite(partials[0], raw); err != nil {
		return err
	}
	owned = append(owned, partials[0])
	if err := verifyBackupArchive(partials[2]); err != nil {
		return fmt.Errorf("backup archive validation failed: %w", err)
	}
	digest, err := hashFile(partials[2])
	if err != nil {
		return err
	}
	if err := exclusiveWrite(partials[1], []byte(digest+"  "+filepath.Base(archive)+"\n")); err != nil {
		return err
	}
	owned = append(owned, partials[1])
	for i, partial := range partials {
		if err := publishExclusive(partial, finals[i]); err != nil {
			return err
		}
		published = append(published, finals[i])
	}
	complete = true
	fmt.Fprintf(b.out, "Verified %s\nSHA-256 %s\n", archive, digest)
	return nil
}
