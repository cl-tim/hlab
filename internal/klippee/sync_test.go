package klippee

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Errorf("missing authentication")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	folder := t.TempDir()
	if err := os.Mkdir(filepath.Join(folder, ".klippee"), 0700); err != nil {
		t.Fatal(err)
	}
	return &Client{Config: Config{BaseURL: server.URL, RepositoryID: "repo", Token: "private-token", ConfigDir: folder, ClientMode: "desktop"}, In: strings.NewReader(""), Out: &bytes.Buffer{}, ErrOut: io.Discard}
}
func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func desired(w http.ResponseWriter, revision, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"version": map[string]any{"version_number": 2, "revision": revision, "source": "pending", "files": []File{{Path: "printer.cfg", Content: content, Role: "main"}}}})
}

func TestApplyIfCleanReadsCRLFBaseline(t *testing.T) {
	original := "[include macros/print.cfg]\n[printer]\nmax_velocity: 200\n"
	updated := strings.Replace(original, "200", "300", 1)
	macro := "[gcode_macro PRINT]\ngcode: M400\n"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "status.txt") {
			io.WriteString(w, "printer_name=Fixture\n")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": map[string]any{
			"version_number": 2, "revision": "new-revision", "source": "printer",
			"files": []File{{Path: "printer.cfg", Content: updated}, {Path: "macros/print.cfg", Content: macro}},
		}})
	})
	writeFile(t, c.Config.ConfigDir, "printer.cfg", original)
	writeFile(t, c.Config.ConfigDir, "macros/print.cfg", macro)
	writeFile(t, c.Config.ConfigDir, "orca-profile.json", "local preset\n")
	writeFile(t, c.Config.ConfigDir, ".klippee/last-sync", "schema=1\r\nrepository_id=repo\r\nrevision=old-revision\r\n")
	writeFile(t, c.Config.ConfigDir, ".klippee/last-sync-files",
		checksum([]byte(macro))+"|macros/print.cfg\r\n"+checksum([]byte(original))+"|printer.cfg\r\n")
	if err := c.Apply(context.Background(), false, true, ""); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{"printer.cfg": updated, "macros/print.cfg": macro, "orca-profile.json": "local preset\n"} {
		data, err := os.ReadFile(filepath.Join(c.Config.ConfigDir, name))
		if err != nil || string(data) != expected {
			t.Fatalf("unexpected %s after sync: %v", name, err)
		}
	}
	if changes, err := c.localChanges(); err != nil || len(changes) != 0 {
		t.Fatalf("new baseline does not match applied files: %v %v", changes, err)
	}
}

func TestManifestCRLFDoesNotAllowUnsafePathsOrDuplicates(t *testing.T) {
	for _, path := range []string{"../outside.cfg", "macros/injected\rname.cfg", "printer.cfg|extra", "printer.cfg\r", "saved_variables.cfg", "duplicate.cfg"} {
		t.Run(strings.ReplaceAll(path, "\r", "CR"), func(t *testing.T) {
			c := &Client{Config: Config{ConfigDir: t.TempDir()}}
			writeFile(t, c.Config.ConfigDir, ".klippee/last-sync-files", "digest|"+path+"\r\ndigest|duplicate.cfg\r\n")
			if _, err := c.manifest(); err == nil {
				t.Fatal("accepted an unsafe or duplicate baseline entry")
			}
		})
	}
}

