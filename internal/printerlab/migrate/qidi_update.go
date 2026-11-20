package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/cl-tim/hlab/internal/printerlab/migration"
)

const QIDIUpdateAction = "UPDATE-QIDI-FIRMWARE"

type QIDIArtifact struct {
	URL    string `json:"url" yaml:"url"`
	SHA256 string `json:"sha256" yaml:"sha256"`
	Bytes  int64  `json:"bytes" yaml:"bytes"`
}

// QIDIRelease is a reviewed installation profile, not a general-purpose package URL.
// Add models/releases only after inspecting their installer and hardware layout.
type QIDIRelease struct {
	Model          string       `json:"model" yaml:"model"`
	Version        string       `json:"version" yaml:"version"`
	DeviceMarker   string       `json:"device_marker" yaml:"device_marker"`
	Account        string       `json:"account" yaml:"account"`
	Architecture   string       `json:"architecture" yaml:"architecture"`
	PackageName    string       `json:"package_name" yaml:"package_name"`
	PackageArch    string       `json:"package_arch" yaml:"package_arch"`
	VersionFile    string       `json:"version_file" yaml:"version_file"`
	ScreenDevice   string       `json:"screen_device" yaml:"screen_device"`
	ScreenVersion  string       `json:"screen_version" yaml:"screen_version"`
	MinimumVersion string       `json:"minimum_version" yaml:"minimum_version"`
	Source         string       `json:"source" yaml:"source"`
	Host           QIDIArtifact `json:"host" yaml:"host"`
	Screen         QIDIArtifact `json:"screen" yaml:"screen"`
	PreservePaths  []string     `json:"preserve_paths" yaml:"preserve_paths"`
	RemovedGlobs   []string     `json:"removed_globs" yaml:"removed_globs"`
}

func QIDIReleases() ([]QIDIRelease, error) {
	raw, err := migration.QIDIReleaseCatalog()
	if err != nil {
		return nil, err
	}
	var releases []QIDIRelease
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&releases); err != nil {
		return nil, err
	}
	for _, release := range releases {
		for _, artifact := range []QIDIArtifact{release.Host, release.Screen} {
			if !digestPattern.MatchString(artifact.SHA256) || artifact.Bytes <= 0 || !strings.HasPrefix(artifact.URL, "https://") {
				return nil, fmt.Errorf("invalid QIDI release artifact")
			}
		}
	}
	return releases, nil
}

func QIDIReleaseFor(model, version string) (QIDIRelease, error) {
	if version == "" {
		return QIDIRelease{}, fmt.Errorf("stock QIDI updates require --version; use firmware releases to list reviewed releases")
	}
	releases, err := QIDIReleases()
	if err != nil {
		return QIDIRelease{}, err
	}
	for _, release := range releases {
		if release.Model == model && release.Version == version {
			return release, nil
		}
	}
	return QIDIRelease{}, fmt.Errorf("no reviewed QIDI %s firmware %s; use firmware releases (arbitrary package URLs are not accepted)", model, version)
}

// SelectQIDIRelease resolves standalone CLI defaults only from the reviewed
// catalogue. Ambiguous models require an explicit selection; live preflight
// still verifies the printer's actual device marker against this profile.
func SelectQIDIRelease(model, version string) (QIDIRelease, error) {
	releases, err := QIDIReleases()
	if err != nil {
		return QIDIRelease{}, err
	}
	return selectQIDIRelease(releases, model, version)
}

func selectQIDIRelease(releases []QIDIRelease, model, version string) (QIDIRelease, error) {
	var selected QIDIRelease
	var latest [3]int
	for _, release := range releases {
		if model != "" && release.Model != model || version != "" && release.Version != version {
			continue
		}
		if selected.Model != "" && selected.Model != release.Model {
			return QIDIRelease{}, fmt.Errorf("multiple reviewed QIDI models match; select --model")
		}
		key, err := kernelVersion(release.Version)
		if err != nil {
			return QIDIRelease{}, fmt.Errorf("invalid reviewed QIDI release version %q", release.Version)
		}
		if selected.Model == "" || kernelOlder(latest, key) {
			selected, latest = release, key
		}
	}
	if selected.Model == "" {
		return QIDIRelease{}, fmt.Errorf("no reviewed QIDI release matches model %q and version %q; use firmware releases", model, version)
	}
	return selected, nil
}

