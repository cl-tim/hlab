package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/klippee"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

const (
	klippeeAgentName             = "klippee"
	klippeeOperationStatusMethod = "klippee_operation_status"
	klippeeOperationPollInterval = 250 * time.Millisecond
	defaultKlippeeWaitTimeout    = 4 * time.Minute
)

func newKlippeeCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:   "klippee",
		Short: "Manage printer configuration with Klippee",
		Long:  "Run Klippee commands in the local workspace or on the selected printer.",
	}
	command.AddCommand(
		newKlippeePathCmd(a),
		newKlippeeTargetCmd(a),
		newKlippeeCheckCmd(a),
		newKlippeeApplyCmd(a),
		newKlippeeCaptureCmd(a),
		newKlippeeVariableListCmd(a),
		newKlippeeVariableSetCmd(a),
		newKlippeeVariableUnsetCmd(a),
		newKlippeeVariableDoctorCmd(a),
		newKlippeePrinterVariableSetCmd(a),
		newKlippeePrinterCheckCmd(a),
		newKlippeePrinterDiffCmd(a),
		newKlippeeAgentEventsCmd(a),
		newKlippeePrinterCaptureCmd(a),
		newKlippeeDeployCmd(a),
		newKlippeeDiagnosticsCmd(a),
		newKlippeeBoxResetReasonCmd(a),
		newKlippeeBoxGuardStatusCmd(a),
		newKlippeeBoxRuntimeStatusCmd(a),
		newKlippeeBoxAutoInsertCmd(a, false),
		newKlippeeBoxAutoInsertCmd(a, true),
		newKlippeeHardenBoxMotionTemplateCmd(a),
		newKlippeeRestoreBoxMotionTemplateCmd(a),
		newKlippeeInstallBoxClockGuardCmd(a),
		newKlippeeOperationStatusCmd(a),
		newKlippeeUpgradeAgentCmd(a),
		newKlippeeRefreshAgentCmd(a),
		newKlippeeServiceRestartCmd(a),
		newKlippeeFirmwareRestartCmd(a),
		newKlippeeRestartCmd(a),
	)
	return command
}

func newKlippeePathCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "path PRINTER",
		Short: "Print a printer's local configuration workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			workspace, err := klippee.ResolveWorkspace(printer.Klippee.Workspace)
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			_, err = fmt.Fprintln(a.out, workspace)
			return err
		},
	}
}

func newKlippeeTargetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "target PRINTER",
		Short: "Verify the Klippee service, target, role, and workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runKlippee(cmd, args[0], "target")
		},
	}
}

func newKlippeeCheckCmd(a *app) *cobra.Command {
	var showDiff bool
	command := &cobra.Command{
		Use:   "check PRINTER",
		Short: "Compare the local workspace with desired configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			klippeeArgs := []string{"check"}
			if showDiff {
				klippeeArgs = append(klippeeArgs, "--diff")
			}
			return a.runKlippee(cmd, args[0], klippeeArgs...)
		},
	}
	command.Flags().BoolVar(&showDiff, "diff", false, "show unified configuration diffs")
	return command
}

func newKlippeeApplyCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:   "apply PRINTER",
		Short: "Apply desired configuration to the local desktop workspace",
		Long: "Make the local desktop workspace match Klippee's desired configuration. " +
			"Klippee shows changed paths, prompts before replacement, and creates a backup.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			klippeeArgs := []string{"apply"}
			if a.yes {
				klippeeArgs = append(klippeeArgs, "--yes")
			}
			return a.runKlippee(cmd, args[0], klippeeArgs...)
		},
	}
	return command
}

func newKlippeeCaptureCmd(a *app) *cobra.Command {
	var note string
	command := &cobra.Command{
		Use:   "capture PRINTER",
		Short: "Make desktop workspace edits the desired configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			klippeeArgs := []string{"capture"}
			if note != "" {
				klippeeArgs = append(klippeeArgs, "--note", note)
			}
			return a.runKlippee(cmd, args[0], klippeeArgs...)
		},
	}
	command.Flags().StringVar(&note, "note", "", "optional desired-configuration history note")
	return command
}

func newKlippeeVariableListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "variable-list PRINTER",
		Short: "List the selected desktop workspace's local Klippee variables",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runKlippee(cmd, args[0], "variables", "list")
		},
	}
}

func newKlippeeVariableSetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "variable-set PRINTER NAME VALUE",
		Short: "Set a local Klippee rendering variable",
		Long:  "Set a rendering variable in the local workspace's klippee_variables.cfg.",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runKlippee(cmd, args[0], "variables", "set", args[1], args[2])
		},
	}
}

func newKlippeeVariableUnsetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "variable-unset PRINTER NAME",
		Short: "Remove a local Klippee rendering variable",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runKlippee(cmd, args[0], "variables", "unset", args[1])
		},
	}
}

func newKlippeeVariableDoctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "variable-doctor PRINTER",
		Short: "Validate the selected desktop workspace's Klippee variables",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runKlippee(cmd, args[0], "variables", "doctor")
		},
	}
}

func newKlippeePrinterCheckCmd(a *app) *cobra.Command {
	var legacy bool
	command := &cobra.Command{
		Use:   "printer-check PRINTER",
		Short: "Check the printer's configuration drift",
		Long: "Check the printer's configuration through its Klippee agent.\n" +
			"Use --legacy for agents that expose the older klippee_status method.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			method := "klippee_check"
			acceptedCommands := []string{"check"}
			if legacy {
				method = "klippee_status"
				// Original agents acknowledged "status". Current agents retain
				// the old RPC name as an alias but acknowledge canonical "check".
				acceptedCommands = []string{"status", "check"}
			}
			printer, client, err := a.physicalKlippeeClient(args[0])
			if err != nil {
				return err
			}
			_, err = a.requestPrinterKlippee(
				cmd.Context(), printer, client, method, "check", acceptedCommands,
			)
			return err
		},
	}
	command.Flags().BoolVar(
		&legacy,
		"legacy",
		false,
		"call the legacy klippee_status agent method instead of klippee_check",
	)
	return command
}

func newKlippeePrinterCaptureCmd(a *app) *cobra.Command {
	var waitTimeout time.Duration
	command := &cobra.Command{
		Use:   "printer-capture PRINTER",
		Short: "Capture the printer's configuration as desired state",
		Long: "Capture the printer's current configuration as its desired configuration\n" +
			"and wait for completion. Requires an idle printer and Klippee agent.\n" +
			"Review the printer's configuration before capturing it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("physical Klippee configuration capture"); err != nil {
				return err
			}
			if waitTimeout <= 0 {
				return fmt.Errorf("--wait-timeout must be greater than zero")
			}
			printer, client, err := a.physicalKlippeeClient(args[0])
			if err != nil {
				return err
			}
			if err := client.EnsureIdle(cmd.Context()); err != nil {
				return fmt.Errorf("%s: physical Klippee capture blocked: %w", printer.Name, err)
			}
			status, err := requestKlippeeOperationStatus(cmd.Context(), client, "")
			if err != nil {
				return fmt.Errorf("%s: printer-capture requires current Klippee operation status: %w", printer.Name, err)
			}
			if err := requireKlippeeOperationQuiescent(status); err != nil {
				return fmt.Errorf("%s: physical Klippee capture blocked: %w", printer.Name, err)
			}
			response, err := a.requestPrinterKlippee(
				cmd.Context(), printer, client, "klippee_capture", "capture", []string{"capture"},
			)
			if err != nil {
				return err
			}
			if strings.TrimSpace(response.OperationID) == "" {
				return fmt.Errorf("%s: Klippee capture was accepted without an operation ID", printer.Name)
			}
			operation, err := waitForKlippeeOperation(
				cmd.Context(), client, response.OperationID, waitTimeout,
			)
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			if operation.Status != "succeeded" || operation.ReturnCode == nil || *operation.ReturnCode != 0 {
				return fmt.Errorf(
					"%s: Klippee capture %s completed with status %q, return code %s: %s",
					printer.Name, operation.ID, operation.Status,
					returnCodeLabel(operation.ReturnCode), operationSummary(operation),
				)
			}
			_, err = fmt.Fprintf(
				a.out,
				"%s: Klippee capture %s succeeded (return code 0): %s\n",
				printer.Name, operation.ID, operationSummary(operation),
			)
			return err
		},
	}
	command.Flags().DurationVar(
		&waitTimeout,
		"wait-timeout",
		defaultKlippeeWaitTimeout,
		"maximum time to wait for the capture operation",
	)
	return command
}

