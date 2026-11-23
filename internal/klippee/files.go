package klippee

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const variablesName = "klippee_variables.cfg"
const variablePrefix = "# KLIPPEE_VARIABLE "
const variablesHeader = "# Klippee local variable values. Edit the entries below.\n# Format: # KLIPPEE_VARIABLE name=value\n# This file stays on this computer and is never included by hlab klippee capture.\n# Every entry is a comment so broad Klipper include patterns remain safe.\n"

var variableName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var placeholder = regexp.MustCompile(`\$\{KLIPPEE:([a-z][a-z0-9_]*)\}`)
var sensitiveStem = regexp.MustCompile(`(?i)(^|[-_])(auth|credentials?|passwords?|secrets?|tokens?)([-_.]|$)`)
var saveSnapshot = regexp.MustCompile(`(?i)^printer-[0-9]+_[0-9]+\.cfg$`)
var includeLine = regexp.MustCompile(`(?i)^\s*\[include\s+(.+?)\]\s*(#.*)?$`)

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Role    string `json:"role"`
	Public  bool   `json:"public"`
}

func safePath(path string) bool {
	if path == "" || strings.ContainsAny(path, "\\:\x00\r\n|") || strings.HasPrefix(path, "/") || !strings.HasSuffix(strings.ToLower(path), ".cfg") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.TrimRight(part, " .") != part {
			return false
		}
		switch strings.ToLower(part) {
		case "backup", "backups", "archive", "archives":
			return false
		}
		for _, char := range part {
			if unicode.IsControl(char) || unicode.Is(unicode.Mn, char) || unicode.Is(unicode.Mc, char) {
				return false
			}
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return false
		}
	}
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "auth.cfg", "authorization.cfg", "credentials.cfg", "credential.cfg", variablesName, "mooncord.cfg", "moonraker.cfg", "mobileraker.cfg", "password.cfg", "passwords.cfg", "saved_variables.cfg", "secret.cfg", "secrets.cfg", "telegram.cfg", "token.cfg", "tokens.cfg":
		return false
	}
	return !sensitiveStem.MatchString(base) && !saveSnapshot.MatchString(base)
}

// managedPath rejects links in the complete write path, including local state.
func managedPath(root, relative string) (string, error) {
	if !safePath(relative) {
		return "", fmt.Errorf("unsafe config path: %s", relative)
	}
	current := root
	if err := realPath(root); err != nil {
		return "", err
	}
	for _, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		if _, err := os.Lstat(current); err == nil {
			if err := realPath(current); err != nil {
				return "", err
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return current, nil
}

func captureSafe(root, relative string) bool {
	_, err := captureSource(root, relative)
	return err == nil
}

// A trusted alias may point to Klipper add-ons outside the workspace. Inspect
// every link in the chain so an innocent alias cannot hide private state.
func captureSource(root, relative string) (string, error) {
	if !safePath(relative) {
		return "", fmt.Errorf("unsafe configuration path: %s", relative)
	}
	if err := realPath(root); err != nil {
		return "", err
	}
	current := root
	pending := strings.Split(relative, "/")
	links := 0
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			current = filepath.Dir(current)
			continue
		}
		lower := strings.ToLower(part)
		if strings.HasPrefix(part, ".") || sensitiveStem.MatchString(lower) || saveSnapshot.MatchString(lower) ||
			lower == "backup" || lower == "backups" || lower == "archive" || lower == "archives" ||
			(strings.HasSuffix(lower, ".cfg") && !safePath(part)) {
			return "", fmt.Errorf("configuration path %s points to private or backup state", relative)
		}
		next := filepath.Join(current, part)
		info, err := os.Lstat(next)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		links++
		if links > 40 {
			return "", fmt.Errorf("too many links in configuration path %s", relative)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			volume := filepath.VolumeName(target)
			current = volume + string(filepath.Separator)
			target = strings.TrimPrefix(target, volume)
		}
		pending = append(strings.Split(filepath.ToSlash(target), "/"), pending...)
	}
	if err := realPath(current); err != nil {
		return "", err
	}
	info, err := os.Stat(current)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(current), ".cfg") {
		return "", fmt.Errorf("configuration path %s does not point to a regular .cfg file", relative)
	}
	return current, nil
}

func workspaceFile(root, relative string) (string, error) {
	path, err := captureSource(root, relative)
	if err != nil {
		return "", err
	}
	return readConfigFile(path)
}

