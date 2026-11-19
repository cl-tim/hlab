package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const renderSchema = "qidi-plus4-config-render/v1"
const renderManager = "qidi-plus4-firmware/render-config"

func (b *nativeBackend) validateRender() error {
	path := b.value("output")
	if !managedRender(path) {
		return fmt.Errorf("existing configuration is not a managed render: %s; preserve it and choose a new config output", path)
	}
	raw, err := os.ReadFile(filepath.Join(path, "RENDER-METADATA.json"))
	if err != nil {
		return err
	}
	doc, err := decodeObject(raw)
	if err != nil {
		return err
	}
	profile := b.value("profile")
	if b.value("profile-dir") != "" {
		profile = filepath.Base(b.value("profile-dir"))
	}
	if doc["commissioning"] != true || doc["beacon_serial"] != b.value("beacon-serial") || doc["profile"] != profile {
		return fmt.Errorf("existing render does not match this printer's commissioning settings; preserve it and choose a new config output")
	}
	for _, name := range []string{"printer.cfg", "hardware.cfg", "beacon.cfg"} {
		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("managed render lacks a regular %s", name)
		}
	}
	fmt.Fprintf(b.out, "Using existing configuration: %s\n", path)
	return nil
}

func managedRender(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	marker := filepath.Join(path, "RENDER-METADATA.json")
	info, err = os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		return false
	}
	doc, err := decodeObject(raw)
	if err != nil {
		return false
	}
	for _, key := range []string{"profile", "beacon_serial", "rendered_at_utc"} {
		if value, ok := doc[key].(string); !ok || value == "" {
			return false
		}
	}
	serial := doc["beacon_serial"].(string)
	if !validBeaconSerial(serial) {
		return false
	}
	if len(doc) == 3 {
		return true
	}
	return doc["schema"] == renderSchema && doc["managed_by"] == renderManager
}
func (b *nativeBackend) renderConfig() error {
	serial, err := b.required("beacon-serial")
	if err != nil {
		return err
	}
	serial = strings.TrimSpace(serial)
	if !validBeaconSerial(serial) {
		return fmt.Errorf("Beacon serial must be a /dev/serial/by-id/usb-Beacon_ path without whitespace")
	}
	source := b.value("profile-dir")
	if source == "" {
		profile := b.value("profile")
		if filepath.Base(profile) != profile || profile == "." || profile == ".." || strings.ContainsAny(profile, "/\\") {
			return fmt.Errorf("invalid configuration profile")
		}
		source = filepath.Join(b.root, "mainline-configs", profile)
	}
	source, err = localPath(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("configuration profile not found: %s", source)
	}
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	dest, err := b.required("output")
	if err != nil {
		return err
	}
	dest, err = localPath(dest)
	if err != nil {
		return err
	}
	if sameLocalPath(source, dest) {
		return fmt.Errorf("cannot replace the source profile")
	}
	if resolved, err := filepath.EvalSymlinks(dest); err == nil && sameLocalPath(source, resolved) {
		return fmt.Errorf("cannot replace the source profile")
	}
	if _, err := os.Lstat(dest); err == nil {
		if !b.switches["force"] || !managedRender(dest) {
			return fmt.Errorf("refusing to replace %s: use --force only with a managed render directory", dest)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".render-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".cfg") || entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("profile contains a symlink: %s", entry.Name())
		}
		raw, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		text := strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "CHANGE_ME_BEACON_SERIAL", serial)
		if err := os.WriteFile(filepath.Join(stage, entry.Name()), []byte(text), 0600); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return fmt.Errorf("profile contains no .cfg files")
	}
	if b.switches["commissioning"] {
		for _, name := range []string{"hardware.cfg", "beacon.cfg"} {
			if _, err := os.Stat(filepath.Join(stage, name)); err != nil {
				return fmt.Errorf("commissioning requires %s", name)
			}
		}
		raw, err := b.helper("commissioning-printer.cfg")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(stage, "printer.cfg"), raw, 0600); err != nil {
			return err
		}
		beacon := filepath.Join(stage, "beacon.cfg")
		raw, err = os.ReadFile(beacon)
		if err != nil {
			return err
		}
		raw = regexp.MustCompile(`(?m)^accel_axes_map:.*$`).ReplaceAll(raw, []byte("accel_axes_map: x, y, z"))
		if err := os.WriteFile(beacon, raw, 0600); err != nil {
			return err
		}
	}
	staged, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range staged {
		raw, err := os.ReadFile(filepath.Join(stage, entry.Name()))
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), "CHANGE_ME_BEACON_SERIAL") {
			return fmt.Errorf("unresolved Beacon serial placeholder in %s", entry.Name())
		}
	}
	metadata := map[string]any{"schema": renderSchema, "managed_by": renderManager, "profile": filepath.Base(source), "beacon_serial": serial, "commissioning": b.switches["commissioning"], "rendered_at_utc": utcStamp()}
	raw, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "RENDER-METADATA.json"), append(raw, '\n'), 0600); err != nil {
		return err
	}
	previous := ""
	if _, err := os.Lstat(dest); err == nil {
		previous = dest + ".previous-" + randomToken()
		if err := os.Rename(dest, previous); err != nil {
			return err
		}
	}
	if err := os.Rename(stage, dest); err != nil {
		if previous != "" {
			if restoreErr := os.Rename(previous, dest); restoreErr != nil {
				return fmt.Errorf("publish failed: %v; previous render remains at %s: %v", err, previous, restoreErr)
			}
		}
		return err
	}
	if previous != "" {
		if err := os.RemoveAll(previous); err != nil {
			return err
		}
	}
	fmt.Fprintf(b.out, "Rendered %d configuration files to %s\nReview pin mappings and uncalibrated values before commissioning.\n", count, dest)
	return nil
}
func sameLocalPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if os.PathSeparator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}
func validBeaconSerial(value string) bool {
	return strings.HasPrefix(value, "/dev/serial/by-id/usb-Beacon_") && strings.IndexFunc(value, unicode.IsSpace) < 0 && !strings.ContainsRune(value, 0)
}
