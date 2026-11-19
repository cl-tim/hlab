package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	PLRDeploymentSchema = "qidi-plus4-plr-deployment/v1"
	KlipperStartSchema  = "qidi-plus4-klipper-start/v1"
	PLRRollbackSchema   = "qidi-plus4-plr-rollback/v1"
	KlipperService      = "klipper.service"

	PLRTransactionNotStarted     = "not_started"
	PLRTransactionApplied        = "applied"
	PLRTransactionAutoRolledBack = "auto_rolled_back"
	PLRTransactionRolledBack     = "rolled_back"
	PLRTransactionSafetyRestored = "safety_restored"
	PLRTransactionPartial        = "partial"
	PLRTransactionIndeterminate  = "indeterminate"
)

var plrAccountPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

type PLRStabilitySample struct {
	State string `json:"state" yaml:"state"`
	Exit  *int   `json:"exit" yaml:"exit"`
}

// PLRPaths identifies the Klipper account and paths for a recovery operation.
// The v1 result requires the complete field set.
type PLRPaths struct {
	Home        string `json:"home" yaml:"home"`
	KlipperDir  string `json:"klipper_dir" yaml:"klipper_dir"`
	PrinterData string `json:"printer_data" yaml:"printer_data"`
	ScriptsDir  string `json:"scripts_dir" yaml:"scripts_dir"`
	Python      string `json:"python" yaml:"python"`
	GCodeRoot   string `json:"gcode_root" yaml:"gcode_root"`
	Checkpoint  string `json:"checkpoint" yaml:"checkpoint"`
	Record      string `json:"record" yaml:"record"`
	Output      string `json:"output" yaml:"output"`
}

func (paths *PLRPaths) UnmarshalJSON(raw []byte) error {
	type plainPaths PLRPaths
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		"home": {}, "klipper_dir": {}, "printer_data": {}, "scripts_dir": {},
		"python": {}, "gcode_root": {}, "checkpoint": {}, "record": {}, "output": {},
	}
	for name := range fields {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("PLR paths returned unexpected field %q", name)
		}
	}
	if len(fields) != len(allowed) {
		return fmt.Errorf("PLR paths must contain exactly %d fields", len(allowed))
	}
	var decoded plainPaths
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	*paths = PLRPaths(decoded)
	return nil
}

type plrNestedResult struct {
	BackupManifest string `json:"backup_manifest,omitempty"`
	SafetyManifest string `json:"safety_manifest,omitempty"`
	Error          string `json:"error,omitempty"`
}

// PLROperationResult contains lifecycle status, recovery handles, and diagnostics.
// Detailed installer results are retained in the utility's JSON output.
type PLROperationResult struct {
	Schema             string               `json:"schema" yaml:"schema"`
	OK                 *bool                `json:"ok" yaml:"ok"`
	Account            string               `json:"account,omitempty" yaml:"account,omitempty"`
	Paths              PLRPaths             `json:"paths,omitempty" yaml:"paths,omitempty"`
	TransactionState   string               `json:"transaction_state,omitempty" yaml:"transaction_state,omitempty"`
	Changed            *bool                `json:"changed,omitempty" yaml:"changed,omitempty"`
	Applied            *bool                `json:"applied,omitempty" yaml:"applied,omitempty"`
	Verified           *bool                `json:"verified,omitempty" yaml:"verified,omitempty"`
	KlipperLeftStopped *bool                `json:"klipper_left_stopped,omitempty" yaml:"klipper_left_stopped,omitempty"`
	BackupManifest     string               `json:"backup_manifest,omitempty" yaml:"backup_manifest,omitempty"`
	SafetyManifest     string               `json:"safety_manifest,omitempty" yaml:"safety_manifest,omitempty"`
	RollbackManifest   string               `json:"rollback_manifest,omitempty" yaml:"rollback_manifest,omitempty"`
	Service            string               `json:"service,omitempty" yaml:"service,omitempty"`
	ObservedState      string               `json:"observed_state,omitempty" yaml:"observed_state,omitempty"`
	Phase              string               `json:"phase,omitempty" yaml:"phase,omitempty"`
	Error              string               `json:"error,omitempty" yaml:"error,omitempty"`
	NextCommand        string               `json:"next_command,omitempty" yaml:"next_command,omitempty"`
	StartExit          *int                 `json:"start_exit,omitempty" yaml:"start_exit,omitempty"`
	StateExit          *int                 `json:"state_exit,omitempty" yaml:"state_exit,omitempty"`
	StabilityChecks    int                  `json:"stability_checks_required,omitempty" yaml:"stability_checks_required,omitempty"`
	StabilityInterval  float64              `json:"stability_interval_seconds,omitempty" yaml:"stability_interval_seconds,omitempty"`
	StabilitySamples   []PLRStabilitySample `json:"stability_samples,omitempty" yaml:"stability_samples,omitempty"`
	Plan               plrNestedResult      `json:"plan,omitempty" yaml:"-"`
	Installation       plrNestedResult      `json:"installation,omitempty" yaml:"-"`
	Rollback           plrNestedResult      `json:"rollback,omitempty" yaml:"-"`
	Verification       plrNestedResult      `json:"verification,omitempty" yaml:"-"`
	appliedPresent     bool
}