type QIDIUpdateResult struct {
	Schema          int    `json:"schema" yaml:"schema"`
	OK              bool   `json:"ok" yaml:"ok"`
	Status          string `json:"status" yaml:"status"`
	Model           string `json:"model" yaml:"model"`
	Target          string `json:"target" yaml:"target"`
	Before          string `json:"before" yaml:"before"`
	After           string `json:"after" yaml:"after"`
	CheckOnly       bool   `json:"check_only" yaml:"check_only"`
	HostOnly        bool   `json:"host_only" yaml:"host_only"`
	ConfigPolicy    string `json:"config_policy" yaml:"config_policy"`
	ScreenStatus    string `json:"screen_status" yaml:"screen_status"`
	Backup          string `json:"backup" yaml:"backup"`
	BackupSHA256    string `json:"backup_sha256" yaml:"backup_sha256"`
	LocalBackup     string `json:"local_backup" yaml:"local_backup"`
	ConfigVerified  bool   `json:"config_verified" yaml:"config_verified"`
	StartupVerified bool   `json:"startup_verified" yaml:"startup_verified"`
	Error           string `json:"error" yaml:"error"`
}

func ParseQIDIUpdate(raw []byte) (QIDIUpdateResult, error) {
	var result QIDIUpdateResult
	if len(raw) > 64*1024 {
		return result, fmt.Errorf("oversized QIDI update receipt")
	}
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return result, err
	}
	if _, err := exactUpgradeObject(raw, "QIDI update", []string{"schema", "ok", "status", "model", "target", "before", "after", "check_only", "host_only", "config_policy", "screen_status", "backup", "backup_sha256", "local_backup", "config_verified", "startup_verified", "error"}, nil); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return result, fmt.Errorf("QIDI update returned multiple results")
	}
	if result.Schema != 1 || result.Model == "" || result.Target == "" || (result.ConfigPolicy != "preserve" && result.ConfigPolicy != "vendor") {
		return result, fmt.Errorf("invalid QIDI update schema or target")
	}
	if result.Backup != "" && (!regexp.MustCompile(`^/var/lib/hlab/qidi-updates/[0-9a-f]{32}$`).MatchString(result.Backup) || !digestPattern.MatchString(result.BackupSHA256)) {
		return result, fmt.Errorf("invalid QIDI backup path or digest")
	}
	if !result.OK {
		if result.Error == "" || (result.Status != "failed" && result.Status != "recovery_required") {
			return result, fmt.Errorf("invalid QIDI failure receipt")
		}
		return result, nil
	}
	if result.Error != "" || result.Before == "" {
		return result, fmt.Errorf("QIDI success omitted version evidence")
	}
	switch result.Status {
	case "checked":
		if !result.CheckOnly || result.After != result.Before {
			return result, fmt.Errorf("invalid QIDI check receipt")
		}
	case "prepared":
		if result.CheckOnly || result.Backup == "" || result.After != result.Before {
			return result, fmt.Errorf("invalid QIDI preparation receipt")
		}
	case "updated":
		if result.CheckOnly || result.After != result.Target || result.Backup == "" || result.LocalBackup == "" || !result.StartupVerified || !result.ConfigVerified || (!result.HostOnly && result.ScreenStatus != "acknowledged") || (result.HostOnly && result.ScreenStatus != "unchanged") {
			return result, fmt.Errorf("QIDI update omitted completed startup, configuration, screen or backup evidence")
		}
	default:
		return result, fmt.Errorf("unknown QIDI update status %q", result.Status)
	}
	return result, nil
}

