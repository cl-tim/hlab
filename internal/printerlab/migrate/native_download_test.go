package migrate

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeArmbianDownloadPublishesOnlyVerifiedImage(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			b := nativeTestBackend(t)
			name := "Armbian_qidi-x6_trixie_current_6.18.54_minimal.img.xz"
			image := bytes.Repeat([]byte("image-fixture"), 1024)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/alias" {
					http.Redirect(w, r, "/"+name, http.StatusFound)
					return
				}
				if strings.HasSuffix(r.URL.Path, ".sha") {
					digest := hashBytes(image)
					if !valid {
						digest = strings.Repeat("0", 64)
					}
					fmt.Fprintf(w, "%s  %s\n", digest, name)
					return
				}
				w.Header().Set("Content-Length", fmt.Sprint(len(image)))
				if r.Method != http.MethodHead {
					w.Write(image)
				}
			}))
			defer server.Close()
			b.httpClient = server.Client()
			output := t.TempDir()
			args := []string{"armbian-image", "--url", server.URL + "/alias", "--output", output}
			err := b.execute(args)
			if (err == nil) != valid {
				t.Fatalf("download err %v", err)
			}
			entries, _ := os.ReadDir(output)
			if !valid {
				if len(entries) != 0 {
					t.Fatal("checksum failure published download")
				}
				return
			}
			raw, _ := os.ReadFile(filepath.Join(output, name))
			if !bytes.Equal(raw, image) {
				t.Fatal("download bytes differ")
			}
			metadata, _ := os.ReadFile(filepath.Join(output, name+".json"))
			if !bytes.Contains(metadata, []byte(hashBytes(image))) {
				t.Fatal("receipt missing verified digest")
			}
			if err := b.execute(append(args, "--reuse")); err != nil {
				t.Fatalf("valid cached image not reused: %v", err)
			}
			if err := os.WriteFile(filepath.Join(output, name), []byte("corrupt cached image"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := b.execute(append(args, "--reuse")); err == nil {
				t.Fatal("corrupt cache accepted")
			}
			if err := b.execute(args); err == nil {
				t.Fatal("existing image overwritten")
			}
		})
	}
}
func TestNativeDownloaderRejectsHTTPSDowngrade(t *testing.T) {
	b := nativeTestBackend(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/image.img.xz", http.StatusFound)
	}))
	defer server.Close()
	b.httpClient = server.Client()
	if err := b.execute([]string{"armbian-image", "--url", server.URL, "--output", t.TempDir()}); err == nil || !strings.Contains(err.Error(), "downgrade") {
		t.Fatalf("downgrade err %v", err)
	}
}
