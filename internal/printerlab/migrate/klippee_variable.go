package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	KlippeeVariableSetSchema = "qidi-plus4-klippee-variable-set/v1"
	KlippeeVariableSetAction = "SET-KLIPPEE-VARIABLE"
	KlippeeVariableCLIPath   = "/home/mks/.local/bin/klippee"
)

var klippeeVariableNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type KlippeeVariableSetResult struct {
	Schema           string `json:"schema" yaml:"schema"`
	OK               *bool  `json:"ok" yaml:"ok"`
	Account          string `json:"account" yaml:"account"`
	CLIPath          string `json:"cli_path" yaml:"cli_path"`
	Name             string `json:"name" yaml:"name"`
	ValueSHA256      string `json:"value_sha256" yaml:"value_sha256"`
	SetReturnCode    int    `json:"set_return_code" yaml:"set_return_code"`
	DoctorReturnCode *int   `json:"doctor_return_code" yaml:"doctor_return_code"`
	Verified         *bool  `json:"verified" yaml:"verified"`
	ConfigDeployed   *bool  `json:"config_deployed" yaml:"config_deployed"`
	KlipperAction    string `json:"klipper_action" yaml:"klipper_action"`
	Error            string `json:"error" yaml:"error"`
}

func (result KlippeeVariableSetResult) Succeeded() bool {
	return result.OK != nil && *result.OK && result.Verified != nil && *result.Verified
}

func KlippeeVariableValueSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func ParseKlippeeVariableSet(raw []byte) (KlippeeVariableSetResult, error) {
	var result KlippeeVariableSetResult
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > 32*1024 || trimmed[0] != '{' {
		return result, fmt.Errorf("Klippee variable-set result must be one bounded JSON object")
	}
	if err := rejectDuplicateJSONFields(trimmed); err != nil {
		return result, err
	}
	shape := map[string]map[string]struct{}{
		"schema": nil, "ok": nil, "account": nil, "cli_path": nil,
		"name": nil, "value_sha256": nil, "set_return_code": nil,
		"doctor_return_code": nil, "verified": nil, "config_deployed": nil,
		"klipper_action": nil, "error": nil,
	}
	if err := rejectCaseAliasedJSONFields(trimmed, "Klippee variable-set result", shape); err != nil {
		return result, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return result, fmt.Errorf("Klippee variable-set result must be one JSON object")
	}
	for name := range shape {
		if _, present := fields[name]; !present {
			return result, fmt.Errorf("Klippee variable-set result omitted required field %q", name)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode Klippee variable-set result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return result, fmt.Errorf("Klippee variable-set returned trailing output")
	}
	if result.Schema != KlippeeVariableSetSchema || result.OK == nil ||
		result.Verified == nil || result.ConfigDeployed == nil ||
		result.Account != BoxClockGuardAccount || result.CLIPath != KlippeeVariableCLIPath ||
		!klippeeVariableNamePattern.MatchString(result.Name) ||
		!boxClockGuardDigestPattern.MatchString(result.ValueSHA256) ||
		*result.ConfigDeployed || result.KlipperAction != "none" {
		return result, fmt.Errorf("Klippee variable-set returned an invalid fixed-operation proof")
	}
	if *result.OK {
		if !*result.Verified || result.SetReturnCode != 0 || result.DoctorReturnCode == nil ||
			*result.DoctorReturnCode != 0 || strings.TrimSpace(result.Error) != "" {
			return result, fmt.Errorf("successful Klippee variable-set returned inconsistent proof")
		}
	} else if *result.Verified || strings.TrimSpace(result.Error) == "" {
		return result, fmt.Errorf("failed Klippee variable-set returned inconsistent proof")
	}
	return result, nil
}
