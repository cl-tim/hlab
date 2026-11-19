package migrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	KlippeeAgentUpgradeSchema  = "qidi-plus4-klippee-agent-upgrade/v1"
	KlippeeAgentUpgradeAction  = "UPGRADE-KLIPPEE-AGENT"
	KlippeeAgentAccount        = "mks"
	KlippeeAgentCLIPath        = "/home/mks/.local/bin/klippee"
	KlippeeAgentOfficialSource = "https://klippee.xyz/install/klippee.py"

	maximumKlippeeAgentUpgradeResult = 32 * 1024
	maximumLinuxPID                  = 4_194_304
)

var klippeeVersionPattern = regexp.MustCompile(
	`^(20[0-9]{2})\.(0[1-9]|1[0-2])\.(0[1-9]|[12][0-9]|3[01])(?:\.(0|[1-9][0-9]*))?$`,
)

type KlippeeAgentArtifact struct {
	Version string `json:"version" yaml:"version"`
	SHA256  string `json:"sha256" yaml:"sha256"`
}

type KlippeeAgentUpgradeResult struct {
	Schema                    string               `json:"schema" yaml:"schema"`
	OK                        *bool                `json:"ok" yaml:"ok"`
	Changed                   *bool                `json:"changed" yaml:"changed"`
	Account                   string               `json:"account" yaml:"account"`
	CLIPath                   string               `json:"cli_path" yaml:"cli_path"`
	SourceURL                 string               `json:"source_url" yaml:"source_url"`
	Before                    KlippeeAgentArtifact `json:"before" yaml:"before"`
	After                     KlippeeAgentArtifact `json:"after" yaml:"after"`
	Service                   string               `json:"service" yaml:"service"`
	Scope                     string               `json:"scope" yaml:"scope"`
	ObservedState             string               `json:"observed_state" yaml:"observed_state"`
	BeforePID                 *int                 `json:"before_pid" yaml:"before_pid"`
	AfterPID                  *int                 `json:"after_pid" yaml:"after_pid"`
	UpgradeExit               *int                 `json:"upgrade_exit" yaml:"upgrade_exit"`
	StateExit                 *int                 `json:"state_exit" yaml:"state_exit"`
	UpgradeReportedSuccess    *bool                `json:"upgrade_reported_success" yaml:"upgrade_reported_success"`
	AgentRestartPerformed     *bool                `json:"agent_restart_performed" yaml:"agent_restart_performed"`
	KlipperActionsPerformed   *bool                `json:"klipper_actions_performed" yaml:"klipper_actions_performed"`
	ConfigDeploymentPerformed *bool                `json:"config_deployment_performed" yaml:"config_deployment_performed"`
	Error                     string               `json:"error,omitempty" yaml:"error,omitempty"`
}

func (result KlippeeAgentUpgradeResult) Succeeded() bool {
	return result.OK != nil && *result.OK
}

func (result KlippeeAgentUpgradeResult) DidChange() bool {
	return result.Changed != nil && *result.Changed
}

func (result KlippeeAgentUpgradeResult) DiagnosticSummary() string {
	return SafeDiagnosticSummary([]byte(result.Error))
}

// ParseKlippeeAgentUpgrade consumes the exact bounded v1 proof emitted by the
// reviewed firmware operation. Unknown, duplicate, case-aliased, missing, or
// trailing fields fail closed before any lifecycle claim is trusted.
func ParseKlippeeAgentUpgrade(raw []byte) (KlippeeAgentUpgradeResult, error) {
	var result KlippeeAgentUpgradeResult
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return result, fmt.Errorf("Klippee agent upgrade returned no JSON result")
	}
	if len(trimmed) > maximumKlippeeAgentUpgradeResult {
		return result, fmt.Errorf(
			"Klippee agent upgrade result exceeds %d bytes",
			maximumKlippeeAgentUpgradeResult,
		)
	}
	if trimmed[0] != '{' {
		return result, fmt.Errorf("Klippee agent upgrade result must be one JSON object")
	}
	if err := rejectDuplicateJSONFields(trimmed); err != nil {
		return result, err
	}
	if err := requireExactKlippeeAgentUpgradeShape(trimmed); err != nil {
		return result, err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode Klippee agent upgrade result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return result, fmt.Errorf("Klippee agent upgrade returned more than one JSON document")
		}
		return result, fmt.Errorf("decode trailing Klippee agent upgrade output: %w", err)
	}
	if err := validateKlippeeAgentUpgrade(result); err != nil {
		return result, err
	}
	return result, nil
}