func (b *nativeBackend) updateQIDI() error {
	if !b.switches["check"] {
		if err := b.confirm(QIDIUpdateAction); err != nil {
			return err
		}
	}
	if _, err := b.connection(); err != nil {
		return err
	}
	release, err := QIDIReleaseFor(b.value("model"), b.value("firmware-version"))
	if err != nil {
		return err
	}
	if b.account() != release.Account {
		return fmt.Errorf("QIDI %s requires printer account %s", release.Model, release.Account)
	}
	policy := b.value("config-policy")
	if policy != "preserve" && policy != "vendor" {
		return fmt.Errorf("invalid QIDI configuration policy")
	}
	port, err := strconv.Atoi(b.value("moonraker-port"))
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid Moonraker port")
	}
	wait, err := strconv.Atoi(b.value("wait-seconds"))
	if err != nil || wait < 5 || wait > 600 {
		return fmt.Errorf("invalid QIDI startup timeout")
	}
	key := ""
	if name := b.value("api-key-env"); name != "" {
		key = os.Getenv(name)
		if key == "" || strings.ContainsAny(key, "\x00\r\n") {
			return fmt.Errorf("Moonraker API key environment is missing or invalid")
		}
	}
	helper, err := b.helper("update-qidi.py")
	if err != nil {
		return err
	}
	options := map[string]any{"release": release, "config_policy": policy, "host_only": b.switches["host-only"], "check_only": b.switches["check"], "moonraker_port": port, "wait_seconds": wait, "api_key": key, "phase": "prepare", "backup": "", "backup_sha256": "", "local_backup": ""}
	if directory := b.value("firmware-dir"); directory != "" {
		files, err := verifiedQIDIFiles(directory, release, b.switches["host-only"])
		if err != nil {
			return err
		}
		if !b.switches["check"] {
			staging, cleanup, err := b.stageQIDIFiles(files, release)
			if err != nil {
				return err
			}
			defer cleanup()
			options["firmware_staging"] = staging
		}
	}
	if b.switches["check"] {
		options["phase"] = "check"
	}
	call := func() (QIDIUpdateResult, error) {
		encoded, _ := json.Marshal(options)
		literal, _ := json.Marshal(string(encoded))
		payload := append([]byte("import json\nPLAB_OPTIONS=json.loads("+string(literal)+")\n"), helper...)
		raw, code, runErr := b.sshCapture("sudo -n python3 -I -", payload)
		result, err := ParseQIDIUpdate(raw)
		if err != nil {
			if runErr != nil {
				return result, runErr
			}
			return result, fmt.Errorf("invalid QIDI receipt: %w", err)
		}
		if result.Model != release.Model || result.Target != release.Version || result.HostOnly != b.switches["host-only"] || result.CheckOnly != b.switches["check"] || result.ConfigPolicy != policy || (code == 0) != result.OK {
			return result, fmt.Errorf("QIDI receipt contradicts target, mode or process exit")
		}
		if options["phase"] == "apply" && (result.Backup != options["backup"] || result.BackupSHA256 != options["backup_sha256"] || result.LocalBackup != options["local_backup"]) {
			return result, fmt.Errorf("QIDI apply receipt does not match the verified recovery backup")
		}
		if runErr != nil {
			return result, runErr
		}
		return result, nil
	}
	result, runErr := call()
	if runErr != nil || !result.OK || b.switches["check"] {
		if result.Schema == 1 {
			_ = b.writeJSON(result)
		}
		if runErr != nil {
			if result.Error != "" {
				return fmt.Errorf("QIDI update: %s", result.Error)
			}
			return runErr
		}
		if !result.OK {
			return fmt.Errorf("QIDI update: %s", result.Error)
		}
		return nil
	}
	if result.Status != "prepared" {
		return fmt.Errorf("QIDI prepare did not return a backup")
	}
	localDir := filepath.Join(b.home, "backups", "qidi-firmware", filepath.Base(result.Backup))
	if err := os.MkdirAll(localDir, 0700); err != nil {
		return err
	}
	localPath := filepath.Join(localDir, "recovery.tar.gz")
	stream, err := os.OpenFile(localPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = b.sshWrite("sudo -n cat -- "+quote(result.Backup+"/recovery.tar.gz"), nil, stream)
	syncErr := stream.Sync()
	closeErr := stream.Close()
	if err != nil {
		_ = os.Remove(localPath)
		return fmt.Errorf("backup transfer failed; printer was not upgraded: %w", err)
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	digest, err := hashFile(localPath)
	if err != nil || digest != result.BackupSHA256 {
		_ = os.Remove(localPath)
		return fmt.Errorf("desktop backup verification failed; printer was not upgraded")
	}
	options["phase"], options["backup"], options["backup_sha256"], options["local_backup"] = "apply", result.Backup, digest, localPath
	result, runErr = call()
	if result.Schema == 1 {
		if err := b.writeJSON(result); err != nil {
			return err
		}
	}
	if runErr != nil {
		return runErr
	}
	if !result.OK {
		return fmt.Errorf("QIDI update: %s", result.Error)
	}
	return nil
}