func TestApplyRevalidatesBeforeAnyMutation(t *testing.T) {
	calls := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "status.txt") {
			io.WriteString(w, "printer_name=Fixture\nprinter_id=one\n")
			return
		}
		calls++
		revision := "first"
		if calls > 1 {
			revision = "second"
		}
		desired(w, revision, "new config\n")
	})
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "original\n")
	err := c.Apply(context.Background(), true, false, "")
	if err == nil || !strings.Contains(err.Error(), "changed during review") {
		t.Fatalf("got %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(c.Config.ConfigDir, "printer.cfg"))
	if string(data) != "original\n" {
		t.Fatal("mutated before revalidation")
	}
	if _, err := os.Stat(c.state("backups")); !os.IsNotExist(err) {
		t.Fatal("backup created before validation")
	}
}

func TestApplyCaptureRoundTripKeepsSecretsLocal(t *testing.T) {
	var captured []File
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "status.txt"):
			io.WriteString(w, "printer_name=Fixture\n")
		case strings.HasSuffix(r.URL.Path, "pull"):
			desired(w, "rev-one", "[printer]\nserial: ${KLIPPEE:serial}\n")
		case strings.HasSuffix(r.URL.Path, "push"):
			var payload struct {
				Files    []File `json:"files"`
				Mode     string `json:"clientMode"`
				Revision string `json:"baseRevision"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			captured = payload.Files
			if payload.Mode != "desktop" || payload.Revision != "rev-one" {
				t.Errorf("wrong capture identity: %+v", payload)
			}
			io.WriteString(w, `{"desired":{"versionNumber":2,"revision":"rev-two"}}`)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	})
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "old\n")
	writeFile(t, c.Config.ConfigDir, "unreferenced.cfg", "do not capture\n")
	if err := c.Variables("set", "serial", "private-serial"); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(context.Background(), true, false, ""); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(filepath.Join(c.Config.ConfigDir, "printer.cfg"))
	if !strings.Contains(string(installed), "private-serial") {
		t.Fatal("variable was not rendered")
	}
	if err := c.Capture(context.Background(), "reviewed"); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || strings.Contains(captured[0].Content, "private-serial") || !strings.Contains(captured[0].Content, "${KLIPPEE:serial}") {
		t.Fatalf("unsafe capture: %+v", captured)
	}
	if strings.Contains(c.Out.(*bytes.Buffer).String(), "private-token") {
		t.Fatal("token printed")
	}
	entries, err := os.ReadDir(c.state("backups"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("missing backup: %v", err)
	}
}

func TestApplyRequiresExplicitReviewAndValidCleanBaseline(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "status.txt") {
			io.WriteString(w, "printer_name=Fixture\n")
			return
		}
		desired(w, "rev-one", "new\n")
	})
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "old\n")
	if err := c.Apply(context.Background(), false, false, ""); err == nil || !strings.Contains(err.Error(), "requires confirmation") {
		t.Fatalf("got %v", err)
	}
	if err := c.Apply(context.Background(), false, true, ""); err == nil || !strings.Contains(err.Error(), "baseline") {
		t.Fatalf("got %v", err)
	}
	if err := c.Apply(context.Background(), true, false, ""); err != nil {
		t.Fatal(err)
	}
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "local edit\n")
	if err := c.Apply(context.Background(), false, true, ""); err == nil || !strings.Contains(err.Error(), "local configuration changed") {
		t.Fatalf("got %v", err)
	}
}

func TestUnsafeRemoteFilesAreRejected(t *testing.T) {
	for _, path := range []string{"../secret.cfg", ".ssh/key.cfg", "C:/escape.cfg", "sub/../escape.cfg", "klippee_variables.cfg", "password.cfg"} {
		t.Run(path, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"version": map[string]any{"version_number": 1, "revision": "r", "files": []File{{Path: path, Content: "secret"}}}})
			})
			if _, err := c.snapshot(context.Background()); err == nil {
				t.Fatal("accepted unsafe file")
			}
		})
	}
}

func TestDesktopApplyNeverAdvancesPhysicalAppliedState(t *testing.T) {
	applied := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "status.txt"):
			io.WriteString(w, "printer_name=Fixture\n")
		case strings.HasSuffix(r.URL.Path, "pull"):
			desired(w, "r", "new\n")
		default:
			applied = true
			io.WriteString(w, "{}")
		}
	})
	if err := c.Apply(context.Background(), true, false, ""); err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("desktop advanced applied state")
	}
}

func TestInitAndLoadExistingConnection(t *testing.T) {
	folder := t.TempDir()
	cfg := Config{BaseURL: "https://klippee.xyz", RepositoryID: "one", Token: "private-token", ConfigDir: folder, ClientMode: "desktop"}
	if err := Init(context.Background(), cfg, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(context.Background(), filepath.Join(folder, ".klippee", "config.json"), folder)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != cfg {
		t.Fatalf("got %+v", loaded)
	}
	if _, err := Load(context.Background(), filepath.Join(folder, ".klippee", "config.json"), t.TempDir()); err == nil {
		t.Fatal("accepted wrong workspace")
	}
}

func TestCommandUsesNativeClientWithoutHostInventory(t *testing.T) {
	folder := t.TempDir()
	t.Setenv("KLIPPEE_REPOSITORY_ID", "repo")
	t.Setenv("KLIPPEE_TOKEN", "private-token")
	t.Setenv("KLIPPEE_BASE_URL", "https://klippee.xyz")
	var out bytes.Buffer
	cmd := NewCommand(strings.NewReader(""), &out, io.Discard)
	cmd.SetArgs([]string{"--workspace", folder, "init", "--desktop"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	cmd = NewCommand(strings.NewReader(""), &out, io.Discard)
	cmd.SetArgs([]string{"--workspace", folder, "variables", "set", "serial", "safe"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(folder, variablesName))
	if err != nil || !strings.Contains(string(data), "# KLIPPEE_VARIABLE serial=safe") {
		t.Fatalf("%s %v", data, err)
	}
}

func TestCaptureNeverTransmitsModifiedRenderedSecretLine(t *testing.T) {
	requests := 0
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests++; http.Error(w, "should not upload", 500) })
	files := []File{{Path: "printer.cfg", Content: "serial: ${KLIPPEE:serial}\n"}}
	if err := c.writeTemplates(files, []File{{Path: "printer.cfg", Content: "serial: secret-value\n"}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "serial: secret-value # modified\n")
	if err := c.Capture(context.Background(), ""); err == nil {
		t.Fatal("captured edited private line")
	}
	if requests != 0 {
		t.Fatal("transmitted secret")
	}
}

func TestApplyRefusesLocalEditsDuringRemoteRevalidation(t *testing.T) {
	var c *Client
	calls := 0
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "status.txt") {
			io.WriteString(w, "printer_name=Fixture\n")
			return
		}
		calls++
		if calls == 2 {
			writeFile(t, c.Config.ConfigDir, "printer.cfg", "edited during review\n")
		}
		desired(w, "same-revision", "desired\n")
	})
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "original\n")
	if err := c.Apply(context.Background(), true, false, ""); err == nil || !strings.Contains(err.Error(), "local configuration changed") {
		t.Fatalf("got %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(c.Config.ConfigDir, "printer.cfg"))
	if string(data) != "edited during review\n" {
		t.Fatal("overwrote concurrent edit")
	}
}

func TestCaptureBaselineDescribesUploadedSnapshot(t *testing.T) {
	var c *Client
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeFile(t, c.Config.ConfigDir, "printer.cfg", "edited during upload\n")
		io.WriteString(w, `{"desired":{"versionNumber":2,"revision":"captured"}}`)
	})
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "captured content\n")
	if err := c.Capture(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	changes, err := c.localChanges()
	if err != nil || len(changes) != 1 || changes[0] != "printer.cfg" {
		t.Fatalf("lost concurrent edit: %v %v", changes, err)
	}
}

func TestCaptureExcludesSensitiveAndUnreferencedFiles(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Files []File `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.Files) != 2 || payload.Files[0].Path != "macros/print.cfg" || payload.Files[1].Path != "printer.cfg" {
			t.Errorf("unsafe include graph: %+v", payload.Files)
		}
		io.WriteString(w, `{"desired":{"versionNumber":1,"revision":"capture"}}`)
	})
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "[include macros/*.cfg]\n[include secrets.cfg]\n")
	writeFile(t, c.Config.ConfigDir, "macros/print.cfg", "[gcode_macro PRINT]\n")
	for _, name := range []string{"secrets.cfg", "unreferenced.cfg", "saved_variables.cfg", "macros/printer-20260101_010101.cfg", "macros/access_token.cfg"} {
		writeFile(t, c.Config.ConfigDir, name, "secret\n")
	}
	if err := c.Capture(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestPhysicalApplyConfirmsTemplatesWithoutLocalValues(t *testing.T) {
	confirmed := false
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "status.txt"):
			io.WriteString(w, "printer_name=Fixture\n")
		case strings.HasSuffix(r.URL.Path, "pull"):
			desired(w, "revision", "serial: ${KLIPPEE:serial}\n")
		case strings.HasSuffix(r.URL.Path, "host-info"):
			io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "applied"):
			data, _ := io.ReadAll(r.Body)
			if strings.Contains(string(data), "private-serial") || !strings.Contains(string(data), "${KLIPPEE:serial}") || !strings.Contains(string(data), `"desiredRevision":"revision"`) {
				t.Errorf("invalid confirmation: %s", data)
			}
			confirmed = true
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	c.Config.ClientMode = "printer"
	if err := c.Variables("set", "serial", "private-serial"); err != nil {
		t.Fatal(err)
	}
	if err := c.Apply(context.Background(), true, false, ""); err != nil {
		t.Fatal(err)
	}
	if !confirmed {
		t.Fatal("physical snapshot was not confirmed")
	}
}

