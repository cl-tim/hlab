package tlsmgr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func Architecture(machine, bits string) (string, error) {
	switch strings.TrimSpace(machine) {
	case "x86_64":
		return "amd64", nil
	case "aarch64", "arm64":
		if strings.TrimSpace(bits) == "32" {
			return "armv7", nil
		}
		return "arm64", nil
	case "armv7l", "armv8l":
		return "armv7", nil
	default:
		return "", fmt.Errorf("unsupported Caddy architecture %q", machine)
	}
}

// Download verifies the archive against the versioned upstream checksum file,
// then extracts only the regular caddy member with a strict size limit. The
// cache is content-checked again on every use, rather than trusting its name.
func Download(ctx context.Context, cache, arch string) ([]byte, error) {
	if arch != "amd64" && arch != "arm64" && arch != "armv7" {
		return nil, fmt.Errorf("invalid architecture")
	}
	name := "caddy_" + CaddyVersion + "_linux_" + arch + ".tar.gz"
	base := "https://github.com/caddyserver/caddy/releases/download/v" + CaddyVersion + "/"
	checks, err := download(ctx, base+"caddy_"+CaddyVersion+"_checksums.txt", 1<<20)
	if err != nil {
		return nil, err
	}
	var want string
	for _, line := range strings.Split(string(checks), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = f[0]
		}
	}
	if _, err = hex.DecodeString(want); err != nil || (len(want) != 64 && len(want) != 128) {
		return nil, fmt.Errorf("upstream checksum missing for %s", name)
	}
	path := filepath.Join(cache, name)
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err != nil {
		raw, err = download(ctx, base+name, 128<<20)
		if err != nil {
			return nil, err
		}
	}
	h := sha256.Sum256(raw)
	got := hex.EncodeToString(h[:])
	if len(want) == 128 {
		h512 := sha512.Sum512(raw)
		got = hex.EncodeToString(h512[:])
	}
	if got != want {
		return nil, fmt.Errorf("Caddy archive checksum mismatch: %s", name)
	}
	if err = os.MkdirAll(cache, 0700); err != nil {
		return nil, err
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		return nil, err
	}
	return extractBinary(raw)
}

func download(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	r, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: HTTP %d", address, r.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("download exceeds size limit")
	}
	return raw, nil
}

func extractBinary(raw []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, 200<<20))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Name != "caddy" {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size < 1 || h.Size > 128<<20 {
			return nil, fmt.Errorf("invalid caddy archive member")
		}
		return io.ReadAll(io.LimitReader(tr, h.Size))
	}
	return nil, fmt.Errorf("archive contains no caddy binary")
}
