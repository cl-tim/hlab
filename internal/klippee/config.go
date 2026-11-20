// Package klippee implements Klippee configuration sync inside hlab.
package klippee

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Config is compatible with existing Python client connection files.
type Config struct {
	BaseURL      string `json:"base_url"`
	RepositoryID string `json:"repository_id"`
	Token        string `json:"token"`
	ConfigDir    string `json:"config_dir"`
	ClientMode   string `json:"client_mode"`
}

// Client owns one explicitly selected connection; it never searches another
// printer's workspace or changes the process working directory.
type Client struct {
	Config Config
	HTTP   *http.Client
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

func normalizeURL(value string) (string, error) {
	u, err := url.Parse(strings.TrimRight(value, "/"))
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", fmt.Errorf("Klippee API URL must be an HTTP(S) origin without credentials, a path, query, or fragment")
	}
	return u.String(), nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

func realPath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing linked path: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !samePath(resolved, absolute) {
		return fmt.Errorf("refusing linked path: %s", path)
	}
	return nil
}

func gitCheck(ctx context.Context, root string, write bool) error {
	if _, err := os.Lstat(filepath.Join(root, ".klippee", ".git")); err == nil {
		return fmt.Errorf("refusing nested .klippee Git repository")
	}
	_, gitErr := exec.LookPath("git")
	// Fail closed if Git metadata exists but Git cannot verify the credential path.
	for dir := root; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil && gitErr != nil {
			return fmt.Errorf("git is required to verify sync credential privacy")
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	if gitErr != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel")
	if err := cmd.Run(); err != nil {
		return nil
	}
	tracked, err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--", ".klippee").Output()
	if err != nil {
		return fmt.Errorf("verify Git credential tracking: %w", err)
	}
	if len(strings.TrimSpace(string(tracked))) > 0 {
		return fmt.Errorf("refusing Git-tracked .klippee state; remove it from the index and rotate any committed token")
	}
	if write {
		if err := atomicWrite(filepath.Join(root, ".klippee", ".gitignore"), []byte("# Klippee local state contains sync credentials.\n*\n"), 0600); err != nil {
			return err
		}
	}
	if err := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "-q", "--", ".klippee/config.json").Run(); err != nil {
		return fmt.Errorf("Git must ignore .klippee/config.json; rerun hlab klippee init --desktop")
	}
	return nil
}

// Load reads only the specified connection and enforces desktop workspace identity.
func Load(ctx context.Context, path string, workspace string) (Config, error) {
	var c Config
	if err := realPath(filepath.Dir(path)); err != nil {
		return c, err
	}
	if err := realPath(path); err != nil {
		return c, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return c, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return c, fmt.Errorf("invalid Klippee connection file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return c, fmt.Errorf("sync credentials are not private; run chmod 600 %s", path)
	}
	if !ownedByCurrentUser(info) {
		return c, fmt.Errorf("sync credentials must be owned by the current user")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if workspace == "" && filepath.Base(path) == "config" {
		c, err = legacyConnection(data)
		if err != nil {
			return c, err
		}
	} else if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse Klippee connection: %w", err)
	}
	if c.ClientMode == "" {
		c.ClientMode = "printer"
	}
	if c.ClientMode != "desktop" && c.ClientMode != "printer" {
		return c, fmt.Errorf("invalid client_mode")
	}
	if c.RepositoryID == "" || c.Token == "" || c.ConfigDir == "" || strings.ContainsAny(c.Token, "\r\n") {
		return c, fmt.Errorf("connection requires repository_id, token, and config_dir")
	}
	c.BaseURL, err = normalizeURL(c.BaseURL)
	if err != nil {
		return c, err
	}
	if !filepath.IsAbs(c.ConfigDir) {
		return c, fmt.Errorf("connection requires absolute config_dir")
	}
	if workspace == "" && c.ClientMode == "desktop" {
		if !samePath(path, filepath.Join(c.ConfigDir, ".klippee", "config.json")) {
			return c, fmt.Errorf("desktop connection must be stored in the selected workspace")
		}
		workspace = c.ConfigDir
	}
	if workspace != "" {
		if c.ClientMode != "desktop" || !samePath(c.ConfigDir, workspace) {
			return c, fmt.Errorf("workspace connection must use desktop mode and point to this workspace")
		}
		if err := gitCheck(ctx, workspace, false); err != nil {
			return c, err
		}
	}
	return c, nil
}

func legacyConnection(data []byte) (Config, error) {
	config := Config{ClientMode: "printer"}
	fields := map[string]*string{
		"KLIPPEE_BASE_URL": &config.BaseURL, "KLIPPEE_REPOSITORY_ID": &config.RepositoryID,
		"KLIPPEE_TOKEN": &config.Token, "KLIPPEE_CONFIG_DIR": &config.ConfigDir,
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, input, ok := strings.Cut(strings.TrimSpace(line), "=")
		field := fields[key]
		if !ok || field == nil {
			continue
		}
		value, ok := legacyValue(input)
		if !ok {
			return Config{}, fmt.Errorf("invalid legacy Klippee connection field %s", key)
		}
		*field = value
	}
	if strings.HasPrefix(config.ConfigDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return Config{}, err
		}
		config.ConfigDir = filepath.Join(home, config.ConfigDir[2:])
	}
	return config, nil
}