func (result *PLROperationResult) UnmarshalJSON(raw []byte) error {
	type plainResult PLROperationResult
	var decoded plainResult
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*result = PLROperationResult(decoded)
	_, result.appliedPresent = fields["applied"]
	return nil
}

func (result PLROperationResult) Succeeded() bool {
	return result.OK != nil && *result.OK
}

func (result PLROperationResult) DidChange() bool {
	return result.Changed != nil && *result.Changed
}

// DiagnosticSummary combines and deduplicates wrapper and remote installer errors.
func (result PLROperationResult) DiagnosticSummary() string {
	parts := make([]string, 0, 5)
	seen := map[string]struct{}{}
	appendDiagnostic := func(label, diagnostic string) {
		diagnostic = SafeDiagnosticSummary([]byte(diagnostic))
		if diagnostic == "" {
			return
		}
		if _, exists := seen[diagnostic]; exists {
			return
		}
		seen[diagnostic] = struct{}{}
		if label != "" {
			diagnostic = label + ": " + diagnostic
		}
		parts = append(parts, diagnostic)
	}
	appendDiagnostic("", result.Error)
	appendDiagnostic("plan", result.Plan.Error)
	appendDiagnostic("installer", result.Installation.Error)
	appendDiagnostic("verification", result.Verification.Error)
	appendDiagnostic("rollback", result.Rollback.Error)
	diagnostic := strings.Join(parts, "; ")
	if result.Phase != "" && diagnostic != "" {
		diagnostic = "phase " + SafeDiagnosticSummary([]byte(result.Phase)) + ": " + diagnostic
	}
	return SafeDiagnosticSummary([]byte(diagnostic))
}

// ParsePLROperation accepts exactly one JSON document and validates its schema.
// It never searches printer files or guesses a backup path.
func ParsePLROperation(raw []byte, expectedSchema string) (PLROperationResult, error) {
	var result PLROperationResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode firmware operation result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return result, fmt.Errorf("firmware operation returned more than one JSON document")
		}
		return result, fmt.Errorf("decode trailing firmware operation output: %w", err)
	}
	if result.Schema != expectedSchema {
		return result, fmt.Errorf(
			"firmware operation returned schema %q; expected %q",
			result.Schema, expectedSchema,
		)
	}
	backup, err := promotePLRHandle(
		"backup_manifest", result.BackupManifest,
		result.Installation.BackupManifest, result.Rollback.BackupManifest,
		result.Verification.BackupManifest,
	)
	if err != nil {
		return result, err
	}
	result.BackupManifest = backup
	safety, err := promotePLRHandle(
		"safety_manifest", result.SafetyManifest,
		result.Installation.SafetyManifest, result.Rollback.SafetyManifest,
		result.Verification.SafetyManifest,
	)
	if err != nil {
		return result, err
	}
	result.SafetyManifest = safety
	return result, nil
}

func promotePLRHandle(name, authoritative string, nested ...string) (string, error) {
	selected := authoritative
	for _, candidate := range nested {
		if candidate == "" {
			continue
		}
		if selected != "" && candidate != selected {
			return "", fmt.Errorf("firmware operation returned conflicting %s values", name)
		}
		selected = candidate
	}
	return selected, nil
}

