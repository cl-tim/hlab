package migrate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const QIDIFixesAction = "APPLY-QIDI-FIXES"

var fixesTransaction = regexp.MustCompile(`^/var/lib/hlab/qidi-fixes/[0-9a-f]{32}$`)

type QIDIFixesResult struct {
	Schema          int      `json:"schema" yaml:"schema"`
	OK              bool     `json:"ok" yaml:"ok"`
	Status          string   `json:"status" yaml:"status"`
	Model           string   `json:"model" yaml:"model"`
	Version         string   `json:"version" yaml:"version"`
	CheckOnly       bool     `json:"check_only" yaml:"check_only"`
	Polling         bool     `json:"polling" yaml:"polling"`
	TrimRunoutWaits bool     `json:"trim_runout_waits" yaml:"trim_runout_waits"`
	USBEventLog     bool     `json:"usb_event_log" yaml:"usb_event_log"`
	FastProbing     bool     `json:"fast_probing" yaml:"fast_probing"`
	BoxDrying       bool     `json:"box_drying" yaml:"box_drying"`
	Backup          string   `json:"backup" yaml:"backup"`
	BackupSHA256    string   `json:"backup_sha256" yaml:"backup_sha256"`
	LocalBackup     string   `json:"local_backup" yaml:"local_backup"`
	StartupVerified bool     `json:"startup_verified" yaml:"startup_verified"`
	Changes         []string `json:"changes" yaml:"changes"`
	Notes           []string `json:"notes" yaml:"notes"`
	Error           string   `json:"error" yaml:"error"`
}

func ParseQIDIFixes(raw []byte) (QIDIFixesResult, error) {
	var result QIDIFixesResult
	if len(raw) > 128*1024 {
		return result, fmt.Errorf("oversized QIDI fixes receipt")
	}
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return result, err
	}
	if _, err := exactUpgradeObject(raw, "QIDI fixes", strings.Fields("schema ok status model version check_only polling trim_runout_waits usb_event_log fast_probing box_drying backup backup_sha256 local_backup startup_verified changes notes error"), nil); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if decoder.Decode(new(any)) != io.EOF || result.Schema != 1 || result.Model != "plus4" {
		return result, fmt.Errorf("invalid QIDI fixes receipt")
	}
	if result.Backup != "" && (!fixesTransaction.MatchString(result.Backup) || !boxClockGuardDigestPattern.MatchString(result.BackupSHA256)) {
		return result, fmt.Errorf("invalid QIDI fixes recovery handle")
	}
	if !result.OK {
		if result.Status != "failed" || result.Error == "" {
			return result, fmt.Errorf("invalid QIDI fixes failure receipt")
		}
		return result, nil
	}
	if (result.Version != "1.8.2" && result.Version != "2.1.1") || result.Error != "" {
		return result, fmt.Errorf("unsupported fixes version or contradictory success")
	}
	switch result.Status {
	case "checked":
		if !result.CheckOnly || result.Backup != "" || result.StartupVerified {
			return result, fmt.Errorf("invalid fixes check receipt")
		}
	case "prepared":
		if result.CheckOnly || result.Backup == "" || result.StartupVerified {
			return result, fmt.Errorf("invalid fixes prepare receipt")
		}
	case "applied", "rolled_back":
		if result.CheckOnly || result.Backup == "" || !result.StartupVerified {
			return result, fmt.Errorf("fixes did not prove recovery and startup")
		}
		if result.Status == "applied" && result.LocalBackup == "" {
			return result, fmt.Errorf("applied fixes omitted desktop recovery backup")
		}
	case "unchanged":
		if result.CheckOnly || !result.StartupVerified || len(result.Changes) != 0 {
			return result, fmt.Errorf("invalid unchanged fixes receipt")
		}
	default:
		return result, fmt.Errorf("unknown QIDI fixes status")
	}
	return result, nil
}

