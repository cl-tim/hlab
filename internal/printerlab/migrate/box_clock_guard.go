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

const (
	BoxClockGuardInstallSchema = "qidi-plus4-box-output-clock-guard-install/v1"
	BoxClockGuardInstallAction = "INSTALL-BOX-OUTPUT-CLOCK-GUARD"
	BoxClockGuardAccount       = "mks"
	BoxClockGuardTargetPath    = "/home/mks/klipper/klippy/extras/box_output_clock_guard.py"
	BoxClockGuardTargetMode    = "0644"
	BoxClockGuardConfigSection = "[box_output_clock_guard]"

	maximumBoxClockGuardResult = 64 * 1024
)

var (
	boxClockGuardDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	boxClockGuardTxnPattern    = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}$`)
)

type BoxClockGuardFile struct {
	Path   string `json:"path" yaml:"path"`
	SHA256 string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
	Owner  string `json:"owner,omitempty" yaml:"owner,omitempty"`
	Group  string `json:"group,omitempty" yaml:"group,omitempty"`
	Mode   string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// BoxClockGuardInstallResult is the narrow structured contract returned by
// qidi-plus4-firmware's fixed-purpose installer. Pointer booleans distinguish
// explicit safety proofs from omitted zero values.
type BoxClockGuardInstallResult struct {
	Schema                 string             `json:"schema" yaml:"schema"`
	OK                     *bool              `json:"ok" yaml:"ok"`
	Changed                *bool              `json:"changed,omitempty" yaml:"changed,omitempty"`
	Applied                *bool              `json:"applied,omitempty" yaml:"applied,omitempty"`
	Verified               *bool              `json:"verified" yaml:"verified"`
	Account                string             `json:"account" yaml:"account"`
	TransactionID          string             `json:"transaction_id" yaml:"transaction_id"`
	SourceSHA256           string             `json:"source_sha256" yaml:"source_sha256"`
	Target                 BoxClockGuardFile  `json:"target" yaml:"target"`
	Backup                 *BoxClockGuardFile `json:"backup" yaml:"backup"`
	KlipperServiceAction   string             `json:"klipper_service_action" yaml:"klipper_service_action"`
	KlipperRestarted       *bool              `json:"klipper_restarted" yaml:"klipper_restarted"`
	ConfigChanged          *bool              `json:"config_changed" yaml:"config_changed"`
	ActivationRequired     *bool              `json:"activation_required" yaml:"activation_required"`
	RequiredConfigSection  string             `json:"required_config_section" yaml:"required_config_section"`
	StagingCleanupVerified *bool              `json:"staging_cleanup_verified" yaml:"staging_cleanup_verified"`
	Phase                  string             `json:"phase,omitempty" yaml:"phase,omitempty"`
	Error                  string             `json:"error,omitempty" yaml:"error,omitempty"`
	StagingCleanupError    string             `json:"staging_cleanup_error,omitempty" yaml:"staging_cleanup_error,omitempty"`
}

func (result BoxClockGuardInstallResult) Succeeded() bool {
	return result.OK != nil && *result.OK && result.Verified != nil && *result.Verified &&
		result.StagingCleanupVerified != nil && *result.StagingCleanupVerified
}

func (result BoxClockGuardInstallResult) DidChange() bool {
	return result.Changed != nil && *result.Changed
}

func (result BoxClockGuardInstallResult) DiagnosticSummary() string {
	details := make([]string, 0, 3)
	if result.Phase != "" {
		details = append(details, "phase "+SafeDiagnosticSummary([]byte(result.Phase)))
	}
	if result.Error != "" {
		details = append(details, SafeDiagnosticSummary([]byte(result.Error)))
	}
	if result.StagingCleanupVerified != nil && !*result.StagingCleanupVerified {
		cleanup := "remote staging cleanup was not verified"
		if result.StagingCleanupError != "" {
			cleanup += ": " + SafeDiagnosticSummary([]byte(result.StagingCleanupError))
		}
		details = append(details, cleanup)
	}
	return SafeDiagnosticSummary([]byte(strings.Join(details, "; ")))
}

// ParseBoxClockGuardInstall validates a bounded v1 installation result.
// It rejects duplicate fields and accepts additional diagnostic fields.
func ParseBoxClockGuardInstall(raw []byte) (BoxClockGuardInstallResult, error) {
	var result BoxClockGuardInstallResult
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return result, fmt.Errorf("clock-guard installer returned no JSON result")
	}
	if len(trimmed) > maximumBoxClockGuardResult {
		return result, fmt.Errorf("clock-guard installer result exceeds %d bytes", maximumBoxClockGuardResult)
	}
	if trimmed[0] != '{' {
		return result, fmt.Errorf("clock-guard installer result must be one JSON object")
	}
	if err := rejectDuplicateJSONFields(trimmed); err != nil {
		return result, err
	}
	knownShape := map[string]map[string]struct{}{
		"schema": nil, "ok": nil, "changed": nil, "applied": nil,
		"verified": nil, "account": nil, "transaction_id": nil,
		"source_sha256": nil,
		"target": {
			"path": {}, "sha256": {}, "owner": {}, "group": {}, "mode": {},
		},
		"backup": {
			"path": {}, "sha256": {}, "owner": {}, "group": {}, "mode": {},
		},
		"klipper_service_action": nil, "klipper_restarted": nil,
		"config_changed": nil, "activation_required": nil,
		"required_config_section": nil, "staging_cleanup_verified": nil,
		"phase": nil, "error": nil, "staging_cleanup_error": nil,
	}
	if err := rejectCaseAliasedJSONFields(
		trimmed, "clock-guard installer result", knownShape,
	); err != nil {
		return result, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return result, fmt.Errorf("clock-guard installer result must be one JSON object")
	}
	required := []string{
		"schema", "ok", "verified", "account", "transaction_id", "source_sha256",
		"target", "backup", "klipper_service_action", "klipper_restarted",
		"config_changed", "activation_required", "required_config_section",
		"staging_cleanup_verified",
	}
	for _, name := range required {
		if _, present := fields[name]; !present {
			return result, fmt.Errorf("clock-guard installer result omitted required field %q", name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode clock-guard installer result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return result, fmt.Errorf("clock-guard installer returned more than one JSON document")
		}
		return result, fmt.Errorf("decode trailing clock-guard installer output: %w", err)
	}
	if err := validateBoxClockGuardInstall(result); err != nil {
		return result, err
	}
	return result, nil
}

func rejectCaseAliasedJSONFields(
	raw json.RawMessage,
	path string,
	shape map[string]map[string]struct{},
) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err != nil {
			return fmt.Errorf("%s must be a JSON object: %w", path, err)
		}
		return fmt.Errorf("%s must be a JSON object", path)
	}
	for actual := range fields {
		for canonical := range shape {
			if actual != canonical && strings.EqualFold(actual, canonical) {
				return fmt.Errorf("%s contains case-aliased field %q", path, actual)
			}
		}
	}
	for canonical, children := range shape {
		if children == nil {
			continue
		}
		child, present := fields[canonical]
		if !present {
			continue
		}
		childShape := make(map[string]map[string]struct{}, len(children))
		for name := range children {
			childShape[name] = nil
		}
		if err := rejectCaseAliasedJSONFields(
			child, path+"."+canonical, childShape,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateBoxClockGuardInstall(result BoxClockGuardInstallResult) error {
	if result.Schema != BoxClockGuardInstallSchema {
		return fmt.Errorf(
			"clock-guard installer returned schema %q; expected %q",
			result.Schema, BoxClockGuardInstallSchema,
		)
	}
	if result.OK == nil || result.Verified == nil {
		return fmt.Errorf("clock-guard installer omitted required outcome proof")
	}
	if result.Account != BoxClockGuardAccount {
		return fmt.Errorf("clock-guard installer targeted account %q; expected %q", result.Account, BoxClockGuardAccount)
	}
	if !boxClockGuardTxnPattern.MatchString(result.TransactionID) {
		return fmt.Errorf("clock-guard installer returned an invalid transaction ID")
	}
	if !boxClockGuardDigestPattern.MatchString(result.SourceSHA256) {
		return fmt.Errorf("clock-guard installer returned an invalid source SHA-256")
	}
	if result.Target.Path != BoxClockGuardTargetPath {
		return fmt.Errorf("clock-guard installer targeted path %q; expected the fixed Klipper extra", result.Target.Path)
	}
	if result.KlipperServiceAction != "none" ||
		result.KlipperRestarted == nil || *result.KlipperRestarted ||
		result.ConfigChanged == nil || *result.ConfigChanged {
		return fmt.Errorf("clock-guard installer did not prove zero Klipper and configuration actions")
	}
	if result.ActivationRequired == nil || !*result.ActivationRequired ||
		result.RequiredConfigSection != BoxClockGuardConfigSection {
		return fmt.Errorf("clock-guard installer returned an invalid activation contract")
	}
	if result.StagingCleanupVerified == nil {
		return fmt.Errorf("clock-guard installer omitted its staging-cleanup proof")
	}
	if result.Backup != nil {
		expected := "/home/mks/klipper/klippy/extras/.qidi-plus4-backups/" +
			"box-output-clock-guard-" + result.TransactionID + "/box_output_clock_guard.py"
		if result.Backup.Path != expected || !boxClockGuardDigestPattern.MatchString(result.Backup.SHA256) {
			return fmt.Errorf("clock-guard installer returned an invalid rollback backup proof")
		}
	}
	if *result.OK {
		if !*result.Verified || result.Changed == nil || result.Applied == nil ||
			*result.Changed != *result.Applied {
			return fmt.Errorf("successful clock-guard install returned inconsistent apply/verification state")
		}
		if result.Target.SHA256 != result.SourceSHA256 ||
			result.Target.Owner != BoxClockGuardAccount ||
			result.Target.Group == "" || result.Target.Mode != BoxClockGuardTargetMode {
			return fmt.Errorf("successful clock-guard install did not verify the fixed target digest and metadata")
		}
		if !*result.Changed && result.Backup != nil {
			return fmt.Errorf("unchanged clock-guard install unexpectedly returned a rollback backup")
		}
		if result.Error != "" {
			return fmt.Errorf("successful clock-guard install unexpectedly returned an error")
		}
	} else {
		if *result.Verified {
			return fmt.Errorf("failed clock-guard install reported verified=true")
		}
		if result.Changed != nil && *result.Changed || result.Applied != nil && *result.Applied {
			return fmt.Errorf("failed clock-guard install reported a successful mutation")
		}
		if strings.TrimSpace(result.Error) == "" {
			return fmt.Errorf("failed clock-guard install omitted its error")
		}
	}
	return nil
}

func rejectDuplicateJSONFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeUniqueJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("clock-guard installer returned more than one JSON document")
		}
		return fmt.Errorf("decode trailing clock-guard installer output: %w", err)
	}
	return nil
}

func consumeUniqueJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode clock-guard installer result: %w", err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode clock-guard installer field: %w", err)
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("clock-guard installer object contains a non-string field name")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("clock-guard installer result contains duplicate field %q", name)
			}
			seen[name] = struct{}{}
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	case '[':
		for decoder.More() {
			if err := consumeUniqueJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
	default:
		return fmt.Errorf("clock-guard installer result contains unexpected delimiter %q", delimiter)
	}
	if err != nil {
		return fmt.Errorf("decode clock-guard installer result: %w", err)
	}
	return nil
}
