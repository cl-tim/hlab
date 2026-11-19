package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var boxMotionGuardModePattern = regexp.MustCompile(`^0[0-7]{3}$`)

const (
	BoxMotionGuardSchema  = "qidi-plus4-box-motion-template-guard/v1"
	BoxMotionGuardAction  = "HARDEN-BOX-MOTION-TEMPLATE"
	BoxMotionVendorAction = "RESTORE-BOX-MOTION-VENDOR"

	maximumBoxMotionGuardResult = 64 * 1024
)

type BoxMotionGuardTarget struct {
	Path         string `json:"path" yaml:"path"`
	BeforeSHA256 string `json:"before_sha256" yaml:"before_sha256"`
	AfterSHA256  string `json:"after_sha256" yaml:"after_sha256"`
	Mode         string `json:"mode" yaml:"mode"`
	UID          int    `json:"uid" yaml:"uid"`
	GID          int    `json:"gid" yaml:"gid"`
}

type BoxMotionGuardBackup struct {
	Path   string `json:"path" yaml:"path"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

type BoxMotionGuardResult struct {
	Schema               string                `json:"schema" yaml:"schema"`
	OK                   *bool                 `json:"ok" yaml:"ok"`
	Changed              *bool                 `json:"changed" yaml:"changed"`
	Verified             *bool                 `json:"verified" yaml:"verified"`
	BoxCount             int                   `json:"box_count" yaml:"box_count"`
	TransactionID        string                `json:"transaction_id" yaml:"transaction_id"`
	Target               BoxMotionGuardTarget  `json:"target" yaml:"target"`
	Backup               *BoxMotionGuardBackup `json:"backup" yaml:"backup"`
	UseIRQ               *bool                 `json:"use_irq" yaml:"use_irq"`
	DebounceUSPresent    *bool                 `json:"debounce_us_present" yaml:"debounce_us_present"`
	ActiveConfigChanged  *bool                 `json:"active_config_changed" yaml:"active_config_changed"`
	KlipperServiceAction string                `json:"klipper_service_action" yaml:"klipper_service_action"`
	KlipperRestarted     *bool                 `json:"klipper_restarted" yaml:"klipper_restarted"`
	ActivationRequired   *bool                 `json:"activation_required" yaml:"activation_required"`
	Error                string                `json:"error,omitempty" yaml:"error,omitempty"`
}

func (result BoxMotionGuardResult) Succeeded() bool {
	return result.OK != nil && *result.OK && result.Verified != nil && *result.Verified
}

func (result BoxMotionGuardResult) DidChange() bool {
	return result.Changed != nil && *result.Changed
}

func ParseBoxMotionGuard(raw []byte) (BoxMotionGuardResult, error) {
	return parseBoxMotionTemplate(raw, false, false, "box-motion-guard-")
}

func ParseBoxMotionVendorRestore(raw []byte) (BoxMotionGuardResult, error) {
	return parseBoxMotionTemplate(raw, true, true, "box-motion-vendor-")
}

func parseBoxMotionTemplate(
	raw []byte,
	expectedUseIRQ bool,
	expectedDebounce bool,
	backupPrefix string,
) (BoxMotionGuardResult, error) {
	var result BoxMotionGuardResult
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return result, fmt.Errorf("Box motion guard returned no JSON result")
	}
	if len(trimmed) > maximumBoxMotionGuardResult {
		return result, fmt.Errorf("Box motion guard result exceeds %d bytes", maximumBoxMotionGuardResult)
	}
	if trimmed[0] != '{' {
		return result, fmt.Errorf("Box motion guard result must be one JSON object")
	}
	if err := rejectDuplicateJSONFields(trimmed); err != nil {
		return result, err
	}
	knownShape := map[string]map[string]struct{}{
		"schema": nil, "ok": nil, "changed": nil, "verified": nil,
		"box_count": nil, "transaction_id": nil,
		"target": {
			"path": {}, "before_sha256": {}, "after_sha256": {},
			"mode": {}, "uid": {}, "gid": {},
		},
		"backup":  {"path": {}, "sha256": {}},
		"use_irq": nil, "debounce_us_present": nil,
		"active_config_changed": nil, "klipper_service_action": nil,
		"klipper_restarted": nil, "activation_required": nil, "error": nil,
	}
	if err := rejectCaseAliasedJSONFields(trimmed, "Box motion guard result", knownShape); err != nil {
		return result, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return result, fmt.Errorf("Box motion guard result must be one JSON object")
	}
	for _, name := range []string{
		"schema", "ok", "changed", "verified", "box_count", "transaction_id",
		"target", "backup", "use_irq", "debounce_us_present",
		"active_config_changed", "klipper_service_action", "klipper_restarted",
		"activation_required",
	} {
		if _, present := fields[name]; !present {
			return result, fmt.Errorf("Box motion guard result omitted required field %q", name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode Box motion guard result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return result, fmt.Errorf("Box motion guard returned trailing output")
	}
	if err := validateBoxMotionGuard(
		result, expectedUseIRQ, expectedDebounce, backupPrefix,
	); err != nil {
		return result, err
	}
	return result, nil
}

func validateBoxMotionGuard(
	result BoxMotionGuardResult,
	expectedUseIRQ bool,
	expectedDebounce bool,
	backupPrefix string,
) error {
	if result.Schema != BoxMotionGuardSchema {
		return fmt.Errorf("Box motion guard returned schema %q; expected %q", result.Schema, BoxMotionGuardSchema)
	}
	if result.OK == nil || result.Changed == nil || result.Verified == nil ||
		result.UseIRQ == nil || result.DebounceUSPresent == nil ||
		result.ActiveConfigChanged == nil || result.KlipperRestarted == nil ||
		result.ActivationRequired == nil {
		return fmt.Errorf("Box motion guard omitted required boolean proof")
	}
	if result.BoxCount < 1 || result.BoxCount > 4 {
		return fmt.Errorf("Box motion guard returned invalid Box count %d", result.BoxCount)
	}
	if !boxClockGuardTxnPattern.MatchString(result.TransactionID) {
		return fmt.Errorf("Box motion guard returned an invalid transaction ID")
	}
	expectedTarget := fmt.Sprintf("/home/mks/cfg_with_%d", result.BoxCount)
	if result.Target.Path != expectedTarget ||
		!boxClockGuardDigestPattern.MatchString(result.Target.BeforeSHA256) ||
		!boxClockGuardDigestPattern.MatchString(result.Target.AfterSHA256) ||
		!boxMotionGuardModePattern.MatchString(result.Target.Mode) ||
		result.Target.UID < 0 || result.Target.GID < 0 {
		return fmt.Errorf("Box motion guard did not verify the fixed template target")
	}
	if *result.UseIRQ != expectedUseIRQ ||
		*result.DebounceUSPresent != expectedDebounce ||
		*result.ActiveConfigChanged ||
		result.KlipperServiceAction != "none" || *result.KlipperRestarted ||
		!*result.ActivationRequired {
		return fmt.Errorf("Box motion guard returned an invalid safety or activation contract")
	}
	if *result.OK {
		if !*result.Verified || strings.TrimSpace(result.Error) != "" {
			return fmt.Errorf("successful Box motion guard returned inconsistent outcome proof")
		}
		if *result.Changed {
			expectedBackup := "/home/mks/.qidi-plus4-backups/" + backupPrefix +
				result.TransactionID + fmt.Sprintf("/cfg_with_%d", result.BoxCount)
			if result.Backup == nil || result.Backup.Path != expectedBackup ||
				result.Backup.SHA256 != result.Target.BeforeSHA256 ||
				result.Target.BeforeSHA256 == result.Target.AfterSHA256 {
				return fmt.Errorf("changed Box motion guard returned invalid rollback proof")
			}
		} else if result.Backup != nil || result.Target.BeforeSHA256 != result.Target.AfterSHA256 {
			return fmt.Errorf("unchanged Box motion guard returned inconsistent proof")
		}
	} else if *result.Verified || strings.TrimSpace(result.Error) == "" {
		return fmt.Errorf("failed Box motion guard returned inconsistent outcome proof")
	}
	return nil
}