func requireExactKlippeeAgentUpgradeShape(raw json.RawMessage) error {
	topFields, err := exactUpgradeObject(raw, "Klippee agent upgrade result", []string{
		"schema", "ok", "changed", "account", "cli_path", "source_url",
		"before", "after", "service", "scope", "observed_state",
		"before_pid", "after_pid", "upgrade_exit", "state_exit",
		"upgrade_reported_success", "agent_restart_performed",
		"klipper_actions_performed", "config_deployment_performed",
	}, []string{"error"})
	if err != nil {
		return err
	}
	for _, name := range []string{"before", "after"} {
		if _, err := exactUpgradeObject(
			topFields[name], "Klippee agent upgrade result."+name,
			[]string{"version", "sha256"}, nil,
		); err != nil {
			return err
		}
	}
	return nil
}

func exactUpgradeObject(
	raw json.RawMessage,
	path string,
	required []string,
	optional []string,
) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err != nil {
			return nil, fmt.Errorf("%s must be a JSON object: %w", path, err)
		}
		return nil, fmt.Errorf("%s must be a JSON object", path)
	}
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, name := range append(append([]string{}, required...), optional...) {
		allowed[name] = struct{}{}
	}
	for name := range fields {
		if _, present := allowed[name]; !present {
			for canonical := range allowed {
				if strings.EqualFold(name, canonical) {
					return nil, fmt.Errorf("%s contains case-aliased field %q", path, name)
				}
			}
			return nil, fmt.Errorf("%s contains unexpected field %q", path, name)
		}
	}
	for _, name := range required {
		if _, present := fields[name]; !present {
			return nil, fmt.Errorf("%s omitted required field %q", path, name)
		}
	}
	return fields, nil
}

type klippeeVersion struct {
	date     time.Time
	revision uint64
}

func parseKlippeeVersion(version string) (klippeeVersion, error) {
	match := klippeeVersionPattern.FindStringSubmatch(version)
	if match == nil {
		return klippeeVersion{}, fmt.Errorf("version %q is not a date-style Klippee version", version)
	}
	date, err := time.Parse("2006.01.02", strings.Join(match[1:4], "."))
	if err != nil {
		return klippeeVersion{}, fmt.Errorf("version %q contains an invalid date", version)
	}
	var revision uint64
	if match[4] != "" {
		revision, err = strconv.ParseUint(match[4], 10, 32)
		if err != nil {
			return klippeeVersion{}, fmt.Errorf("version %q has an invalid revision", version)
		}
	}
	return klippeeVersion{date: date, revision: revision}, nil
}

func compareKlippeeVersions(left, right klippeeVersion) int {
	if left.date.Before(right.date) {
		return -1
	}
	if left.date.After(right.date) {
		return 1
	}
	if left.revision < right.revision {
		return -1
	}
	if left.revision > right.revision {
		return 1
	}
	return 0
}

