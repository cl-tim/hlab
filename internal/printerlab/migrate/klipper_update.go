package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const KlipperUpdateAction = "UPDATE-KLIPPER"

// KlipperUpdateResult describes a host-only update and its startup verification.
type KlipperUpdateResult struct {
	Schema        int    `json:"schema" yaml:"schema"`
	OK            bool   `json:"ok" yaml:"ok"`
	Status        string `json:"status" yaml:"status"`
	Account       string `json:"account" yaml:"account"`
	CheckOnly     bool   `json:"check_only" yaml:"check_only"`
	Before        string `json:"before" yaml:"before"`
	Target        string `json:"target" yaml:"target"`
	After         string `json:"after" yaml:"after"`
	Version       string `json:"version" yaml:"version"`
	Backup        string `json:"backup" yaml:"backup"`
	Error         string `json:"error" yaml:"error"`
	RollbackError string `json:"rollback_error" yaml:"rollback_error"`
}

func ParseKlipperUpdate(raw []byte) (KlipperUpdateResult, error) {
	var result KlipperUpdateResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode Klipper update result: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return result, fmt.Errorf("Klipper update returned more than one JSON result")
	}
	if result.Schema != 1 || !regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`).MatchString(result.Account) {
		return result, fmt.Errorf("invalid Klipper update schema or account")
	}
	if result.OK {
		if result.Error != "" || result.RollbackError != "" || result.Version == "" ||
			!commitPattern.MatchString(result.Before) || !commitPattern.MatchString(result.Target) || !commitPattern.MatchString(result.After) {
			return result, fmt.Errorf("Klipper update success omitted verified revision or startup evidence")
		}
		switch result.Status {
		case "current":
			if result.Before != result.Target || result.After != result.Target {
				return result, fmt.Errorf("current Klipper update result has mismatched revisions")
			}
		case "available":
			if !result.CheckOnly || result.Before == result.Target || result.After != result.Before {
				return result, fmt.Errorf("available Klipper update result contradicts check-only mode")
			}
		case "updated":
			if result.CheckOnly || result.Before == result.Target || result.After != result.Target || result.Backup == "" {
				return result, fmt.Errorf("updated Klipper result lacks a backup or contradicts its revisions")
			}
		default:
			return result, fmt.Errorf("unknown successful Klipper update status %q", result.Status)
		}
	} else if result.Error == "" || (result.Status != "failed" && result.Status != "rolled_back" && result.Status != "rollback_failed") {
		return result, fmt.Errorf("Klipper update failure omitted its cause or returned an unknown status")
	}
	if result.Status == "rolled_back" && (result.CheckOnly || result.After != result.Before ||
		!commitPattern.MatchString(result.Before) || result.Version == "" || result.Backup == "" || result.RollbackError != "") {
		return result, fmt.Errorf("Klipper rollback result omitted verified recovery evidence")
	}
	return result, nil
}

func (b *nativeBackend) updateKlipper() error {
	if !b.switches["check"] {
		if err := b.confirm(KlipperUpdateAction); err != nil {
			return err
		}
	}
	if _, err := b.connection(); err != nil {
		return err
	}
	account := b.account()
	if !regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`).MatchString(account) {
		return fmt.Errorf("invalid printer account")
	}
	port, err := strconv.Atoi(b.value("moonraker-port"))
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid Moonraker port")
	}
	wait, err := strconv.Atoi(b.value("wait-seconds"))
	if err != nil || wait < 5 || wait > 600 {
		return fmt.Errorf("Klipper startup timeout must be 5 to 600 seconds")
	}
	key := ""
	if name := b.value("api-key-env"); name != "" {
		key = os.Getenv(name)
		if key == "" || strings.ContainsAny(key, "\x00\r\n") {
			return fmt.Errorf("Moonraker API key environment is missing or invalid")
		}
	}
	helper, err := b.helper("update-klipper.py")
	if err != nil {
		return err
	}
	options, err := json.Marshal(map[string]any{
		"account": account, "moonraker_port": port, "wait_seconds": wait,
		"check_only": b.switches["check"], "api_key": key,
	})
	if err != nil {
		return err
	}
	// Options (including any API key) travel only in SSH stdin, never argv or
	// a remote temporary file. No helper is installed in Klipper itself.
	encoded, _ := json.Marshal(string(options))
	payload := []byte("import json\nPLAB_OPTIONS = json.loads(" + string(encoded) + ")\n")
	payload = append(payload, helper...)
	command := b.ownerCommand("exec python3 -I -")
	raw, code, runErr := b.sshCapture(command, payload)
	result, parseErr := ParseKlipperUpdate(raw)
	if parseErr != nil {
		return fmt.Errorf("invalid Klipper update receipt: %w", parseErr)
	}
	if result.Account != account || result.CheckOnly != b.switches["check"] || (code == 0) != result.OK {
		return fmt.Errorf("Klipper update receipt contradicts its target, mode or exit status")
	}
	if _, err := b.out.Write(raw); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	if !result.OK {
		return fmt.Errorf("Klipper update failed: %s", result.Error)
	}
	return nil
}
