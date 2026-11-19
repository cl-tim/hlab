package migrate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// installConfig is deliberately a first-install operation: it will replace
// only the exact inert smoke configuration, never a calibrated printer.cfg.
func (b *nativeBackend) installConfig() error {
	if err := b.confirm("INSTALL-CONFIG"); err != nil {
		return err
	}
	if _, err := b.connection(); err != nil {
		return err
	}
	if err := b.validateRender(); err != nil {
		return err
	}
	entryPoint, err := b.helper("commissioning-printer.cfg")
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	entries, err := os.ReadDir(b.value("output"))
	if err != nil {
		return err
	}
	total := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".cfg") {
			continue
		}
		if !safeConfigName(entry.Name()) {
			return fmt.Errorf("unsafe config filename: %s", entry.Name())
		}
		info, err := os.Lstat(filepath.Join(b.value("output"), entry.Name()))
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return fmt.Errorf("unsafe configuration file: %s", entry.Name())
		}
		raw, err := os.ReadFile(filepath.Join(b.value("output"), entry.Name()))
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), "CHANGE_ME_BEACON_SERIAL") {
			return fmt.Errorf("unresolved Beacon serial in %s", entry.Name())
		}
		if entry.Name() == "printer.cfg" && !bytes.Equal(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n")), bytes.ReplaceAll(entryPoint, []byte("\r\n"), []byte("\n"))) {
			return fmt.Errorf("guided installation requires the unchanged commissioning entry point; deploy a custom entry point deliberately using advanced tools")
		}
		total += len(raw)
		if total > 4*1024*1024 {
			return fmt.Errorf("configuration bundle is too large")
		}
		files[entry.Name()] = raw
	}
	if len(files) < 3 {
		return fmt.Errorf("commissioning configuration is incomplete")
	}
	archive, err := tarFiles(files)
	if err != nil {
		return err
	}
	smoke, err := b.helper("smoke-test-printer.cfg")
	if err != nil {
		return err
	}
	data := "/home/" + b.account() + "/printer_data"
	backup := data + "/config-before-firmware-" + randomToken()
	// Receive and verify the complete archive before stopping Klipper. The
	// snapshot path is announced before any overwrite and survives failures.
	command := `set -eu; umask 077
data=` + quote(data) + `; cfg="$data/config"; backup=` + quote(backup) + `
[ -f "$data/mainline-bootstrap.txt" ] && [ -d "$cfg" ] && [ ! -L "$cfg" ] && [ -f "$cfg/printer.cfg" ] && [ ! -L "$cfg/printer.cfg" ]
[ "$(sha256sum "$cfg/printer.cfg" | cut -d ' ' -f1)" = ` + quote(hashBytes(smoke)) + ` ] || { echo 'Refusing to replace an existing/custom/calibrated printer.cfg' >&2; exit 1; }
stage=$(mktemp -d "$data/.commissioning.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
tar -xf - -C "$stage"
`
	for name, raw := range files {
		command += `source="$stage/"` + quote(name) + `; [ "$(sha256sum "$source" | cut -d ' ' -f1)" = ` + quote(hashBytes(raw)) + ` ]` + "\n"
	}
	command += `sudo -n systemctl stop klipper.service
set +e
state=$(sudo -n systemctl is-active klipper.service); rc=$?
set -e
[ "$rc" = 3 ] && [ "$state" = inactive ] || { echo 'Klipper stop was not verified' >&2; exit 1; }
cp -a -- "$cfg" "$backup"
printf 'Configuration recovery snapshot: %s\n' "$backup"
`
	for name, raw := range files {
		command += `destination="$cfg/"` + quote(name) + `; [ ! -L "$destination" ]; install -m 644 -- "$stage/"` + quote(name) + ` "$destination"` + "\n"
		command += `[ "$(sha256sum "$destination" | cut -d ' ' -f1)" = ` + quote(hashBytes(raw)) + ` ]` + "\n"
	}
	command += "sudo -n systemctl restart klipper.service\n"
	if err := b.sshRun(b.ownerCommand(command), archive); err != nil {
		return fmt.Errorf("configuration installation failed; inspect recovery snapshot %s and Klipper state: %w", backup, err)
	}
	last := "no facts collected"
	for attempt := 0; attempt < 45; attempt++ {
		facts, reason, err := b.collectFacts()
		if err != nil {
			return err
		}
		last = reason
		if facts != nil {
			cfg := evaluateNativeProbe(nativeProbeDefinitions[8], facts, hashBytes(smoke))
			mcu := evaluateNativeProbe(nativeProbeDefinitions[9], facts, hashBytes(smoke))
			last = cfg.Detail + "; " + mcu.Detail
			if cfg.Status == "satisfied" && mcu.Status == "satisfied" {
				fmt.Fprintln(b.out, "Commissioning configuration installed; both MCUs connected. Next: calibrate and test single-material printing.")
				return nil
			}
		}
		if attempt < 44 {
			if err := waitContext(b.ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("commissioning did not become ready: %s; recovery snapshot %s", last, backup)
}

func safeConfigName(name string) bool {
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return name != "" && !strings.HasPrefix(name, "-")
}