// Interpret the single shell word written by the old installer. Treat
// interpolation characters as data and never invoke a shell.
func legacyValue(input string) (string, bool) {
	var result strings.Builder
	var quote byte
	for i := 0; i < len(input); i++ {
		b := input[i]
		switch {
		case quote != 0 && b == quote:
			quote = 0
		case quote == 0 && (b == '\'' || b == '"'):
			quote = b
		case quote == 0 && (b == ' ' || b == '\t'):
			return "", false
		case quote != '\'' && b == '\\':
			if i+1 == len(input) {
				return "", false
			}
			if quote == 0 || input[i+1] == '"' || input[i+1] == '\\' {
				i++
				b = input[i]
			}
			result.WriteByte(b)
		default:
			result.WriteByte(b)
		}
	}
	return result.String(), quote == 0
}

// Init creates a private connection without transmitting credentials in argv.
func Init(ctx context.Context, c Config, statePath string) error {
	var err error
	c.BaseURL, err = normalizeURL(c.BaseURL)
	if err != nil {
		return err
	}
	if c.Token == "" || c.RepositoryID == "" || strings.ContainsAny(c.Token, "\r\n") {
		return fmt.Errorf("KLIPPEE_TOKEN and KLIPPEE_REPOSITORY_ID are required")
	}
	if c.ClientMode != "desktop" && c.ClientMode != "printer" {
		return fmt.Errorf("choose --desktop or --printer")
	}
	c.ConfigDir, err = filepath.Abs(c.ConfigDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.ConfigDir, 0700); err != nil {
		return err
	}
	if err := realPath(c.ConfigDir); err != nil {
		return err
	}
	localState := filepath.Join(c.ConfigDir, ".klippee")
	if err := os.MkdirAll(localState, 0700); err != nil {
		return err
	}
	if err := realPath(localState); err != nil {
		return err
	}
	client := Client{Config: c}
	unlock, err := client.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if statePath == "" {
		statePath = filepath.Join(c.ConfigDir, ".klippee", "config.json")
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		return err
	}
	if err := realPath(filepath.Dir(statePath)); err != nil {
		return err
	}
	if _, err := os.Lstat(statePath); err == nil {
		if err := realPath(statePath); err != nil {
			return err
		}
	}
	if c.ClientMode == "desktop" {
		if err := gitCheck(ctx, c.ConfigDir, true); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(statePath, append(data, '\n'), 0600)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := realPath(filepath.Dir(path)); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err := realPath(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hlab-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func (c *Client) output() io.Writer {
	if c.Out == nil {
		return io.Discard
	}
	return c.Out
}
func (c *Client) api(ctx context.Context, suffix string, payload any) ([]byte, error) {
	var body io.Reader
	method := "GET"
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = strings.NewReader(string(data))
		method = "POST"
	}
	headers := http.Header{}
	if payload != nil {
		headers.Set("Content-Type", "application/json")
	}
	return c.request(ctx, method, suffix, body, headers)
}

func (c *Client) request(ctx context.Context, method, suffix string, body io.Reader, headers http.Header) ([]byte, error) {
	address := c.Config.BaseURL + "/api/sync/repos/" + url.PathEscape(c.Config.RepositoryID) + "/" + suffix
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		req.Header = headers.Clone()
	}
	req.Header.Set("Authorization", "Bearer "+c.Config.Token)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	// Authenticated sync must not follow a redirect to another service or replay a mutation.
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Klippee request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 20<<20 {
		return nil, fmt.Errorf("Klippee response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var detail struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &detail)
		return nil, fmt.Errorf("Klippee HTTP %d: %s", resp.StatusCode, strings.ReplaceAll(detail.Error, c.Config.Token, "[redacted]"))
	}
	return data, nil
}