func (b *nativeBackend) qidiFixes() error {
	check := b.switches["check"]
	if !check {
		if err := b.confirm(QIDIFixesAction); err != nil {
			return err
		}
	}
	if _, err := b.connection(); err != nil {
		return err
	}
	if b.value("model") != "plus4" || b.account() != "mks" {
		return fmt.Errorf("fixes require the stock Plus4 mks layout")
	}
	rollback := b.value("rollback-manifest")
	if rollback != "" && (check || b.switches["box-motion-polling"] || b.switches["trim-runout-waits"] || b.switches["usb-event-log"] || b.switches["fast-probing"] || b.switches["box-drying"] || !fixesTransaction.MatchString(rollback)) {
		return fmt.Errorf("--rollback requires an exact fixes transaction and cannot be combined with --check or fix options")
	}
	port, err := strconv.Atoi(b.value("moonraker-port"))
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid Moonraker port")
	}
	wait, err := strconv.Atoi(b.value("wait-seconds"))
	if err != nil || wait < 5 || wait > 600 {
		return fmt.Errorf("invalid fixes startup timeout")
	}
	key := ""
	if name := b.value("api-key-env"); name != "" {
		key = os.Getenv(name)
		if key == "" || strings.ContainsAny(key, "\x00\r\n") {
			return fmt.Errorf("Moonraker API key environment is missing or invalid")
		}
	}
	release, err := QIDIReleaseFor("plus4", "2.1.1")
	if err != nil {
		return err
	}
	names := []string{"tools/migration/update-qidi.py", "qidi-fixes/box_output_clock_guard.py", "qidi-fixes/box_status_compat.py", "qidi-fixes/usb_events.py", "qidi-fixes/box_drying.py"}
	for _, name := range plrAssets {
		names = append(names, "plr-hardening/"+name)
	}
	assets := map[string]string{}
	for _, name := range names {
		data, err := b.asset(name)
		if err != nil {
			return err
		}
		assets[name] = base64.StdEncoding.EncodeToString(data)
	}
	helper, err := b.helper("apply-qidi-fixes.py")
	if err != nil {
		return err
	}
	options := map[string]any{"release": release, "assets": assets, "transaction_id": randomToken() + randomToken(), "check_only": check,
		"polling": b.switches["box-motion-polling"], "rollback": rollback, "moonraker_port": port, "wait_seconds": wait,
		"trim_runout_waits": b.switches["trim-runout-waits"], "usb_event_log": b.switches["usb-event-log"],
		"fast_probing": b.switches["fast-probing"], "box_drying": b.switches["box-drying"],
		"api_key": key, "phase": "prepare", "backup": "", "backup_sha256": "", "local_backup": ""}
	// The combined token is 16 random bytes encoded as 32 hex characters, shared
	// by the root-owned transaction and PLR manifest throughout prepare and apply.
	call := func() (QIDIFixesResult, error) {
		encoded, _ := json.Marshal(options)
		literal, _ := json.Marshal(string(encoded))
		payload := append([]byte("import json\nPLAB_OPTIONS=json.loads("+string(literal)+")\n"), helper...)
		raw, code, runErr := b.sshCapture("sudo -n python3 -I -", payload)
		result, err := ParseQIDIFixes(raw)
		if err != nil {
			return result, fmt.Errorf("invalid fixes result: %w (%v)", err, runErr)
		}
		if result.CheckOnly != check || result.Polling != b.switches["box-motion-polling"] ||
			result.TrimRunoutWaits != b.switches["trim-runout-waits"] || result.USBEventLog != b.switches["usb-event-log"] ||
			result.FastProbing != b.switches["fast-probing"] || result.BoxDrying != b.switches["box-drying"] || (code == 0) != result.OK {
			return result, fmt.Errorf("fixes receipt contradicts mode or process exit")
		}
		if options["phase"] == "apply" && (result.Backup != options["backup"] || result.BackupSHA256 != options["backup_sha256"] || result.LocalBackup != options["local_backup"]) {
			return result, fmt.Errorf("fixes receipt does not match desktop-verified recovery")
		}
		if rollback != "" && result.OK && (result.Backup != rollback || result.Status != "rolled_back") {
			return result, fmt.Errorf("rollback receipt does not match requested fixes transaction")
		}
		return result, runErr
	}
	result, runErr := call()
	if runErr == nil && result.OK && result.Status == "prepared" {
		directory := filepath.Join(b.home, "backups", "qidi-fixes", filepath.Base(result.Backup))
		if err := os.MkdirAll(directory, 0700); err != nil {
			return err
		}
		path := filepath.Join(directory, "recovery.tar.gz")
		stream, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		err = b.sshWrite("sudo -n cat -- "+quote(result.Backup+"/recovery.tar.gz"), nil, stream)
		syncErr, closeErr := stream.Sync(), stream.Close()
		if err != nil || syncErr != nil || closeErr != nil {
			return fmt.Errorf("fixes backup transfer failed; no live changes: %v %v %v", err, syncErr, closeErr)
		}
		digest, err := hashFile(path)
		if err != nil || digest != result.BackupSHA256 {
			return fmt.Errorf("fixes backup verification failed; no live changes")
		}
		options["phase"], options["backup"], options["backup_sha256"], options["local_backup"] = "apply", result.Backup, digest, path
		result, runErr = call()
	}
	if result.Schema == 1 {
		if err := b.writeJSON(result); err != nil {
			return err
		}
	}
	if runErr != nil {
		return runErr
	}
	if !result.OK {
		return fmt.Errorf("QIDI fixes failed: %s", result.Error)
	}
	return nil
}
