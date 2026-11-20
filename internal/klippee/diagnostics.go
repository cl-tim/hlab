package klippee

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/buildinfo"
)

const maxDiagnosticUpload = 16 << 20

type diagnosticArtifact struct {
	Name      string `json:"archiveName"`
	Kind      string `json:"kind"`
	Original  int64  `json:"originalSizeBytes"`
	Stored    int64  `json:"storedSizeBytes"`
	Truncated bool   `json:"truncated"`
}

type diagnosticBundle struct {
	zip       *zip.Writer
	artifacts []diagnosticArtifact
	warnings  []string
	original  int64
	truncated bool
}

func (b *diagnosticBundle) add(name, kind string, source io.Reader, original, max int64) error {
	entry, err := b.zip.Create(name)
	if err != nil {
		return err
	}
	n, err := io.Copy(entry, io.LimitReader(source, max))
	if err != nil {
		return err
	}
	truncated := n < original
	b.artifacts = append(b.artifacts, diagnosticArtifact{name, kind, original, n, truncated})
	b.original += original
	b.truncated = b.truncated || truncated
	return nil
}

func (b *diagnosticBundle) addLog(path, name, kind string, max int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("%s is not a nonempty regular log", path)
	}
	if info.Size() > max {
		if strings.HasSuffix(name, ".gz") {
			return fmt.Errorf("compressed log exceeds the collection limit")
		}
		if _, err := file.Seek(info.Size()-max, io.SeekStart); err != nil {
			return err
		}
	}
	return b.add(name, kind, file, info.Size(), max)
}

