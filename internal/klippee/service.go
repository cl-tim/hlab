package klippee

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const serviceName = "klippee-moonraker.service"
const agentDescription = "Description=Klippee Moonraker integration agent"
const timerDescription = "Description=Run Klippee printer config sync"
const checkDescription = "Description=Klippee printer config sync"
const macroMarker = "# Managed by klippee install-moonraker."

type serviceFile struct {
	path   string
	mode   os.FileMode
	before []byte
	exists bool
}

func inspectServiceFile(path, marker string) (serviceFile, error) {
	file := serviceFile{path: path, mode: 0644}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	if err := realPath(path); err != nil {
		return file, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return file, fmt.Errorf("invalid managed file %s", path)
	}
	file.before, err = os.ReadFile(path)
	if err != nil {
		return file, err
	}
	if marker != "" && !bytes.Contains(file.before, []byte(marker+"\n")) &&
		!bytes.Contains(file.before, []byte(marker+"\r\n")) {
		return file, fmt.Errorf("refusing to replace unmanaged file %s", path)
	}
	file.exists, file.mode = true, info.Mode().Perm()
	return file, nil
}

type serviceEdit struct {
	file    serviceFile
	content []byte
}

func installServiceFiles(edits []serviceEdit, activate func() error, reloadRestored func() error) error {
	written := []serviceFile{}
	for _, edit := range edits {
		if err := atomicWrite(edit.file.path, edit.content, edit.file.mode); err != nil {
			return restoreServiceFiles(written, err)
		}
		written = append(written, edit.file)
	}
	if err := activate(); err != nil {
		err = restoreServiceFiles(written, err)
		if reloadErr := reloadRestored(); reloadErr != nil {
			return fmt.Errorf("%w; could not reload the restored service: %v", err, reloadErr)
		}
		return err
	}
	return nil
}

func restoreServiceFiles(files []serviceFile, original error) error {
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		var err error
		if file.exists {
			err = atomicWrite(file.path, file.before, file.mode)
		} else {
			err = os.Remove(file.path)
		}
		if err != nil {
			original = fmt.Errorf("%w; could not restore %s: %v", original, file.path, err)
		}
	}
	return original
}

func systemdValue(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("invalid path for systemd unit")
	}
	return `"` + strings.NewReplacer("%", "%%", "$", "$$", "\\", "\\\\", `"`, `\"`).Replace(value) + `"`, nil
}