func configPaths(root string) ([]string, error) {
	seen := map[string]bool{}
	queue := []string{"printer.cfg"}
	if _, err := os.Stat(filepath.Join(root, "printer.cfg")); os.IsNotExist(err) {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				relative, _ := filepath.Rel(root, path)
				relative = filepath.ToSlash(relative)
				if captureSafe(root, relative) {
					seen[relative] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	} else {
		for len(queue) > 0 {
			relative := queue[0]
			queue = queue[1:]
			if seen[relative] || !captureSafe(root, relative) {
				continue
			}
			seen[relative] = true
			data, err := workspaceFile(root, relative)
			if err != nil {
				return nil, err
			}
			for _, line := range strings.Split(data, "\n") {
				m := includeLine.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				pattern := strings.TrimSpace(m[1])
				if filepath.IsAbs(pattern) || strings.Contains(pattern, "\\") {
					continue
				}
				matches, err := filepath.Glob(filepath.Join(root, filepath.Dir(relative), pattern))
				if err != nil {
					return nil, fmt.Errorf("invalid include in %s: %w", relative, err)
				}
				for _, path := range matches {
					found, err := filepath.Rel(root, path)
					if err == nil {
						queue = append(queue, filepath.ToSlash(found))
					}
				}
			}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (c *Client) state(name string) string {
	return filepath.Join(c.Config.ConfigDir, ".klippee", name)
}

func (c *Client) lock() (func(), error) {
	if err := realPath(c.Config.ConfigDir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(c.state(""), 0700); err != nil {
		return nil, err
	}
	if err := realPath(c.state("")); err != nil {
		return nil, err
	}
	path := c.state("native-sync.lock")
	if err := realPath(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := openSyncLock(path)
	if err != nil {
		return nil, fmt.Errorf("cannot lock Klippee workspace; another sync may be running: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
func readOptional(path string) ([]byte, error) {
	if err := realPath(filepath.Dir(path)); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := realPath(path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 20<<20 {
		return nil, fmt.Errorf("invalid state file: %s", path)
	}
	return os.ReadFile(path)
}
func keyValues(data []byte) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, ok := strings.Cut(line, "=")
		if ok {
			values[strings.TrimSpace(name)] = strings.TrimSpace(value)
		}
	}
	return values
}
func (c *Client) metadata() (map[string]string, error) {
	data, err := readOptional(c.state("last-sync"))
	return keyValues(data), err
}
func (c *Client) writeMetadata(values map[string]string) error {
	values["schema"] = "1"
	values["repository_id"] = c.Config.RepositoryID
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, strings.NewReplacer("\r", " ", "\n", " ").Replace(values[k]))
	}
	return atomicWrite(c.state("last-sync"), []byte(b.String()), 0600)
}
func checksum(data []byte) string { return fmt.Sprintf("sha256:%x:%d", sha256.Sum256(data), len(data)) }
func (c *Client) manifest() (map[string]string, error) {
	data, err := readOptional(c.state("last-sync-files"))
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		digest, path, ok := strings.Cut(line, "|")
		if !ok || !safePath(path) || values[path] != "" {
			return nil, fmt.Errorf("invalid saved sync baseline")
		}
		values[path] = digest
	}
	return values, nil
}
func (c *Client) writeBaseline(paths []string) error {
	var b strings.Builder
	for _, path := range paths {
		data, err := workspaceFile(c.Config.ConfigDir, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s|%s\n", checksum([]byte(data)), path)
	}
	return atomicWrite(c.state("last-sync-files"), []byte(b.String()), 0600)
}

func (c *Client) writeCapturedBaseline(files []File) error {
	var b strings.Builder
	for _, file := range files {
		fmt.Fprintf(&b, "%s|%s\n", checksum([]byte(file.Content)), file.Path)
	}
	return atomicWrite(c.state("last-sync-files"), []byte(b.String()), 0600)
}
func (c *Client) localChanges() ([]string, error) {
	manifest, err := c.manifest()
	if err != nil {
		return nil, err
	}
	paths, err := configPaths(c.Config.ConfigDir)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	for path, digest := range manifest {
		data, err := workspaceFile(c.Config.ConfigDir, path)
		if err != nil || checksum([]byte(data)) != digest {
			changed[path] = true
		}
	}
	for _, path := range paths {
		if manifest[path] == "" {
			changed[path] = true
		}
	}
	result := []string{}
	for path := range changed {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}
func (c *Client) requireClean() error {
	meta, err := c.metadata()
	if err != nil {
		return err
	}
	manifest, err := c.manifest()
	if err != nil {
		return err
	}
	if meta["schema"] != "1" || meta["repository_id"] != c.Config.RepositoryID || meta["revision"] == "" || len(manifest) == 0 {
		return fmt.Errorf("apply --if-clean requires an existing valid sync baseline")
	}
	changed, err := c.localChanges()
	if err != nil {
		return err
	}
	if len(changed) > 0 {
		return fmt.Errorf("apply --if-clean refused: local configuration changed: %s", strings.Join(changed, ", "))
	}
	return nil
}

func (c *Client) backup(paths []string) (string, error) {
	root := c.state("backups")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if err := realPath(root); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(root, "manual-"+time.Now().UTC().Format("20060102-150405")+"-")
	if err != nil {
		return "", err
	}
	for _, relative := range paths {
		data, err := workspaceFile(c.Config.ConfigDir, relative)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if err := atomicWrite(filepath.Join(dir, filepath.FromSlash(relative)), []byte(data), 0600); err != nil {
			return "", err
		}
	}
	return dir, nil
}

func (c *Client) variableLines() ([]string, error) {
	path := filepath.Join(c.Config.ConfigDir, variablesName)
	data, err := readOptional(path)
	if err != nil {
		return nil, err
	}
	if data == nil {
		legacy, err := readOptional(c.state("variables.env"))
		if err != nil {
			return nil, err
		}
		if legacy != nil {
			data = []byte(variablesHeader)
			for _, line := range strings.Split(string(legacy), "\n") {
				name, value, ok := variableEntry(line)
				if ok {
					data = append(data, []byte(variablePrefix+name+"="+value+"\n")...)
				}
			}
		}
	}
	if len(data) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"), nil
}
func variableEntry(line string) (string, string, bool) {
	line = strings.TrimLeft(strings.TrimSuffix(line, "\r"), " \t")
	if strings.HasPrefix(line, variablePrefix) {
		line = strings.TrimPrefix(line, variablePrefix)
	} else if strings.HasPrefix(line, "#") {
		return "", "", false
	}
	name, value, ok := strings.Cut(line, "=")
	return name, value, ok && variableName.MatchString(name)
}
func (c *Client) Variables(args ...string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] == "set" || args[0] == "unset" {
		unlock, err := c.lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	return c.variableCommand(args)
}

func (c *Client) variableCommand(args []string) error {
	lines, err := c.variableLines()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return fmt.Errorf("variables list accepts no arguments")
		}
		for _, line := range lines {
			fmt.Fprintln(c.output(), line)
		}
		return nil
	case "doctor":
		if len(args) != 1 {
			return fmt.Errorf("variables doctor accepts no arguments")
		}
		for _, line := range lines {
			_, _, ok := variableEntry(line)
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && (!strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, variablePrefix)) && !ok {
				return fmt.Errorf("variables are invalid")
			}
		}
		fmt.Fprintln(c.output(), "Klippee variables look valid")
		return nil
	case "set", "unset":
		want := 2
		if args[0] == "set" {
			want = 3
		}
		if len(args) != want || !variableName.MatchString(args[1]) {
			return fmt.Errorf("variables %s requires a name matching [a-z][a-z0-9_]*%s", args[0], map[bool]string{true: " and value"}[want == 3])
		}
		if want == 3 && strings.ContainsAny(args[2], "\r\n") {
			return fmt.Errorf("variable values must fit on one line")
		}
		if len(lines) == 0 {
			lines = strings.Split(strings.TrimSuffix(variablesHeader, "\n"), "\n")
		}
		kept := []string{}
		for _, line := range lines {
			name, _, ok := variableEntry(line)
			if !ok || name != args[1] {
				kept = append(kept, line)
			}
		}
		if want == 3 {
			kept = append(kept, variablePrefix+args[1]+"="+args[2])
		}
		return atomicWrite(filepath.Join(c.Config.ConfigDir, variablesName), []byte(strings.Join(kept, "\n")+"\n"), 0600)
	default:
		return fmt.Errorf("unknown variables command: %s", args[0])
	}
}

type templateState struct {
	Files map[string]struct {
		Template string `json:"template"`
		Rendered string `json:"rendered"`
	} `json:"files"`
}

func (c *Client) templates() (templateState, error) {
	var state templateState
	data, err := readOptional(c.state("variable-templates.json"))
	if err != nil {
		return state, err
	}
	if data != nil {
		err = json.Unmarshal(data, &state)
	}
	return state, err
}
func (c *Client) writeTemplates(templates, rendered []File) error {
	state := templateState{Files: map[string]struct {
		Template string `json:"template"`
		Rendered string `json:"rendered"`
	}{}}
	for i, file := range templates {
		if placeholder.MatchString(file.Content) {
			state.Files[file.Path] = struct {
				Template string `json:"template"`
				Rendered string `json:"rendered"`
			}{file.Content, rendered[i].Content}
		}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return atomicWrite(c.state("variable-templates.json"), data, 0600)
}

func readConfigFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > 256000 {
		return "", fmt.Errorf("config file exceeds 256000 bytes: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("non-UTF-8 config file: %s", path)
	}
	return string(data), nil
}