// CanonicalPLRPaths returns the only remote layout accepted by the reviewed
// QIDI PLR wrapper. It validates the account before constructing any path.
func CanonicalPLRPaths(account string) (PLRPaths, error) {
	if !plrAccountPattern.MatchString(account) {
		return PLRPaths{}, fmt.Errorf("invalid PLR target account %q", account)
	}
	home := "/home/" + account
	scripts := home + "/scripts/plr"
	printerData := home + "/printer_data"
	gcodeRoot := printerData + "/gcodes"
	return PLRPaths{
		Home: home, KlipperDir: home + "/klipper", PrinterData: printerData,
		ScriptsDir: scripts, Python: home + "/klippy-env/bin/python",
		GCodeRoot: gcodeRoot, Checkpoint: scripts + "/plr_checkpoint.json",
		Record: scripts + "/plr_record", Output: gcodeRoot + "/.plr/plr.gcode",
	}, nil
}

// ValidatePLRTarget binds a parsed deploy/rollback result to the effective
// printer account from inventory and validates every canonical remote path and
// recovery handle. It performs no printer discovery.
func ValidatePLRTarget(result PLROperationResult, expectedAccount string) error {
	expected, err := CanonicalPLRPaths(expectedAccount)
	if err != nil {
		return err
	}
	if result.Account != expectedAccount {
		return fmt.Errorf(
			"PLR operation targeted account %q; expected inventory account %q",
			result.Account, expectedAccount,
		)
	}
	for _, proof := range []struct {
		name     string
		actual   string
		expected string
	}{
		{"home", result.Paths.Home, expected.Home},
		{"klipper_dir", result.Paths.KlipperDir, expected.KlipperDir},
		{"printer_data", result.Paths.PrinterData, expected.PrinterData},
		{"scripts_dir", result.Paths.ScriptsDir, expected.ScriptsDir},
		{"python", result.Paths.Python, expected.Python},
		{"gcode_root", result.Paths.GCodeRoot, expected.GCodeRoot},
		{"checkpoint", result.Paths.Checkpoint, expected.Checkpoint},
		{"record", result.Paths.Record, expected.Record},
		{"output", result.Paths.Output, expected.Output},
	} {
		if proof.actual != proof.expected {
			return fmt.Errorf(
				"PLR operation returned paths.%s %q; expected %q",
				proof.name, proof.actual, proof.expected,
			)
		}
	}
	if result.BackupManifest != "" {
		if err := validatePLRBackupManifest(result.BackupManifest, expected); err != nil {
			return fmt.Errorf("PLR operation returned %w", err)
		}
	}
	if result.SafetyManifest != "" &&
		!validPLRManifest(result.SafetyManifest, expected.ScriptsDir, "plr-rollback-safety-", "safety-manifest.json") {
		return fmt.Errorf("PLR operation returned non-canonical safety_manifest %q", result.SafetyManifest)
	}
	return nil
}

// ValidatePLRBackupManifest rejects a caller-supplied handle unless it names an
// exact installer manifest under the effective printer account's canonical
// scripts directory. Callers can therefore fail before any network request.
func ValidatePLRBackupManifest(manifest, account string) error {
	paths, err := CanonicalPLRPaths(account)
	if err != nil {
		return err
	}
	return validatePLRBackupManifest(manifest, paths)
}

func validatePLRBackupManifest(manifest string, paths PLRPaths) error {
	if !validPLRManifest(manifest, paths.ScriptsDir, "plr-hardening-", "backup-manifest.json") {
		return fmt.Errorf("non-canonical backup_manifest %q", manifest)
	}
	return nil
}