func validateKlippeeAgentUpgrade(result KlippeeAgentUpgradeResult) error {
	if result.Schema != KlippeeAgentUpgradeSchema {
		return fmt.Errorf(
			"Klippee agent upgrade returned schema %q; expected %q",
			result.Schema, KlippeeAgentUpgradeSchema,
		)
	}
	if result.OK == nil || result.Changed == nil {
		return fmt.Errorf("Klippee agent upgrade omitted required outcome proof")
	}
	if result.Account != KlippeeAgentAccount || result.CLIPath != KlippeeAgentCLIPath ||
		result.SourceURL != KlippeeAgentOfficialSource {
		return fmt.Errorf("Klippee agent upgrade returned an unexpected account, CLI path, or source URL")
	}
	if result.Service != KlippeeAgentService || result.Scope != "user" {
		return fmt.Errorf(
			"Klippee agent upgrade targeted unexpected service %q scope %q",
			result.Service, result.Scope,
		)
	}
	beforeVersion, err := parseKlippeeVersion(result.Before.Version)
	if err != nil {
		return fmt.Errorf("invalid before.version: %w", err)
	}
	afterVersion, err := parseKlippeeVersion(result.After.Version)
	if err != nil {
		return fmt.Errorf("invalid after.version: %w", err)
	}
	if compareKlippeeVersions(afterVersion, beforeVersion) < 0 {
		return fmt.Errorf("Klippee agent upgrade reported a version downgrade")
	}
	if !boxClockGuardDigestPattern.MatchString(result.Before.SHA256) ||
		!boxClockGuardDigestPattern.MatchString(result.After.SHA256) {
		return fmt.Errorf("Klippee agent upgrade returned an invalid SHA-256 proof")
	}
	artifactChanged := result.Before.Version != result.After.Version ||
		result.Before.SHA256 != result.After.SHA256
	if *result.Changed != artifactChanged {
		return fmt.Errorf("Klippee agent upgrade returned changed inconsistent with version/digest evidence")
	}
	for name, value := range map[string]*bool{
		"upgrade_reported_success":    result.UpgradeReportedSuccess,
		"agent_restart_performed":     result.AgentRestartPerformed,
		"klipper_actions_performed":   result.KlipperActionsPerformed,
		"config_deployment_performed": result.ConfigDeploymentPerformed,
	} {
		if value == nil {
			return fmt.Errorf("Klippee agent upgrade omitted required boolean %s", name)
		}
	}
	if *result.KlipperActionsPerformed || *result.ConfigDeploymentPerformed {
		return fmt.Errorf("Klippee agent upgrade did not prove zero Klipper actions and config deployments")
	}
	if result.BeforePID == nil || result.AfterPID == nil ||
		result.UpgradeExit == nil || result.StateExit == nil {
		return fmt.Errorf("Klippee agent upgrade omitted PID or exit-code proof")
	}
	if *result.UpgradeExit < -1 || *result.UpgradeExit > 255 ||
		*result.StateExit < -1 || *result.StateExit > 255 {
		return fmt.Errorf("Klippee agent upgrade returned an invalid exit-code proof")
	}
	if (*result.UpgradeExit == 0) != *result.UpgradeReportedSuccess {
		return fmt.Errorf("Klippee agent upgrade returned inconsistent upgrade exit/success proof")
	}
	validStates := map[string]bool{
		"active": true, "inactive": true, "failed": true, "activating": true,
		"deactivating": true, "reloading": true, "unknown": true,
	}
	if !validStates[result.ObservedState] ||
		((*result.StateExit == 0) != (result.ObservedState == "active")) {
		return fmt.Errorf("Klippee agent upgrade returned inconsistent service-state proof")
	}
	if *result.BeforePID <= 0 || *result.BeforePID > maximumLinuxPID ||
		*result.AfterPID < 0 || *result.AfterPID > maximumLinuxPID {
		return fmt.Errorf("Klippee agent upgrade returned an invalid PID proof")
	}
	if !*result.AgentRestartPerformed && *result.AfterPID != *result.BeforePID {
		return fmt.Errorf("Klippee agent PID changed without a reported agent restart")
	}
	provenSuccess := result.ObservedState == "active" && *result.UpgradeExit == 0 &&
		*result.StateExit == 0 && *result.UpgradeReportedSuccess &&
		*result.AgentRestartPerformed && *result.AfterPID > 0 &&
		*result.AfterPID != *result.BeforePID
	if result.Succeeded() != provenSuccess {
		return fmt.Errorf("Klippee agent upgrade outcome is inconsistent with its lifecycle proof")
	}
	if result.Succeeded() {
		if result.Error != "" {
			return fmt.Errorf("successful Klippee agent upgrade unexpectedly returned an error")
		}
	} else if strings.TrimSpace(result.Error) == "" {
		return fmt.Errorf("failed Klippee agent upgrade omitted its error")
	}
	return nil
}