func newKlippeeDeployCmd(a *app) *cobra.Command {
	var legacy bool
	var sshForce bool
	var wait bool
	var waitTimeout time.Duration
	command := &cobra.Command{
		Use:   "deploy PRINTER",
		Short: "Apply desired configuration to the printer",
		Long: "Apply desired configuration through the printer's Klippee agent.\n" +
			"Requires an idle printer. Use --wait to wait for completion and report failures.\n" +
			"Use --legacy for agents that expose the older klippee_pull method.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if sshForce && (legacy || wait) {
				return fmt.Errorf("--ssh-force cannot be combined with --legacy or --wait")
			}
			if sshForce {
				if err := a.requireApproval("forced physical Klippee apply"); err != nil {
					return err
				}
			}
			if legacy && wait {
				return fmt.Errorf("--wait is unavailable with --legacy because legacy agents do not expose operation status")
			}
			if cmd.Flags().Changed("wait-timeout") && !wait {
				return fmt.Errorf("--wait-timeout requires --wait")
			}
			if wait && waitTimeout <= 0 {
				return fmt.Errorf("--wait-timeout must be greater than zero")
			}

			method := "klippee_apply"
			acceptedCommands := []string{"apply"}
			if legacy {
				method = "klippee_pull"
				// Original agents acknowledged "pull". Current agents retain
				// the old RPC name as an alias but acknowledge canonical "apply".
				acceptedCommands = []string{"pull", "apply"}
			}

			printer, client, err := a.physicalKlippeeClient(args[0])
			if err != nil {
				return err
			}
			if err := client.EnsureIdle(cmd.Context()); err != nil {
				return fmt.Errorf("%s: physical Klippee deploy blocked: %w", printer.Name, err)
			}
			if sshForce {
				raw, err := a.migrationRunner().KlippeeForceApplySSHOutput(
					cmd.Context(), printer, a.timeout(),
				)
				if err != nil {
					return fmt.Errorf("%s: %w", printer.Name, err)
				}
				_, err = fmt.Fprintln(a.out, strings.TrimSpace(string(raw)))
				return err
			}
			if wait {
				status, err := requestKlippeeOperationStatus(cmd.Context(), client, "")
				if err != nil {
					return fmt.Errorf(
						"%s: --wait requires a current Klippee agent with operation status: %w",
						printer.Name,
						err,
					)
				}
				if status.Running {
					operation, statusErr := operationFromStatus(status)
					if statusErr != nil {
						return fmt.Errorf("%s: invalid Klippee operation status: %w", printer.Name, statusErr)
					}
					return fmt.Errorf(
						"%s: Klippee operation %s is already running; refusing another deploy",
						printer.Name,
						operation.ID,
					)
				}
			}

			response, err := a.requestPrinterKlippee(
				cmd.Context(), printer, client, method, "apply", acceptedCommands,
			)
			if err != nil || !wait {
				return err
			}
			if strings.TrimSpace(response.OperationID) == "" {
				return fmt.Errorf(
					"%s: Klippee apply was accepted without an operation ID; completion cannot be verified",
					printer.Name,
				)
			}
			operation, err := waitForKlippeeOperation(
				cmd.Context(), client, response.OperationID, waitTimeout,
			)
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			if operation.Status != "succeeded" || operation.ReturnCode == nil || *operation.ReturnCode != 0 {
				return fmt.Errorf(
					"%s: Klippee operation %s completed with status %q, return code %s: %s",
					printer.Name,
					operation.ID,
					operation.Status,
					returnCodeLabel(operation.ReturnCode),
					operationSummary(operation),
				)
			}
			_, err = fmt.Fprintf(
				a.out,
				"%s: Klippee operation %s succeeded (return code 0): %s\n",
				printer.Name,
				operation.ID,
				operationSummary(operation),
			)
			return err
		},
	}
	command.Flags().BoolVar(
		&legacy,
		"legacy",
		false,
		"call the legacy klippee_pull agent method instead of klippee_apply",
	)
	command.Flags().BoolVar(
		&sshForce,
		"ssh-force",
		false,
		"run fixed `klippee pull --force` after independently reviewing the physical diff",
	)
	command.Flags().BoolVar(
		&wait,
		"wait",
		false,
		"wait for the current agent to report this apply completed successfully",
	)
	command.Flags().DurationVar(
		&waitTimeout,
		"wait-timeout",
		defaultKlippeeWaitTimeout,
		"maximum time to wait for --wait operation completion",
	)
	return command
}

func newKlippeeOperationStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "operation-status PRINTER [OPERATION_ID]",
		Short: "Show Klippee agent operation status",
		Long: "Show the Klippee agent's current or most recent operation.\n" +
			"Supply an operation ID to check that operation.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			operationID := ""
			if len(args) == 2 {
				operationID = strings.TrimSpace(args[1])
				if operationID == "" || len(operationID) > 128 {
					return fmt.Errorf("operation ID must be a non-empty string of at most 128 characters")
				}
			}
			printer, client, err := a.physicalKlippeeClient(args[0])
			if err != nil {
				return err
			}
			status, err := requestKlippeeOperationStatus(cmd.Context(), client, operationID)
			if err != nil {
				return fmt.Errorf("%s: Klippee operation status request failed: %w", printer.Name, err)
			}
			if !status.Found {
				if operationID != "" {
					return fmt.Errorf(
						"%s: Klippee operation %s was not found; the agent retains only its current and last operation",
						printer.Name,
						operationID,
					)
				}
				_, err = fmt.Fprintf(
					a.out,
					"%s: Klippee agent has no current or retained operation.\n",
					printer.Name,
				)
				return err
			}
			operation, err := operationFromStatus(status)
			if err != nil {
				return fmt.Errorf("%s: invalid Klippee operation status: %w", printer.Name, err)
			}
			if operationID != "" && operation.ID != operationID {
				return fmt.Errorf(
					"%s: Klippee operation status returned %q, expected %q",
					printer.Name,
					operation.ID,
					operationID,
				)
			}
			return writeKlippeeOperation(a.out, printer.Name, status.Running, operation)
		},
	}
}

func newKlippeeRestartCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:   "restart PRINTER",
		Short: "Reload Klipper's configuration",
		Long: "Send RESTART through Moonraker to reload Klipper's configuration.\n" +
			"Requires an idle printer. Confirm deployment completed before restarting.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("Klipper restart"); err != nil {
				return err
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			if err := client.EnsureIdle(cmd.Context()); err != nil {
				return fmt.Errorf("%s: Klipper restart blocked: %w", printer.Name, err)
			}
			if err := client.RunGcode(cmd.Context(), "RESTART"); err != nil {
				return fmt.Errorf("%s: Klipper restart request failed: %w", printer.Name, err)
			}
			_, err = fmt.Fprintf(a.out, "%s: Klipper restart requested through Moonraker.\n", printer.Name)
			return err
		},
	}
	return command
}

func newKlippeeFirmwareRestartCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:   "firmware-restart PRINTER",
		Short: "Restart Klipper and its MCUs",
		Long: "Send FIRMWARE_RESTART through Moonraker to reload configuration and reset the MCUs.\n" +
			"Requires an idle printer. Save any MCU reset evidence before restarting.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("Klipper firmware restart"); err != nil {
				return err
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			if err := client.EnsureIdle(cmd.Context()); err != nil {
				return fmt.Errorf("%s: Klipper firmware restart blocked: %w", printer.Name, err)
			}
			if err := client.RunGcode(cmd.Context(), "FIRMWARE_RESTART"); err != nil {
				return fmt.Errorf(
					"%s: Klipper firmware restart request failed; outcome is ambiguous and the command must not be retried; verify with read-only `hlab printer status %s` and `hlab printer audit %s`: %w",
					printer.Name,
					printer.Name,
					printer.Name,
					err,
				)
			}
			_, err = fmt.Fprintf(
				a.out,
				"%s: Klipper firmware restart requested through Moonraker.\n",
				printer.Name,
			)
			return err
		},
	}
	return command
}

func (a *app) physicalKlippeeClient(
	printerName string,
) (inventory.Printer, *moonraker.Client, error) {
	printer, err := a.onePrinter(printerName)
	if err != nil {
		return inventory.Printer{}, nil, err
	}
	client, err := moonraker.New(printer, a.timeout())
	if err != nil {
		return inventory.Printer{}, nil, fmt.Errorf("%s: %w", printer.Name, err)
	}
	return printer, client, nil
}