func validPLRManifest(manifest, scriptsDir, directoryPrefix, filename string) bool {
	prefix := scriptsDir + "/backups/" + directoryPrefix
	suffix := "/" + filename
	if !strings.HasPrefix(manifest, prefix) || !strings.HasSuffix(manifest, suffix) {
		return false
	}
	token := strings.TrimSuffix(strings.TrimPrefix(manifest, prefix), suffix)
	if token == "" {
		return false
	}
	for _, character := range token {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// ValidatePLRLifecycle checks successful stop/apply/verify results.
// Failed results retain their rollback instructions for display.
func ValidatePLRLifecycle(result PLROperationResult) error {
	if result.OK == nil {
		return fmt.Errorf("PLR lifecycle result omitted required boolean ok")
	}
	switch result.Schema {
	case PLRDeploymentSchema:
		if err := validatePLRTransaction(result, true); err != nil {
			return fmt.Errorf("PLR deployment %w", err)
		}
		if err := requirePLRBooleans(result); err != nil {
			return fmt.Errorf("PLR deployment %w", err)
		}
		if !result.Succeeded() {
			switch result.TransactionState {
			case PLRTransactionNotStarted:
				if result.BackupManifest != "" || result.SafetyManifest != "" {
					return fmt.Errorf("not-started PLR deployment unexpectedly returned a recovery manifest")
				}
			case PLRTransactionAutoRolledBack, PLRTransactionPartial, PLRTransactionIndeterminate:
				if result.BackupManifest == "" {
					return fmt.Errorf("PLR deployment %s failure omitted recovery backup_manifest", result.TransactionState)
				}
				if result.SafetyManifest != "" {
					return fmt.Errorf("PLR deployment %s failure unexpectedly returned safety_manifest", result.TransactionState)
				}
			}
			return nil
		}
		if result.DidChange() {
			if result.TransactionState != PLRTransactionApplied {
				return fmt.Errorf("changed PLR deployment did not report transaction_state applied")
			}
			if !*result.Applied || !*result.Verified || !*result.KlipperLeftStopped {
				return fmt.Errorf("PLR deployment did not prove apply, verification, and stopped Klipper")
			}
			if result.BackupManifest == "" {
				return fmt.Errorf("PLR deployment did not return backup_manifest")
			}
			if result.SafetyManifest != "" {
				return fmt.Errorf("PLR deployment unexpectedly returned safety_manifest")
			}
			return nil
		}
		if result.TransactionState != PLRTransactionNotStarted {
			return fmt.Errorf("unchanged PLR deployment did not report transaction_state not_started")
		}
		if *result.Applied || !*result.Verified || *result.KlipperLeftStopped {
			return fmt.Errorf(
				"unchanged PLR deployment did not prove unapplied, verified, and running-state-preserving invariants",
			)
		}
		if result.BackupManifest != "" || result.SafetyManifest != "" {
			return fmt.Errorf("unchanged PLR deployment unexpectedly returned a recovery manifest")
		}
	case PLRRollbackSchema:
		if err := validatePLRTransaction(result, false); err != nil {
			return fmt.Errorf("PLR rollback %w", err)
		}
		if err := requirePLRBooleans(result); err != nil {
			return fmt.Errorf("PLR rollback %w", err)
		}
		if result.BackupManifest == "" {
			return fmt.Errorf("PLR rollback did not echo backup_manifest")
		}
		if !result.Succeeded() {
			switch result.TransactionState {
			case PLRTransactionNotStarted:
				if result.SafetyManifest != "" {
					return fmt.Errorf("not-started PLR rollback unexpectedly returned safety_manifest")
				}
			case PLRTransactionSafetyRestored, PLRTransactionPartial, PLRTransactionIndeterminate:
				if result.SafetyManifest == "" {
					return fmt.Errorf("PLR rollback %s failure omitted safety_manifest", result.TransactionState)
				}
			}
			return nil
		}
		if result.DidChange() {
			if result.TransactionState != PLRTransactionRolledBack {
				return fmt.Errorf("changed PLR rollback did not report transaction_state rolled_back")
			}
			if !*result.Applied || !*result.Verified || !*result.KlipperLeftStopped {
				return fmt.Errorf("PLR rollback did not prove apply, verification, and stopped Klipper")
			}
			if result.SafetyManifest == "" {
				return fmt.Errorf("changed PLR rollback omitted safety_manifest")
			}
			return nil
		}
		if result.TransactionState != PLRTransactionNotStarted {
			return fmt.Errorf("unchanged PLR rollback did not report transaction_state not_started")
		}
		if result.SafetyManifest != "" {
			return fmt.Errorf("unchanged PLR rollback unexpectedly returned safety_manifest")
		}
		if *result.Applied || !*result.Verified || *result.KlipperLeftStopped {
			return fmt.Errorf(
				"unchanged PLR rollback did not prove unapplied, verified, and running-state-preserving invariants",
			)
		}
	case KlipperStartSchema:
		if result.Service != KlipperService {
			return fmt.Errorf(
				"Klipper start targeted service %q; expected %q",
				result.Service, KlipperService,
			)
		}
		if result.StartExit == nil || result.StateExit == nil {
			return fmt.Errorf("Klipper start omitted required exit-code proof")
		}
		if result.ObservedState == "" || result.StabilityChecks < 2 ||
			result.StabilityInterval <= 0 || len(result.StabilitySamples) == 0 ||
			len(result.StabilitySamples) > result.StabilityChecks {
			return fmt.Errorf("Klipper start did not return a bounded stability proof")
		}
		for _, sample := range result.StabilitySamples {
			if sample.Exit == nil || sample.State == "" {
				return fmt.Errorf("Klipper start stability sample omitted required proof")
			}
		}
		last := result.StabilitySamples[len(result.StabilitySamples)-1]
		if *result.StateExit != *last.Exit || result.ObservedState != last.State {
			return fmt.Errorf("Klipper start returned inconsistent final stability proof")
		}
		if !result.Succeeded() {
			return nil
		}
		if result.ObservedState != "active" {
			return fmt.Errorf("Klipper start did not prove the service active")
		}
		if *result.StartExit != 0 || *result.StateExit != 0 ||
			len(result.StabilitySamples) != result.StabilityChecks {
			return fmt.Errorf("Klipper start did not return a bounded stable-active proof")
		}
		for _, sample := range result.StabilitySamples {
			if *sample.Exit != 0 || sample.State != "active" {
				return fmt.Errorf("Klipper start stability proof contains a non-active sample")
			}
		}
	default:
		return fmt.Errorf("unsupported PLR lifecycle schema %q", result.Schema)
	}
	return nil
}

func requirePLRBooleans(result PLROperationResult) error {
	for _, proof := range []struct {
		name  string
		value *bool
	}{
		{"changed", result.Changed},
		{"verified", result.Verified},
		{"klipper_left_stopped", result.KlipperLeftStopped},
	} {
		if proof.value == nil {
			return fmt.Errorf("omitted required boolean %s", proof.name)
		}
	}
	if !result.appliedPresent && result.Applied == nil {
		return fmt.Errorf("omitted required boolean applied")
	}
	nullable := !result.Succeeded() &&
		(result.TransactionState == PLRTransactionPartial ||
			result.TransactionState == PLRTransactionIndeterminate)
	if nullable {
		if result.Applied != nil {
			return fmt.Errorf("%s failure must report applied as null", result.TransactionState)
		}
		return nil
	}
	if result.Applied == nil {
		return fmt.Errorf("applied must be an explicit boolean")
	}
	if !result.Succeeded() && *result.Applied {
		return fmt.Errorf("failed %s transaction must report applied as false", result.TransactionState)
	}
	return nil
}

func validatePLRTransaction(result PLROperationResult, deployment bool) error {
	allowed := map[string]bool{}
	if deployment {
		allowed = map[string]bool{
			PLRTransactionNotStarted: true, PLRTransactionApplied: true,
			PLRTransactionAutoRolledBack: true, PLRTransactionPartial: true,
			PLRTransactionIndeterminate: true,
		}
	} else {
		allowed = map[string]bool{
			PLRTransactionNotStarted: true, PLRTransactionRolledBack: true,
			PLRTransactionSafetyRestored: true, PLRTransactionPartial: true,
			PLRTransactionIndeterminate: true,
		}
	}
	if !allowed[result.TransactionState] {
		return fmt.Errorf("returned unsupported transaction_state %q", result.TransactionState)
	}
	if !result.Succeeded() {
		if result.TransactionState == PLRTransactionApplied ||
			result.TransactionState == PLRTransactionRolledBack {
			return fmt.Errorf("failed operation reported successful transaction_state %q", result.TransactionState)
		}
		return nil
	}
	if result.TransactionState != PLRTransactionNotStarted &&
		result.TransactionState != PLRTransactionApplied &&
		result.TransactionState != PLRTransactionRolledBack {
		return fmt.Errorf("successful operation reported failure transaction_state %q", result.TransactionState)
	}
	return nil
}
