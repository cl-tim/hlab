package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func kernelVersion(value string) ([3]int, error) {
	var result [3]int
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(value) {
		return result, fmt.Errorf("kernel version must use X.Y.Z")
	}
	for i, part := range strings.Split(value, ".") {
		n, err := strconv.Atoi(part)
		if err != nil {
			return result, err
		}
		result[i] = n
	}
	return result, nil
}
func kernelOlder(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}
func qualifiedArmbianName(address, minimum string) (string, string, error) {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", "", fmt.Errorf("Armbian image URL must use HTTPS")
	}
	name := path.Base(parsed.Path)
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.img\.xz$`).MatchString(name) {
		return "", "", fmt.Errorf("resolved image filename is unsafe or unsupported")
	}
	match := regexp.MustCompile(`_(?:current|edge|legacy)_([0-9]+\.[0-9]+\.[0-9]+)[_-]`).FindStringSubmatch(name)
	if len(match) != 2 || !strings.Contains(strings.ToLower(name), "qidi") || !strings.HasSuffix(name, ".img.xz") {
		return "", "", fmt.Errorf("cannot identify Qidi image/kernel from resolved filename; select an exact URL")
	}
	actual, err := kernelVersion(match[1])
	if err != nil {
		return "", "", err
	}
	required, err := kernelVersion(minimum)
	if err != nil {
		return "", "", err
	}
	if kernelOlder(actual, required) {
		return "", "", fmt.Errorf("image kernel %s is older than required %s", match[1], minimum)
	}
	return name, match[1], nil
}
func (b *nativeBackend) armbianImage() error {
	fmt.Fprintln(b.out, "Official Armbian Qidi X-6 / Plus4 minimal image\nhttps://www.armbian.com/boards/qidi-x6\nBuild: ./compile.sh BOARD=qidi-x6 RELEASE=trixie BUILD_DESKTOP=no BUILD_MINIMAL=yes KERNEL_CONFIGURE=no")
	if b.switches["list-only"] {
		return nil
	}
	base := b.value("url")
	if base == "" {
		base = "https://dl.armbian.com/qidi-x6/Trixie_current_minimal"
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("Armbian image URL must use HTTPS")
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ResponseHeaderTimeout: 30 * time.Second}}
	if b.httpClient != nil {
		copy := *b.httpClient
		client = &copy
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing HTTPS downgrade")
		}
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}
	head, err := http.NewRequestWithContext(b.ctx, http.MethodHead, base, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(head)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("image HEAD returned %s", response.Status)
	}
	resolved := response.Request.URL.String()
	name, kernel, err := qualifiedArmbianName(resolved, b.value("minimum-kernel"))
	if err != nil {
		return err
	}
	output := b.value("output")
	if output == "" {
		output = filepath.Join(b.home, "artifacts", "armbian")
	}
	output, err = localPath(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	target := filepath.Join(output, name)
	if b.switches["reuse"] {
		if _, err := os.Lstat(target); err == nil {
			raw, err := os.ReadFile(target + ".sha")
			if err != nil {
				return err
			}
			fields := strings.Fields(string(raw))
			digest, err := hashFile(target)
			if err != nil {
				return err
			}
			if len(fields) < 1 || strings.ToLower(fields[0]) != digest {
				return fmt.Errorf("cached Armbian checksum mismatch: %s", target)
			}
			metadata, err := os.ReadFile(target + ".json")
			if err != nil {
				return err
			}
			doc, err := decodeObject(metadata)
			if err != nil {
				return err
			}
			if doc["filename"] != name || doc["kernel"] != kernel || doc["sha256"] != digest || doc["url"] != resolved {
				return fmt.Errorf("cached Armbian receipt does not match the selected image")
			}
			fmt.Fprintf(b.out, "Reusing verified Armbian image: %s (kernel %s, SHA-256 %s)\n", target, kernel, digest)
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for _, suffix := range []string{"", ".sha", ".json"} {
		if _, err := os.Lstat(target + suffix); err == nil {
			return fmt.Errorf("refusing to overwrite existing download %s", target+suffix)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	stage, err := os.MkdirTemp(output, ".armbian-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	fetch := func(address string, destination io.Writer) error {
		req, err := http.NewRequestWithContext(b.ctx, http.MethodGet, address, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "printerlab")
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("download returned %s", res.Status)
		}
		n, err := io.Copy(destination, res.Body)
		if err != nil {
			return err
		}
		if res.ContentLength >= 0 && n != res.ContentLength {
			return fmt.Errorf("download length mismatch")
		}
		return nil
	}
	fmt.Fprintf(b.out, "Downloading %s (kernel %s)...\n", name, kernel)
	file, err := os.Create(filepath.Join(stage, name))
	if err != nil {
		return err
	}
	hash := sha256.New()
	downloadErr := fetch(resolved, io.MultiWriter(file, hash))
	syncErr := file.Sync()
	closeErr := file.Close()
	if downloadErr != nil {
		return downloadErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	var checksum strings.Builder
	checksumURL := *response.Request.URL
	checksumURL.Path += ".sha"
	checksumURL.RawPath = ""
	if err := fetch(checksumURL.String(), &limitedStringWriter{target: &checksum, limit: 64 * 1024}); err != nil {
		return err
	}
	fields := strings.Fields(checksum.String())
	if len(fields) == 0 || !digestPattern.MatchString(strings.ToLower(fields[0])) {
		return fmt.Errorf("invalid image checksum")
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if digest != strings.ToLower(fields[0]) {
		return fmt.Errorf("Armbian image SHA-256 mismatch")
	}
	if err := os.WriteFile(filepath.Join(stage, name+".sha"), []byte(checksum.String()), 0600); err != nil {
		return err
	}
	meta, err := json.MarshalIndent(map[string]any{"url": resolved, "filename": name, "kernel": kernel, "sha256": digest, "qualification": "download verified; boot and UART checks pending"}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, name+".json"), append(meta, '\n'), 0600); err != nil {
		return err
	}
	published := []string{}
	for _, suffix := range []string{".sha", ".json", ""} {
		if err := publishExclusive(filepath.Join(stage, name+suffix), target+suffix); err != nil {
			for _, p := range published {
				os.Remove(p)
			}
			return err
		}
		published = append(published, target+suffix)
	}
	fmt.Fprintf(b.out, "Verified SHA-256 %s\n", digest)
	return nil
}

type limitedStringWriter struct {
	target *strings.Builder
	limit  int
}

func (w *limitedStringWriter) Write(raw []byte) (int, error) {
	if w.target.Len()+len(raw) > w.limit {
		return 0, fmt.Errorf("response exceeds %d bytes", w.limit)
	}
	return w.target.Write(raw)
}

// Hard links publish completed files without replacing a concurrent writer.
func publishExclusive(source, target string) error {
	if err := os.Link(source, target); err != nil {
		return err
	}
	return os.Remove(source)
}
