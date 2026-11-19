package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const (
	KlippeeAgentRefreshSchema = "qidi-plus4-klippee-agent-refresh/v1"
	KlippeeAgentService       = "klippee-moonraker.service"
	KlippeeAgentRefreshAction = "REFRESH-KLIPPEE-AGENT"
)

type KlippeeAgentRefreshResult struct {
	Schema                    string `json:"schema" yaml:"schema"`
	OK                        bool   `json:"ok" yaml:"ok"`
	Service                   string `json:"service" yaml:"service"`
	Scope                     string `json:"scope" yaml:"scope"`
	Account                   string `json:"account" yaml:"account"`
	ObservedState             string `json:"observed_state" yaml:"observed_state"`
	RestartExit               *int   `json:"restart_exit" yaml:"restart_exit"`
	StateExit                 *int   `json:"state_exit" yaml:"state_exit"`
	KlipperActionsPerformed   *bool  `json:"klipper_actions_performed" yaml:"klipper_actions_performed"`
	ConfigDeploymentPerformed *bool  `json:"config_deployment_performed" yaml:"config_deployment_performed"`
	Error                     string `json:"error,omitempty" yaml:"error,omitempty"`
}

func ParseKlippeeAgentRefresh(raw []byte) (KlippeeAgentRefreshResult, error) {
	var result KlippeeAgentRefreshResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode Klippee agent refresh result: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return result, fmt.Errorf("Klippee agent refresh returned more than one JSON document")
		}
		return result, fmt.Errorf("decode trailing Klippee agent refresh output: %w", err)
	}
	if result.Schema != KlippeeAgentRefreshSchema {
		return result, fmt.Errorf(
			"Klippee agent refresh returned schema %q; expected %q",
			result.Schema, KlippeeAgentRefreshSchema,
		)
	}
	if result.Service != KlippeeAgentService || result.Scope != "user" {
		return result, fmt.Errorf(
			"Klippee agent refresh targeted unexpected service %q scope %q",
			result.Service, result.Scope,
		)
	}
	if result.Account == "" {
		return result, fmt.Errorf("Klippee agent refresh returned no user account")
	}
	if result.RestartExit == nil || result.StateExit == nil {
		return result, fmt.Errorf("Klippee agent refresh omitted required exit-code proof")
	}
	if result.KlipperActionsPerformed == nil || *result.KlipperActionsPerformed {
		return result, fmt.Errorf("Klippee agent refresh did not prove zero Klipper actions")
	}
	if result.ConfigDeploymentPerformed == nil || *result.ConfigDeploymentPerformed {
		return result, fmt.Errorf("Klippee agent refresh did not prove zero config deployments")
	}
	if result.OK && (result.ObservedState != "active" || *result.RestartExit != 0 || *result.StateExit != 0) {
		return result, fmt.Errorf("Klippee agent refresh did not prove the managed service active")
	}
	return result, nil
}
