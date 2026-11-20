package klippee

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

type version struct {
	Number   int    `json:"version_number"`
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Source   string `json:"source"`
	Created  string `json:"created_at"`
	Message  string `json:"message"`
	Files    []File `json:"files"`
}
type snapshot struct {
	Version  version
	Rendered []File
}
type change struct{ Action, Path string }

func (c *Client) snapshot(ctx context.Context) (snapshot, error) {
	var s snapshot
	data, err := c.api(ctx, "pull", nil)
	if err != nil {
		return s, err
	}
	var payload struct {
		Version *version `json:"version"`
		Printer struct {
			Values map[string]any `json:"variableValues"`
		} `json:"printer"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return s, fmt.Errorf("invalid desired configuration: %w", err)
	}
	if payload.Version == nil || payload.Version.Files == nil || payload.Version.Number < 0 || payload.Version.Revision == "" || len(payload.Version.Files) > 64 {
		return s, fmt.Errorf("invalid desired configuration")
	}
	s.Version = *payload.Version
	values := map[string]string{}
	for name, value := range payload.Printer.Values {
		if !variableName.MatchString(name) {
			continue
		}
		switch value.(type) {
		case string, bool, float64:
			values[name] = fmt.Sprint(value)
		}
	}
	lines, err := c.variableLines()
	if err != nil {
		return s, err
	}
	for _, line := range lines {
		name, value, ok := variableEntry(line)
		if ok {
			values[name] = value
		}
	}
	seen := map[string]bool{}
	missing := map[string]bool{}
	for _, file := range s.Version.Files {
		key := strings.ToLower(file.Path)
		if !safePath(file.Path) || seen[key] || len(file.Content) > 256000 {
			return s, fmt.Errorf("unsafe, colliding, or oversized desired file: %s", file.Path)
		}
		seen[key] = true
		file.Content = placeholder.ReplaceAllStringFunc(file.Content, func(match string) string {
			name := placeholder.FindStringSubmatch(match)[1]
			if values[name] == "" {
				missing[name] = true
				return match
			}
			return values[name]
		})
		s.Rendered = append(s.Rendered, file)
	}
	if len(missing) > 0 {
		names := []string{}
		for name := range missing {
			names = append(names, name)
		}
		sort.Strings(names)
		return s, fmt.Errorf("cannot render config; set missing values with hlab klippee variables set: %s", strings.Join(names, ", "))
	}
	return s, nil
}

func (c *Client) status(ctx context.Context) (map[string]string, error) {
	data, err := c.api(ctx, "status.txt", nil)
	if err != nil {
		return nil, err
	}
	result := keyValues(data)
	if len(result) == 0 {
		return nil, fmt.Errorf("server returned invalid target information")
	}
	return result, nil
}
func (c *Client) targetSummary(status map[string]string) {
	name := status["printer_name"]
	if name == "" {
		name = "Unidentified printer"
	}
	id := status["printer_id"]
	if id == "" {
		id = c.Config.RepositoryID
	}
	role := "desktop workspace"
	if c.Config.ClientMode == "printer" {
		role = "physical printer"
	}
	fmt.Fprintf(c.output(), "Target:  %s (%s)\nService: %s\nRole:    %s\nFolder:  %s\n", name, id, c.Config.BaseURL, role, c.Config.ConfigDir)
}
func (c *Client) Target(ctx context.Context) error {
	status, err := c.status(ctx)
	if err != nil {
		return err
	}
	c.targetSummary(status)
	fmt.Fprintf(c.output(), "Applied: %s\nDesired: %s\nPending: %s\n", status["applied_version"], status["target_version"], status["target_pending"])
	return nil
}

// diffContent excludes only volatile SAVE_CONFIG calibration sections.
func diffContent(content string) string {
	lines := strings.SplitAfter(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	marker := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "#*#") && strings.Contains(strings.ToUpper(line), "SAVE_CONFIG") {
			marker = i
			break
		}
	}
	if marker < 0 {
		return strings.Join(lines, "")
	}
	section := false
	skip := false
	kept := []string{}
	preamble := []string{}
	for _, line := range lines[marker:] {
		trim := strings.TrimSpace(strings.TrimPrefix(line, "#*#"))
		if strings.HasPrefix(line, "#*#") && strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			section = true
			name := strings.ToLower(strings.Trim(trim, "[]"))
			skip = strings.HasPrefix(name, "beacon model ") || strings.HasPrefix(name, "bed_mesh ")
		}
		if !section {
			preamble = append(preamble, line)
		} else if !skip && trim != "" {
			kept = append(kept, line)
		}
	}
	if !section {
		return strings.Join(lines, "")
	}
	if len(kept) == 0 {
		return strings.Join(lines[:marker], "")
	}
	return strings.Join(append(append(lines[:marker:marker], preamble...), kept...), "")
}

func (c *Client) changes(files []File) ([]change, error) {
	paths, err := configPaths(c.Config.ConfigDir)
	if err != nil {
		return nil, err
	}
	manifest, err := c.manifest()
	if err != nil {
		return nil, err
	}
	local := map[string]bool{}
	for _, p := range paths {
		local[p] = true
	}
	for p := range manifest {
		if _, err := os.Stat(filepath.Join(c.Config.ConfigDir, filepath.FromSlash(p))); err == nil {
			local[p] = true
		}
	}
	changes := []change{}
	for _, file := range files {
		data, err := workspaceFile(c.Config.ConfigDir, file.Path)
		if os.IsNotExist(err) {
			changes = append(changes, change{"add", file.Path})
		} else if err != nil {
			return nil, err
		} else if diffContent(data) != diffContent(file.Content) {
			changes = append(changes, change{"update", file.Path})
		}
		delete(local, file.Path)
	}
	for path := range local {
		changes = append(changes, change{"remove", path})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}
func (c *Client) printChanges(changes []change) {
	for _, ch := range changes {
		fmt.Fprintf(c.output(), "  %s %s\n", ch.Action, ch.Path)
	}
}

func writeUnifiedDiff(out io.Writer, before, after string) {
	lines := func(content string) []string {
		result := strings.SplitAfter(content, "\n")
		if result[len(result)-1] == "" {
			return result[:len(result)-1]
		}
		return result
	}
	old, new := lines(before), lines(after)
	prefix, suffix := 0, 0
	for prefix < min(len(old), len(new)) && old[prefix] == new[prefix] {
		prefix++
	}
	for suffix < min(len(old), len(new))-prefix && old[len(old)-1-suffix] == new[len(new)-1-suffix] {
		suffix++
	}
	begin := max(0, prefix-3)
	contextLines := min(3, suffix)
	oldCount := len(old) - suffix + contextLines - begin
	newCount := len(new) - suffix + contextLines - begin
	rangeStart := func(count int) int {
		if count == 0 {
			return begin
		}
		return begin + 1
	}
	fmt.Fprintf(out, "@@ -%d,%d +%d,%d @@\n", rangeStart(oldCount), oldCount, rangeStart(newCount), newCount)
	writeLines := func(marker string, selected []string) {
		for _, line := range selected {
			fmt.Fprint(out, marker, line)
			if !strings.HasSuffix(line, "\n") {
				fmt.Fprintln(out, "\n\\ No newline at end of file")
			}
		}
	}
	writeLines(" ", old[begin:prefix])
	writeLines("-", old[prefix:len(old)-suffix])
	writeLines("+", new[prefix:len(new)-suffix])
	writeLines(" ", old[len(old)-suffix:len(old)-suffix+contextLines])
}

func (c *Client) Check(ctx context.Context, showDiff bool) error {
	status, err := c.status(ctx)
	if err != nil {
		return err
	}
	s, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	changes, err := c.changes(s.Rendered)
	if err != nil {
		return err
	}
	c.targetSummary(status)
	if len(changes) == 0 {
		fmt.Fprintln(c.output(), "State:   current")
	} else {
		local, err := c.localChanges()
		if err != nil {
			return err
		}
		baseline, err := c.metadata()
		if err != nil {
			return err
		}
		state := "differences found (workspace baseline unverified)"
		if len(local) > 0 {
			state = "local edits"
		}
		if baseline["revision"] != "" && baseline["revision"] != s.Version.Revision {
			state = "printer target changed"
			if len(local) > 0 {
				state = "diverged — both this workspace and the printer target changed"
			}
		}
		fmt.Fprintf(c.output(), "State:   %s\n", state)
		c.printChanges(changes)
		fmt.Fprintln(c.output(), "Choose 'hlab klippee apply' to use desired configuration or 'hlab klippee capture' to keep local files.")
	}
	if showDiff {
		remote := map[string]string{}
		for _, f := range s.Rendered {
			remote[f.Path] = f.Content
		}
		for _, ch := range changes {
			data, err := workspaceFile(c.Config.ConfigDir, ch.Path)
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintf(c.output(), "--- local/%s\n+++ desired/%s\n", ch.Path, ch.Path)
			writeUnifiedDiff(c.output(), diffContent(data), diffContent(remote[ch.Path]))
		}
	}
	return nil
}

func (c *Client) confirm(changes []change, yes bool) error {
	if len(changes) == 0 || yes {
		return nil
	}
	f, ok := c.In.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return fmt.Errorf("apply requires confirmation when local files differ; rerun hlab klippee apply --yes")
	}
	fmt.Fprint(c.output(), "Replace these local files with desired configuration? [y/N] ")
	answer, err := bufio.NewReader(c.In).ReadString('\n')
	if err != nil {
		return err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("apply cancelled without changing files")
	}
	return nil
}

func (c *Client) Apply(ctx context.Context, yes, ifClean bool, expectedRevision string) error {
	unlock, err := c.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if yes && ifClean {
		return fmt.Errorf("apply accepts only one of --yes or --if-clean")
	}
	if ifClean {
		if err := c.requireClean(); err != nil {
			return err
		}
	}
	status, err := c.status(ctx)
	if err != nil {
		return err
	}
	s, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	if expectedRevision != "" && s.Version.Revision != expectedRevision {
		return fmt.Errorf("desired configuration changed after recovery")
	}
	changes, err := c.changes(s.Rendered)
	if err != nil {
		return err
	}
	for _, ch := range changes {
		if _, err := managedPath(c.Config.ConfigDir, ch.Path); err != nil {
			return err
		}
	}
	c.targetSummary(status)
	c.printChanges(changes)
	// Refuse concurrent edits between review and installation, even with --yes.
	before := map[string]string{}
	paths, err := configPaths(c.Config.ConfigDir)
	if err != nil {
		return err
	}
	manifest, err := c.manifest()
	if err != nil {
		return err
	}
	all := map[string]bool{}
	for _, p := range paths {
		all[p] = true
	}
	for p := range manifest {
		all[p] = true
	}
	for _, f := range s.Rendered {
		all[f.Path] = true
	}
	paths = nil
	for path := range all {
		paths = append(paths, path)
		data, e := workspaceFile(c.Config.ConfigDir, path)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if e == nil {
			before[path] = checksum([]byte(data))
		}
	}
	sort.Strings(paths)
	if err := c.confirm(changes, yes || ifClean); err != nil {
		return err
	}
	confirmed, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(s, confirmed) {
		return fmt.Errorf("desired configuration changed during review; no local files were changed")
	}
	if len(changes) > 0 {
		backup, err := c.backup(paths)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.output(), "Backup:  %s\n", backup)
	}
	if ifClean {
		if err := c.requireClean(); err != nil {
			return err
		}
	}
	for _, path := range paths {
		data, e := workspaceFile(c.Config.ConfigDir, path)
		digest := ""
		if e == nil {
			digest = checksum([]byte(data))
		} else if !os.IsNotExist(e) {
			return e
		}
		if digest != before[path] {
			return fmt.Errorf("local configuration changed during review or backup; no managed files were changed")
		}
	}
	remote := map[string]string{}
	for _, f := range s.Rendered {
		remote[f.Path] = f.Content
	}
	for _, ch := range changes {
		path, err := managedPath(c.Config.ConfigDir, ch.Path)
		if err != nil {
			return err
		}
		if ch.Action == "remove" {
			err = os.Remove(path)
		} else {
			err = atomicWrite(path, []byte(remote[ch.Path]), 0600)
		}
		if err != nil {
			return fmt.Errorf("install %s: %w; recover using the local backup", ch.Path, err)
		}
	}
	meta := map[string]string{"version": strconv.Itoa(s.Version.Number), "version_id": s.Version.ID, "revision": s.Version.Revision, "source": s.Version.Source, "created_at": s.Version.Created, "synced_at": time.Now().UTC().Format(time.RFC3339)}
	if err := c.writeMetadata(meta); err != nil {
		return err
	}
	installed := []string{}
	for _, f := range s.Rendered {
		installed = append(installed, f.Path)
	}
	if err := c.writeBaseline(installed); err != nil {
		return err
	}
	if err := c.writeTemplates(s.Version.Files, s.Rendered); err != nil {
		return err
	}
	if c.Config.ClientMode == "printer" {
		data, err := c.api(ctx, "applied", map[string]any{"protocolVersion": 2, "versionNumber": s.Version.Number, "desiredRevision": s.Version.Revision, "files": s.Version.Files})
		if err != nil {
			return fmt.Errorf("files installed locally but cloud confirmation failed; applied state did not advance: %w", err)
		}
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			return fmt.Errorf("invalid applied-state response: %w", err)
		}
	}
	fmt.Fprintf(c.output(), "Updated: %d configuration paths; snapshot %s\n", len(changes), s.Version.Revision)
	if c.Config.ClientMode == "desktop" {
		fmt.Fprintln(c.output(), "The physical printer has not applied this desktop workspace.")
	} else {
		fmt.Fprintln(c.output(), "Restart Klipper when ready.")
		c.reportHostInfo(ctx)
	}
	return nil
}

func role(path string) string {
	if path == "printer.cfg" {
		return "main"
	}
	if strings.Contains(strings.ToLower(path), "macro") {
		return "macro"
	}
	return "include"
}
func (c *Client) Capture(ctx context.Context, note string) error {
	unlock, err := c.lock()
	if err != nil {
		return err
	}
	defer unlock()
	paths, err := configPaths(c.Config.ConfigDir)
	if err != nil {
		return err
	}
	if len(paths) == 0 || len(paths) > 64 {
		return fmt.Errorf("capture requires between 1 and 64 safe config files")
	}
	templates, err := c.templates()
	if err != nil {
		return err
	}
	files := []File{}
	rendered := []File{}
	for _, path := range paths {
		content, err := workspaceFile(c.Config.ConfigDir, path)
		if err != nil {
			return err
		}
		file := File{Path: path, Content: content, Role: role(path)}
		rendered = append(rendered, file)
		if state, ok := templates.Files[path]; ok {
			// A rendered line may contain a private local value. Restore known
			// template lines and refuse edited secret-bearing lines until reviewed.
			if content == state.Rendered {
				file.Content = state.Template
			} else {
				t := strings.Split(state.Template, "\n")
				r := strings.Split(state.Rendered, "\n")
				current := strings.Split(content, "\n")
				if len(t) != len(r) {
					return fmt.Errorf("invalid variable template baseline")
				}
				for i, template := range t {
					if !placeholder.MatchString(template) {
						continue
					}
					found := false
					for j, line := range current {
						if line == r[i] || line == template {
							current[j] = template
							found = true
						}
					}
					if !found {
						return fmt.Errorf("%s: a rendered variable line changed; restore its ${KLIPPEE:name} placeholder before capture", path)
					}
				}
				file.Content = strings.Join(current, "\n")
			}
		}
		files = append(files, file)
	}
	meta, err := c.metadata()
	if err != nil {
		return err
	}
	if note == "" {
		note = "Captured from " + c.Config.ClientMode
	}
	payload := map[string]any{"protocolVersion": 2, "clientMode": c.Config.ClientMode, "message": note, "files": files}
	c.targetSummary(nil)
	if meta["revision"] != "" {
		payload["baseRevision"] = meta["revision"]
	}
	if number, err := strconv.Atoi(meta["version"]); err == nil {
		payload["baseVersionNumber"] = number
	}
	data, err := c.api(ctx, "push", payload)
	if err != nil {
		return err
	}
	var response struct {
		Version *version `json:"version"`
		Desired struct {
			Number   int    `json:"versionNumber"`
			ID       string `json:"versionId"`
			Revision string `json:"revision"`
			Created  string `json:"createdAt"`
		} `json:"desired"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("invalid capture response: %w", err)
	}
	v := version{Number: response.Desired.Number, ID: response.Desired.ID, Revision: response.Desired.Revision, Created: response.Desired.Created, Source: "capture"}
	if response.Version != nil {
		v = *response.Version
	}
	if v.Revision == "" {
		return fmt.Errorf("capture response missing revision; local baseline was not advanced")
	}
	if err := c.writeTemplates(files, rendered); err != nil {
		return err
	}
	if err := c.writeMetadata(map[string]string{"version": strconv.Itoa(v.Number), "version_id": v.ID, "revision": v.Revision, "source": v.Source, "created_at": v.Created, "note": note, "captured_at": time.Now().UTC().Format(time.RFC3339)}); err != nil {
		return err
	}
	if err := c.writeCapturedBaseline(rendered); err != nil {
		return err
	}
	fmt.Fprintf(c.output(), "Captured: %d configuration files\n", len(files))
	if c.Config.ClientMode == "desktop" {
		fmt.Fprintln(c.output(), "Draft updated; the physical printer has not applied it.")
	} else {
		c.reportHostInfo(ctx)
	}
	return nil
}