func regularLog(path string) (os.FileInfo, bool) {
	info, err := os.Stat(path)
	return info, err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func (c *Client) diagnosticLog(path string, previous bool) (string, error) {
	if path != "" {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	candidates := []string{os.Getenv("KLIPPEE_LOG_PATH"), filepath.Join(filepath.Dir(c.Config.ConfigDir), "logs", "klippy.log"),
		filepath.Join(home, "printer_data", "logs", "klippy.log"), filepath.Join(home, "klipper_logs", "klippy.log"), "/tmp/klippy.log"}
	for _, candidate := range candidates {
		if previous {
			if candidate == "" {
				continue
			}
			matches, err := filepath.Glob(candidate + ".*")
			if err != nil {
				return "", err
			}
			var latest string
			var modified time.Time
			for _, match := range matches {
				if info, ok := regularLog(match); ok && (latest == "" || info.ModTime().After(modified)) {
					latest, modified = match, info.ModTime()
				}
			}
			if latest != "" {
				return latest, nil
			}
		} else if _, ok := regularLog(candidate); ok {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a Klippy log; supply its path or set KLIPPEE_LOG_PATH")
}

func (b *diagnosticBundle) addJSON(name, kind string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return b.add(name, kind, bytes.NewReader(data), int64(len(data)), int64(len(data)))
}

func (c *Client) UploadDiagnostics(ctx context.Context, path string, previous bool) error {
	if c.Config.ClientMode != "printer" {
		return fmt.Errorf("diagnostic upload requires a printer connection")
	}
	source, err := c.diagnosticLog(path, previous)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp("", "hlab-klippee-diagnostics-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	b := &diagnosticBundle{zip: zip.NewWriter(file), warnings: []string{}}
	primary := "logs/klippy.log"
	if strings.EqualFold(filepath.Ext(source), ".gz") {
		primary += ".gz"
	}
	if err := b.addLog(source, primary, "klippy-log", 64<<20); err != nil {
		return err
	}
	support := filepath.Join(filepath.Dir(source), "moonraker.log")
	if _, ok := regularLog(support); ok && !samePath(source, support) {
		if err := b.addLog(support, "logs/moonraker.log", "moonraker-log", 8<<20); err != nil {
			return err
		}
	} else {
		b.warnings = append(b.warnings, "moonraker.log was not found beside the Klippy log.")
	}
	if err := c.collectRuntimeDiagnostics(ctx, b); err != nil {
		return err
	}
	now := time.Now().UTC()
	manifest := map[string]any{
		"schema": "https://klippee.xyz/schemas/diagnostic-bundle-v2.json", "schemaVersion": 2,
		"artifacts": b.artifacts, "generatedAt": now.Format(time.RFC3339), "collectionWarnings": b.warnings,
		"client": map[string]string{"mode": "printer", "repositoryId": c.Config.RepositoryID, "version": buildinfo.Version},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	entry, err := b.zip.Create("manifest.json")
	if err != nil {
		return err
	}
	if _, err := entry.Write(data); err != nil {
		return err
	}
	if err := b.zip.Close(); err != nil {
		return err
	}
	size, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if size > maxDiagnosticUpload {
		return fmt.Errorf("compressed diagnostic bundle exceeds the 16 MiB upload limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	truncated := "0"
	if b.truncated {
		truncated = "1"
	}
	headers := http.Header{}
	headers.Set("Content-Type", "application/zip")
	headers.Set("X-Klippee-Log-Name", "klippee-diagnostics-"+now.Format("20060102T150405Z")+".zip")
	headers.Set("X-Klippee-Original-Size", strconv.FormatInt(b.original, 10))
	headers.Set("X-Klippee-Truncated", truncated)
	fmt.Fprintf(c.output(), "klippee: collected %d diagnostic artifacts\n", len(b.artifacts))
	for _, warning := range b.warnings {
		fmt.Fprintf(c.output(), "klippee: warning: %s\n", warning)
	}
	archive, err := io.ReadAll(io.LimitReader(file, maxDiagnosticUpload+1))
	if err != nil {
		return err
	}
	response, err := c.request(ctx, http.MethodPost, "diagnostics", bytes.NewReader(archive), headers)
	if err != nil {
		return err
	}
	var uploaded struct{ URL string }
	if err := json.Unmarshal(response, &uploaded); err != nil {
		return fmt.Errorf("invalid diagnostic upload response")
	}
	u, err := url.Parse(uploaded.URL)
	origin, originErr := url.Parse(c.Config.BaseURL)
	if err != nil || originErr != nil || u.Scheme != origin.Scheme || u.Host != origin.Host || u.User != nil {
		return fmt.Errorf("diagnostic upload response did not include its private URL")
	}
	fmt.Fprintf(c.output(), "klippee: diagnostic bundle uploaded privately for 48 hours\nklippee: open %s\nklippee: DIAGNOSTIC UPLOAD SUCCESSFUL\n", u)
	return nil
}

func (c *Client) hostInfo(printer, server json.RawMessage) map[string]string {
	info := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "cliVersion": buildinfo.Version}
	info["hostname"], _ = os.Hostname()
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		if pretty := strings.Trim(keyValues(data)["PRETTY_NAME"], "\"'"); pretty != "" {
			info["os"] = pretty
		}
	}
	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		info["kernel"] = strings.TrimSpace(string(data))
	}
	var p struct {
		Version string `json:"software_version"`
		State   string
	}
	var s struct {
		Version string `json:"moonraker_version"`
	}
	_ = json.Unmarshal(printer, &p)
	_ = json.Unmarshal(server, &s)
	info["klipperVersion"], info["klippyState"], info["moonrakerVersion"] = p.Version, p.State, s.Version
	return info
}

func (c *Client) reportHostInfo(ctx context.Context) {
	if c.Config.ClientMode != "printer" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	printer, _ := c.moonrakerCall(ctx, "printer.info", nil)
	server, _ := c.moonrakerCall(ctx, "server.info", nil)
	if _, err := c.api(ctx, "host-info", map[string]any{"hostInfo": c.hostInfo(printer, server)}); err != nil && c.ErrOut != nil {
		fmt.Fprintln(c.ErrOut, "klippee: warning: could not report host info:", err)
	}
}

var (
	slotVariable = regexp.MustCompile(`^slot(?:1[0-5]|[0-9])$`)
	slotValue    = regexp.MustCompile(`^slot(?:-1|1[0-5]|[0-9])$`)
	retryStep    = regexp.MustCompile(`^QDE_[0-9]{3}_[0-9]{3}_[0-9]{2}$`)
)

func safeDiagnosticVariables(raw json.RawMessage) (map[string]any, error) {
	var result struct {
		Status struct {
			Save struct{ Variables map[string]json.RawMessage } `json:"save_variables"`
		}
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.Status.Save.Variables == nil {
		return nil, fmt.Errorf("invalid save_variables snapshot")
	}
	selected := map[string]json.RawMessage{}
	for name, value := range result.Status.Save.Variables {
		var text string
		number, numberErr := strconv.Atoi(string(value))
		switch name {
		case "auto_init_detect", "auto_read_rfid", "auto_reload_detect", "enable_box", "is_tool_change", "qidi_box_auto_insert":
			if string(value) == "true" || string(value) == "false" || (numberErr == nil && (number == 0 || number == 1)) {
				selected[name] = value
			}
		case "last_load_slot", "slot_sync":
			if json.Unmarshal(value, &text) == nil && slotValue.MatchString(text) {
				selected[name] = value
			}
		case "retry_step":
			if string(value) == "null" || (json.Unmarshal(value, &text) == nil && retryStep.MatchString(text)) {
				selected[name] = value
			}
		default:
			if (name == "box_count" || name == "extrude_state" || name == "load_retry_num" || slotVariable.MatchString(name)) && numberErr == nil && number >= -128 && number <= 128 {
				selected[name] = value
			}
		}
	}
	return map[string]any{"schema": "klippee-safe-save-variables/v1", "variables": selected,
		"omitted_variable_count": len(result.Status.Save.Variables) - len(selected)}, nil
}