func TestAuthenticatedRequestsNeverFollowRedirects(t *testing.T) {
	forwarded := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true }))
	defer other.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	})
	if _, err := c.api(context.Background(), "push", map[string]string{"message": "private"}); err == nil {
		t.Fatal("accepted redirect")
	}
	if forwarded {
		t.Fatal("replayed authenticated request to another service")
	}
}

func TestOverlappingConfigurationMutationsAreRefused(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "status.txt") {
			io.WriteString(w, "printer_name=Fixture\n")
			return
		}
		once.Do(func() { close(started); <-release })
		desired(w, "revision", "desired\n")
	})
	result := make(chan error, 1)
	go func() { result <- c.Apply(context.Background(), true, false, "") }()
	select {
	case <-started:
	case err := <-result:
		t.Fatalf("apply failed before review: %v", err)
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("apply never requested snapshot")
	}
	err := c.Capture(context.Background(), "overlap")
	close(release)
	if err == nil || !strings.Contains(err.Error(), "cannot lock") {
		t.Errorf("concurrent capture was not refused: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestLocalRecoveryBacksUpEveryOverwrittenPath(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { t.Errorf("local recovery contacted cloud") })
	writeFile(t, c.Config.ConfigDir, "printer.cfg", "[printer]\n")
	writeFile(t, c.Config.ConfigDir, "unreferenced.cfg", "current local content\n")
	writeFile(t, c.Config.ConfigDir, ".klippee/backups/fixture/unreferenced.cfg", "recovered content\n")
	if err := c.Recover(context.Background(), "local:fixture"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(c.Config.ConfigDir, "unreferenced.cfg"))
	if err != nil || string(data) != "recovered content\n" {
		t.Fatalf("restore failed: %s %v", data, err)
	}
	entries, err := os.ReadDir(c.state("backups"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name() == "fixture" {
			continue
		}
		data, err := os.ReadFile(c.state(filepath.Join("backups", entry.Name(), "unreferenced.cfg")))
		if err == nil && string(data) == "current local content\n" {
			found = true
		}
	}
	if !found {
		t.Fatal("overwritten unreferenced file was not backed up")
	}
}