func (a *app) requestPrinterKlippee(
	ctx context.Context,
	printer inventory.Printer,
	client *moonraker.Client,
	method string,
	action string,
	acceptedCommands []string,
) (moonraker.ExtensionResponse, error) {
	response, err := a.requestPrinterKlippeeAccepted(
		ctx, printer, client, method, action, acceptedCommands,
	)
	if err != nil {
		return moonraker.ExtensionResponse{}, err
	}
	operationLabel := ""
	if response.OperationID != "" {
		operationLabel = fmt.Sprintf(" as operation %s", response.OperationID)
	}
	if _, err := fmt.Fprintf(
		a.out,
		"%s: physical Klippee %s accepted%s; it is running asynchronously on the printer (acceptance does not confirm completion).\n",
		printer.Name,
		action,
		operationLabel,
	); err != nil {
		return moonraker.ExtensionResponse{}, err
	}
	return response, nil
}

func (a *app) requestPrinterKlippeeAccepted(
	ctx context.Context,
	printer inventory.Printer,
	client *moonraker.Client,
	method string,
	action string,
	acceptedCommands []string,
) (moonraker.ExtensionResponse, error) {
	var response moonraker.ExtensionResponse
	if err := client.RequestExtension(
		ctx, klippeeAgentName, method, nil, &response,
	); err != nil {
		return moonraker.ExtensionResponse{}, fmt.Errorf(
			"%s: physical Klippee %s request failed: %w", printer.Name, action, err,
		)
	}
	if !response.Accepted {
		return moonraker.ExtensionResponse{}, fmt.Errorf(
			"%s: physical Klippee agent did not accept %s", printer.Name, action,
		)
	}
	if !containsString(acceptedCommands, response.Command) {
		return moonraker.ExtensionResponse{}, fmt.Errorf(
			"%s: physical Klippee agent acknowledged command %q, expected one of %q",
			printer.Name, response.Command, acceptedCommands,
		)
	}
	return response, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func requestKlippeeOperationStatus(
	ctx context.Context,
	client *moonraker.Client,
	operationID string,
) (moonraker.ExtensionOperationStatus, error) {
	var arguments any
	if operationID != "" {
		arguments = map[string]string{"operation_id": operationID}
	}
	var status moonraker.ExtensionOperationStatus
	if err := client.RequestExtension(
		ctx, klippeeAgentName, klippeeOperationStatusMethod, arguments, &status,
	); err != nil {
		return moonraker.ExtensionOperationStatus{}, err
	}
	return status, nil
}

func operationFromStatus(
	status moonraker.ExtensionOperationStatus,
) (*moonraker.ExtensionOperation, error) {
	if !status.Found {
		return nil, fmt.Errorf("response did not find an operation")
	}
	var operation *moonraker.ExtensionOperation
	if status.Running {
		if status.Current == nil {
			return nil, fmt.Errorf("running response has no current operation")
		}
		operation = status.Current
	} else {
		if status.Current != nil {
			return nil, fmt.Errorf("completed response unexpectedly has a current operation")
		}
		if status.LastResult == nil {
			return nil, fmt.Errorf("found response has no retained result")
		}
		operation = status.LastResult
	}
	if operation.ID == "" {
		return nil, fmt.Errorf("operation has no ID")
	}
	if status.OperationID != "" && status.OperationID != operation.ID {
		return nil, fmt.Errorf(
			"response operation ID %q does not match record %q",
			status.OperationID,
			operation.ID,
		)
	}
	return operation, nil
}

// requireKlippeeOperationQuiescent validates a complete operation-status
// response before any action that could interrupt the agent. Unsupported,
// malformed, and running responses all fail closed.
func requireKlippeeOperationQuiescent(
	status moonraker.ExtensionOperationStatus,
) error {
	if status.Running {
		operation, err := operationFromStatus(status)
		if err != nil {
			return fmt.Errorf("running response is invalid: %w", err)
		}
		return fmt.Errorf("Klippee operation %s is still running", operation.ID)
	}
	if !status.Found {
		if status.OperationID != "" || status.Current != nil || status.LastResult != nil {
			return fmt.Errorf("not-found response contains operation records")
		}
		return nil
	}
	operation, err := operationFromStatus(status)
	if err != nil {
		return err
	}
	if !containsString(
		[]string{"succeeded", "failed", "timed_out"}, operation.Status,
	) {
		return fmt.Errorf(
			"retained operation %s has unknown or non-terminal status %q",
			operation.ID,
			operation.Status,
		)
	}
	return nil
}

func waitForKlippeeOperation(
	ctx context.Context,
	client *moonraker.Client,
	operationID string,
	timeout time.Duration,
) (*moonraker.ExtensionOperation, error) {
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		status, err := requestKlippeeOperationStatus(waitContext, client, operationID)
		if err != nil {
			if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf(
					"timed out after %s waiting for Klippee operation %s; query operation-status before retrying",
					timeout,
					operationID,
				)
			}
			if waitContext.Err() != nil {
				return nil, waitContext.Err()
			}
			var networkError net.Error
			if !errors.As(err, &networkError) || !networkError.Timeout() {
				return nil, fmt.Errorf("query Klippee operation %s: %w", operationID, err)
			}
			// A busy agent can temporarily outlive the Moonraker request timeout.
			// Retry only this read-only status query; never resubmit the operation.
		} else {
			if status.OperationID != operationID {
				return nil, fmt.Errorf(
					"Klippee status identified operation %q while waiting for exact operation %q; refusing to infer completion",
					status.OperationID,
					operationID,
				)
			}
			if !status.Found {
				if status.Current != nil || status.LastResult != nil {
					return nil, fmt.Errorf(
						"Klippee operation %s was reported not found with conflicting operation records",
						operationID,
					)
				}
				return nil, fmt.Errorf(
					"Klippee operation %s is no longer retained by the agent; do not retry it without independent verification",
					operationID,
				)
			}
			if status.Running && status.LastResult != nil {
				return nil, fmt.Errorf(
					"Klippee operation %s has ambiguous running and completed records",
					operationID,
				)
			}
			operation, err := operationFromStatus(status)
			if err != nil {
				return nil, fmt.Errorf("invalid status for Klippee operation %s: %w", operationID, err)
			}
			if operation.ID != operationID {
				return nil, fmt.Errorf(
					"Klippee status returned operation %s while waiting for %s; refusing to infer completion",
					operation.ID,
					operationID,
				)
			}
			if !status.Running {
				return operation, nil
			}
		}

		timer := time.NewTimer(klippeeOperationPollInterval)
		select {
		case <-waitContext.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if errors.Is(waitContext.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf(
					"timed out after %s waiting for Klippee operation %s; query operation-status before retrying",
					timeout,
					operationID,
				)
			}
			return nil, waitContext.Err()
		case <-timer.C:
		}
	}
}

