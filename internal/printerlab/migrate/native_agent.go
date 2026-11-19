package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (b *nativeBackend) refreshAgent() error {
	if err := b.confirm("REFRESH-KLIPPEE-AGENT"); err != nil {
		return err
	}
	if _, err := b.connection(); err != nil {
		return err
	}
	systemctl := "systemctl --user"
	account := b.account()
	if account != b.value("user") {
		systemctl = "uid=$(id -u -- " + quote(account) + ") && sudo -n -u " + quote(account) + " env XDG_RUNTIME_DIR=/run/user/$uid DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus systemctl --user"
	}
	_, restartExit, _ := b.sshCapture(systemctl+" restart klippee-moonraker.service", nil)
	raw, stateExit, _ := b.sshCapture(systemctl+" is-active klippee-moonraker.service", nil)
	state := strings.TrimSpace(string(raw))
	if state == "" {
		state = "unknown"
	}
	ok := restartExit == 0 && stateExit == 0 && state == "active"
	result := map[string]any{"schema": KlippeeAgentRefreshSchema, "ok": ok, "service": "klippee-moonraker.service", "scope": "user", "account": account, "observed_state": state, "restart_exit": restartExit, "state_exit": stateExit, "klipper_actions_performed": false, "config_deployment_performed": false}
	if !ok {
		result["error"] = "Klippee Moonraker agent did not reach the active state"
	}
	if err := b.writeJSON(result); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Klippee agent refresh failed")
	}
	return nil
}
func (b *nativeBackend) upgradeAgent() error {
	if err := b.confirm("UPGRADE-KLIPPEE-AGENT"); err != nil {
		return err
	}
	if b.account() != "mks" {
		return fmt.Errorf("Klippee agent upgrade requires the fixed mks printer account")
	}
	path := filepath.Join(b.root, "tools", "migration", "upgrade-klippee-agent.py")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 128*1024 {
		return fmt.Errorf("reviewed Klippee upgrade helper not found")
	}
	helper, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	command := "exec /usr/bin/env -i HOME=/home/mks USER=mks LOGNAME=mks PATH=/usr/bin:/bin LANG=C LC_ALL=C /usr/bin/python3 -I -"
	if b.value("user") != "mks" {
		command = strings.Replace(command, "exec ", "exec /usr/bin/sudo -n -u mks ", 1)
	}
	raw, code, runErr := b.sshCapture(command, helper)
	result, err := ParseKlippeeAgentUpgrade(raw)
	if err != nil {
		return fmt.Errorf("invalid remote upgrade receipt: %w (%v)", err, runErr)
	}
	if (code == 0) != result.Succeeded() {
		return fmt.Errorf("remote upgrade receipt contradicts its exit status")
	}
	if _, err := b.out.Write(raw); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	return nil
}