func (c *Client) Recover(ctx context.Context, selection string) error {
	if strings.HasPrefix(selection, "local:") {
		unlock, err := c.lock()
		if err != nil {
			return err
		}
		defer unlock()
		name := strings.TrimPrefix(selection, "local:")
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsAny(name, "\\/:") {
			return fmt.Errorf("invalid local recovery point")
		}
		source := c.state(filepath.Join("backups", name))
		if err := realPath(source); err != nil {
			return err
		}
		files := []File{}
		err = filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if err := realPath(path); err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !safePath(relative) {
				return nil
			}
			if _, err := managedPath(c.Config.ConfigDir, relative); err != nil {
				return err
			}
			content, err := readConfigFile(path)
			if err != nil {
				return err
			}
			files = append(files, File{Path: relative, Content: content})
			return nil
		})
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return fmt.Errorf("empty local recovery point")
		}
		paths, err := configPaths(c.Config.ConfigDir)
		if err != nil {
			return err
		}
		backupPaths := map[string]bool{}
		for _, path := range paths {
			backupPaths[path] = true
		}
		for _, file := range files {
			backupPaths[file.Path] = true
		}
		paths = nil
		for path := range backupPaths {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		backup, err := c.backup(paths)
		if err != nil {
			return err
		}
		for _, file := range files {
			path, err := managedPath(c.Config.ConfigDir, file.Path)
			if err != nil {
				return err
			}
			if err := atomicWrite(path, []byte(file.Content), 0600); err != nil {
				return err
			}
		}
		fmt.Fprintf(c.output(), "Recovered local files from %s\nBackup: %s\nRun hlab klippee check to compare with desired configuration.\n", selection, backup)
		return nil
	}
	if selection != "" && !strings.HasPrefix(selection, "cloud:") {
		return fmt.Errorf("recovery points use cloud:<id> or local:<name>")
	}
	data, err := c.api(ctx, "versions", nil)
	if err != nil {
		return err
	}
	var payload struct {
		Versions []version `json:"versions"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || payload.Versions == nil {
		return fmt.Errorf("invalid version history")
	}
	if selection == "" {
		fmt.Fprintln(c.output(), "Cloud recovery points:")
		for _, v := range payload.Versions {
			fmt.Fprintf(c.output(), "  cloud:%d %s %s\n", v.Number, v.Created, v.Message)
		}
		fmt.Fprintln(c.output(), "Local recovery points:")
		entries, err := os.ReadDir(c.state("backups"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
				fmt.Fprintf(c.output(), "  local:%s\n", entry.Name())
			}
		}
		return nil
	}
	if c.Config.ClientMode != "printer" {
		return fmt.Errorf("cloud recovery can only be applied by the physical printer")
	}
	number, err := strconv.Atoi(strings.TrimPrefix(selection, "cloud:"))
	if err != nil {
		return fmt.Errorf("invalid cloud recovery point")
	}
	found := false
	for _, v := range payload.Versions {
		if v.Number == number {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("cloud recovery point not found")
	}
	data, err = c.api(ctx, "rollback", map[string]any{"versionNumber": number})
	if err != nil {
		return err
	}
	var response struct {
		Version version `json:"version"`
	}
	if err := json.Unmarshal(data, &response); err != nil || response.Version.Revision == "" {
		return fmt.Errorf("invalid recovery response")
	}
	return c.Apply(ctx, true, false, response.Version.Revision)
}