func writeKlippeeOperation(
	writer io.Writer,
	printerName string,
	running bool,
	operation *moonraker.ExtensionOperation,
) error {
	if running {
		_, err := fmt.Fprintf(
			writer,
			"%s: Klippee operation %s is running (%s, started %s).\n",
			printerName,
			operation.ID,
			operation.Command,
			operation.StartedAt,
		)
		return err
	}
	_, err := fmt.Fprintf(
		writer,
		"%s: Klippee operation %s completed with status %s (return code %s): %s\n",
		printerName,
		operation.ID,
		operation.Status,
		returnCodeLabel(operation.ReturnCode),
		operationSummary(operation),
	)
	return err
}

func returnCodeLabel(code *int) string {
	if code == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%d", *code)
}

func operationSummary(operation *moonraker.ExtensionOperation) string {
	summary := strings.Join(strings.Fields(operation.Summary), " ")
	if summary == "" {
		return "no summary reported"
	}
	return summary
}

func (a *app) klippeeRunner(printer inventory.Printer) klippee.Runner {
	return klippee.Runner{
		Executable: a.cfg.Defaults.Klippee.Executable,
		Workspace:  printer.Klippee.Workspace,
		In:         a.in,
		Out:        a.out,
		ErrOut:     a.errOut,
	}
}

func (a *app) runKlippee(cmd *cobra.Command, printerName string, args ...string) error {
	printer, err := a.onePrinter(printerName)
	if err != nil {
		return err
	}
	if err := a.klippeeRunner(printer).Run(cmd.Context(), args...); err != nil {
		var commandError *klippee.CommandError
		if errors.As(err, &commandError) {
			// Klippee already streamed its own diagnostics. Preserve its exit
			// status without printing a second generic error in cmd/plab.
			return &ExitError{Code: commandError.Code}
		}
		return fmt.Errorf("%s: %w", printer.Name, err)
	}
	return nil
}