func (c *Client) installService(ctx context.Context, connection string, timer bool) error {
	if c.Config.ClientMode != "printer" || runtime.GOOS != "linux" {
		return fmt.Errorf("service installation requires a Linux printer connection")
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	connection, err = filepath.Abs(connection)
	if err != nil {
		return err
	}
	exe, err := systemdValue(executable)
	if err != nil {
		return err
	}
	conn, err := systemdValue(connection)
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	command := exe + " klippee --connection " + conn
	name := serviceName
	var edits []serviceEdit
	run := func(ctx context.Context, args ...string) error {
		cmd := exec.CommandContext(ctx, systemctl, append([]string{"--user"}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %w (%s)", args[0], err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	if err := run(ctx, "show-environment"); err != nil {
		return err
	}
	wasEnabled := run(ctx, "is-enabled", "--quiet", name) == nil
	wasActive := run(ctx, "is-active", "--quiet", name) == nil
	if timer {
		wasEnabled = run(ctx, "is-enabled", "--quiet", "klippee-sync.timer") == nil
		wasActive = run(ctx, "is-active", "--quiet", "klippee-sync.timer") == nil
	}
	var unlock func()
	if timer {
		name = "klippee-sync.timer"
		service, err := inspectServiceFile(filepath.Join(dir, "klippee-sync.service"), checkDescription)
		if err != nil {
			return err
		}
		timerFile, err := inspectServiceFile(filepath.Join(dir, name), timerDescription)
		if err != nil {
			return err
		}
		edits = []serviceEdit{
			{service, []byte("[Unit]\n" + checkDescription + "\n\n[Service]\nType=oneshot\nExecStart=" + command + " check\n")},
			{timerFile, []byte("[Unit]\n" + timerDescription + "\n\n[Timer]\nOnBootSec=2min\nOnUnitActiveSec=10min\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n")},
		}
	} else {
		unlock, err = c.lock()
		if err != nil {
			return err
		}
		defer func() {
			if unlock != nil {
				unlock()
			}
		}()
		if err := c.requireIdle(ctx); err != nil {
			return err
		}
		if wasActive {
			if err := c.agentIdle(ctx); err != nil {
				return fmt.Errorf("cannot replace the running Moonraker agent: %w", err)
			}
		}
		service, err := inspectServiceFile(filepath.Join(dir, name), agentDescription)
		if err != nil {
			return err
		}
		socket, err := systemdValue("KLIPPEE_MOONRAKER_SOCKET=" + c.moonrakerPath())
		if err != nil {
			return err
		}
		edits, err = c.dashboardEdits()
		if err != nil {
			return err
		}
		unit := "[Unit]\n" + agentDescription + "\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=" +
			command + " moonraker-agent\nEnvironment=" + socket + "\nRestart=on-failure\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n"
		edits = append(edits, serviceEdit{service, []byte(unit)})
	}
	if err := installServiceFiles(edits, func() error {
		if err := run(ctx, "daemon-reload"); err != nil {
			return err
		}
		if unlock != nil {
			unlock()
			unlock = nil
		}
		if err := run(ctx, "enable", name); err != nil {
			return err
		}
		if err := run(ctx, "restart", name); err != nil {
			return err
		}
		if timer {
			return nil
		}
		deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		for {
			err := c.agentIdle(deadline)
			if err == nil {
				return nil
			}
			select {
			case <-deadline.Done():
				return fmt.Errorf("new agent did not answer Moonraker: %w", err)
			case <-time.After(300 * time.Millisecond):
			}
		}
	}, func() error {
		reload, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if !wasEnabled {
			_ = run(reload, "disable", name)
		}
		if !wasActive {
			_ = run(reload, "stop", name)
		}
		if err := run(reload, "daemon-reload"); err != nil {
			return err
		}
		if wasActive {
			return run(reload, "restart", name)
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintf(c.output(), "Installed %s using hlab.\n", name)
	if !timer {
		fmt.Fprintln(c.output(), "Restart Klipper when ready to load newly added dashboard macros.")
	}
	return nil
}

func (c *Client) agentIdle(ctx context.Context) error {
	response, err := c.moonrakerCall(ctx, "server.extensions.request", map[string]any{
		"agent": "klippee", "method": "klippee_operation_status",
	})
	if err != nil {
		return err
	}
	var status struct {
		Running *bool
		Current json.RawMessage
	}
	if err := json.Unmarshal(response, &status); err != nil || status.Running == nil {
		return fmt.Errorf("could not establish agent state")
	}
	if *status.Running || string(status.Current) != "null" {
		return fmt.Errorf("a Klippee operation is running")
	}
	return nil
}

var explicitDashboardInclude = regexp.MustCompile(`(?im)^\s*\[include\s+klippee-macros\.cfg\s*\]\s*(?:#.*)?$`)
var respondSection = regexp.MustCompile(`(?im)^\s*\[respond\]\s*(?:#.*)?$`)

func (c *Client) dashboardEdits() ([]serviceEdit, error) {
	root := c.Config.ConfigDir
	macrosPath, err := managedPath(root, "klippee-macros.cfg")
	if err != nil {
		return nil, err
	}
	macros, err := inspectServiceFile(macrosPath, macroMarker)
	if err != nil {
		return nil, err
	}
	mainPath, err := managedPath(root, "printer.cfg")
	if err != nil {
		return nil, err
	}
	main, err := inspectServiceFile(mainPath, "")
	if err != nil {
		return nil, err
	}
	if !main.exists {
		return nil, fmt.Errorf("printer.cfg is required to install dashboard macros")
	}
	paths, err := configPaths(root)
	if err != nil {
		return nil, err
	}
	hasRespond, hasInclude := false, explicitDashboardInclude.Match(main.before)
	for _, path := range paths {
		if path == "klippee-macros.cfg" {
			hasInclude = true
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, err
		}
		if respondSection.Match(data) {
			hasRespond = true
		}
	}
	var output strings.Builder
	fmt.Fprintln(&output, macroMarker)
	if !hasRespond {
		fmt.Fprintln(&output, "\n[respond]")
	}
	for _, command := range []string{"CHECK", "CAPTURE", "APPLY", "UPLOAD_DIAGNOSTICS"} {
		fmt.Fprintf(&output, "\n[gcode_macro KLIPPEE_%s]\ngcode:\n  {action_call_remote_method(\"klippee_%s\")}\n",
			command, strings.ToLower(command))
	}
	edits := []serviceEdit{{macros, []byte(output.String())}}
	if !hasInclude {
		backup, err := c.backup(paths)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(c.output(), "Backup: %s\n", backup)
		content := append([]byte("[include klippee-macros.cfg]\n\n"), main.before...)
		edits = append(edits, serviceEdit{main, content})
	}
	return edits, nil
}
