package migrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

const KlipperServiceRestartSchema = "printerlab-klipper-service-restart/v1"

const klipperServiceRestartCommand = `set -eu; service=klipper.service; before=$(systemctl show "$service" -p MainPID --value); sudo -n systemctl restart "$service"; state=$(systemctl is-active "$service"); after=$(systemctl show "$service" -p MainPID --value); printf '{"schema":"printerlab-klipper-service-restart/v1","service":"%s","state":"%s","previous_pid":%s,"current_pid":%s}\n' "$service" "$state" "$before" "$after"`

type KlipperServiceRestartResult struct {
	Schema      string `json:"schema" yaml:"schema"`
	Service     string `json:"service" yaml:"service"`
	State       string `json:"state" yaml:"state"`
	PreviousPID int    `json:"previous_pid" yaml:"previous_pid"`
	CurrentPID  int    `json:"current_pid" yaml:"current_pid"`
}

func (result KlipperServiceRestartResult) Validate() error {
	if result.Schema != KlipperServiceRestartSchema {
		return fmt.Errorf("service restart returned schema %q", result.Schema)
	}
	if result.Service != KlipperService {
		return fmt.Errorf("service restart targeted %q", result.Service)
	}
	if result.State != "active" {
		return fmt.Errorf("service restart observed state %q", result.State)
	}
	if result.PreviousPID <= 0 || result.CurrentPID <= 0 {
		return fmt.Errorf("service restart returned invalid PID proof")
	}
	if result.PreviousPID == result.CurrentPID {
		return fmt.Errorf("Klipper process PID did not change")
	}
	return nil
}

func ParseKlipperServiceRestart(raw []byte) (KlipperServiceRestartResult, error) {
	var result KlipperServiceRestartResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode service restart proof: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return result, fmt.Errorf("service restart returned more than one JSON document")
		}
		return result, fmt.Errorf("decode trailing service restart output: %w", err)
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func (r Runner) KlipperServiceRestartSSHOutput(
	ctx context.Context,
	printer inventory.Printer,
	connectTimeout time.Duration,
) ([]byte, error) {
	sshExecutable, args, err := r.fixedSSHCommand(printer, connectTimeout)
	if err != nil {
		return nil, err
	}
	args = append(args, klipperServiceRestartCommand)
	command := exec.CommandContext(ctx, sshExecutable, args...)
	command.Env = bootstrapOnlyEnvironment(nil, r.BootstrapPasswordEnvironments, "")
	stdout := tailBuffer{limit: maxKlippeeDiagnosticsOutputBytes}
	stderr := tailBuffer{limit: maxCapturedDiagnosticBytes}
	command.Stdout = &stdout
	command.Stderr = &stderr
	if r.ErrOut != nil {
		command.Stderr = io.MultiWriter(r.ErrOut, &stderr)
	}
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return stdout.data, ctx.Err()
		}
		if exitError, ok := err.(*exec.ExitError); ok {
			diagnostic := SafeDiagnosticSummary(stderr.data)
			if diagnostic != "" {
				return stdout.data, fmt.Errorf(
					"SSH service restart exited %d: %s",
					exitError.ExitCode(), diagnostic,
				)
			}
			return stdout.data, fmt.Errorf(
				"SSH service restart exited %d", exitError.ExitCode(),
			)
		}
		return stdout.data, fmt.Errorf("run SSH service restart: %w", err)
	}
	return bytes.Clone(stdout.data), nil
}
