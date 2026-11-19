package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

const (
	defaultKlippyReadyTimeout = 45 * time.Second
	maximumKlippyReadyTimeout = 5 * time.Minute
	klippyReadyPollInterval   = 500 * time.Millisecond
	plrRollbackGuidance       = "hlab printer firmware advanced rollback-plr-hardening PRINTER " +
		"--backup-manifest BACKUP_MANIFEST -y"
)

type plrOperationReport struct {
	Operation         string            `json:"operation" yaml:"operation"`
	Printer           string            `json:"printer" yaml:"printer"`
	OK                bool              `json:"ok" yaml:"ok"`
	Status            string            `json:"status" yaml:"status"`
	KlipperState      string            `json:"klipperState,omitempty" yaml:"klipperState,omitempty"`
	Phase             string            `json:"phase,omitempty" yaml:"phase,omitempty"`
	Account           string            `json:"account,omitempty" yaml:"account,omitempty"`
	Paths             *migrate.PLRPaths `json:"paths,omitempty" yaml:"paths,omitempty"`
	TransactionState  string            `json:"transactionState,omitempty" yaml:"transactionState,omitempty"`
	BackupManifest    string            `json:"backupManifest,omitempty" yaml:"backupManifest,omitempty"`
	SafetyManifest    string            `json:"safetyManifest,omitempty" yaml:"safetyManifest,omitempty"`
	RollbackNext      string            `json:"rollbackNext,omitempty" yaml:"rollbackNext,omitempty"`
	FirmwareResult    string            `json:"firmwareResultSchema" yaml:"firmwareResultSchema"`
	Diagnostic        string            `json:"diagnostic,omitempty" yaml:"diagnostic,omitempty"`
	NoImplicitRestart bool              `json:"noImplicitRestart" yaml:"noImplicitRestart"`
	ManifestFromTool  bool              `json:"manifestFromTool" yaml:"manifestFromTool"`
	SafetyFromTool    bool              `json:"safetyManifestFromTool" yaml:"safetyManifestFromTool"`
	MoonrakerReady    bool              `json:"moonrakerReady" yaml:"moonrakerReady"`
}

func newMainlineDeployPLRCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:   "deploy-plr-hardening PRINTER",
		Short: "Install power-loss recovery hardening and stop Klipper",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.guardedPLRPrinter(args[0], "PLR hardening deployment")
			if err != nil {
				return err
			}
			if err := a.ensureIdle(cmd.Context(), printer, false); err != nil {
				return err
			}
			stageArgs := append(
				[]string{"deploy-plr-hardening"}, migrate.ConnectionArgs(printer)...,
			)
			stageArgs = append(stageArgs, "--confirm", "DEPLOY-PLR-HARDENING")
			return a.runPLROperation(
				cmd, printer, "deploy", migrate.PLRDeploymentSchema, "", stageArgs...,
			)
		},
	}
	return command
}

func newMainlineStartKlipperCmd(a *app) *cobra.Command {
	var manifest string
	var readyTimeout time.Duration
	command := &cobra.Command{
		Use:   "start-klipper PRINTER",
		Short: "Start Klipper after reviewing power-loss recovery setup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.guardedPLRPrinter(args[0], "Klipper start")
			if err != nil {
				return err
			}
			if manifest == "" {
				return fmt.Errorf("--backup-manifest is required")
			}
			if err := migrate.ValidatePLRBackupManifest(
				manifest, plrPrinterAccount(printer),
			); err != nil {
				return err
			}
			if readyTimeout <= 0 || readyTimeout > maximumKlippyReadyTimeout {
				return fmt.Errorf(
					"--ready-timeout must be greater than zero and at most %s",
					maximumKlippyReadyTimeout,
				)
			}
			stageArgs := append([]string{"start-klipper"}, migrate.ConnectionArgs(printer)...)
			stageArgs = append(stageArgs,
				"--confirm", "START-KLIPPER",
				"--rollback-manifest", manifest,
			)
			return a.runPLRStart(
				cmd, printer, manifest, readyTimeout, stageArgs...,
			)
		},
	}
	command.Flags().StringVar(
		&manifest, "backup-manifest", "",
		"exact backup_manifest returned by deploy-plr-hardening",
	)
	command.Flags().DurationVar(
		&readyTimeout, "ready-timeout", defaultKlippyReadyTimeout,
		"maximum wait for Moonraker to report Klippy ready",
	)
	return command
}

func newMainlineRollbackPLRCmd(a *app) *cobra.Command {
	var manifest string
	command := &cobra.Command{
		Use:   "rollback-plr-hardening PRINTER",
		Short: "Restore a power-loss recovery backup and stop Klipper",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.guardedPLRPrinter(args[0], "PLR hardening rollback")
			if err != nil {
				return err
			}
			if manifest == "" {
				return fmt.Errorf("--backup-manifest is required")
			}
			if err := migrate.ValidatePLRBackupManifest(
				manifest, plrPrinterAccount(printer),
			); err != nil {
				return err
			}
			if err := a.ensurePLRRollbackSafe(cmd.Context(), printer); err != nil {
				return err
			}
			stageArgs := append(
				[]string{"rollback-plr-hardening"}, migrate.ConnectionArgs(printer)...,
			)
			stageArgs = append(stageArgs,
				"--confirm", "ROLLBACK-PLR-HARDENING",
				"--backup-manifest", manifest,
			)
			return a.runPLROperation(
				cmd, printer, "rollback", migrate.PLRRollbackSchema, manifest, stageArgs...,
			)
		},
	}
	command.Flags().StringVar(
		&manifest, "backup-manifest", "",
		"exact backup_manifest returned by deploy-plr-hardening",
	)
	return command
}

func (a *app) guardedPLRPrinter(
	name, action string,
) (inventory.Printer, error) {
	printer, err := a.onePrinter(name)
	if err != nil {
		return inventory.Printer{}, err
	}
	if err := migrate.RequirePlus4(printer); err != nil {
		return inventory.Printer{}, err
	}
	if err := a.requireApproval(action); err != nil {
		return inventory.Printer{}, err
	}
	if _, err := migrate.CanonicalPLRPaths(plrPrinterAccount(printer)); err != nil {
		return inventory.Printer{}, fmt.Errorf("%s: %w", printer.Name, err)
	}
	return printer, nil
}

func plrPrinterAccount(printer inventory.Printer) string {
	if printer.PrinterUser != "" {
		return printer.PrinterUser
	}
	return printer.User
}

func (a *app) runPLROperation(
	cmd *cobra.Command,
	printer inventory.Printer,
	operation, schema, expectedManifest string,
	args ...string,
) error {
	result, runErr, err := a.readPLROperation(
		cmd.Context(), printer, operation, schema, expectedManifest, args...,
	)
	if err != nil {
		report := makeUnavailablePLRReport(
			operation, printer.Name, schema, expectedManifest, err,
		)
		if reportErr := writePLRReport(a, report); reportErr != nil {
			return errors.Join(err, reportErr)
		}
		return err
	}
	report := makePLRReport(operation, printer.Name, result)
	if operation == "rollback" && report.BackupManifest == "" {
		// An idempotent rollback may report unchanged without echoing the
		// handle. Surface the exact caller-supplied value; it was already passed
		// to and validated by the firmware tool, never discovered on-printer.
		report.BackupManifest = expectedManifest
		report.ManifestFromTool = false
	}
	ensurePLRFailureRecovery(&report, expectedManifest)
	if err := writePLRReport(a, report); err != nil {
		return err
	}
	return plrOperationError(result, runErr)
}

func (a *app) readPLROperation(
	ctx context.Context,
	printer inventory.Printer,
	operation, schema, expectedManifest string,
	args ...string,
) (migrate.PLROperationResult, error, error) {
	raw, runErr := a.migrationRunner().Output(ctx, args...)
	if len(bytes.TrimSpace(raw)) == 0 {
		if runErr != nil {
			return migrate.PLROperationResult{}, runErr, fmt.Errorf(
				"firmware operation returned no JSON result: %w", runErr,
			)
		}
		return migrate.PLROperationResult{}, nil, fmt.Errorf(
			"firmware operation returned no JSON result",
		)
	}
	result, parseErr := migrate.ParsePLROperation(raw, schema)
	if parseErr != nil {
		preview := migrate.SafeDiagnosticSummary(raw)
		if preview != "" {
			parseErr = fmt.Errorf(
				"%w (firmware stdout summary: %s)", parseErr, preview,
			)
		}
		if runErr != nil {
			return result, runErr, fmt.Errorf(
				"%w (result could not be validated: %v)", runErr, parseErr,
			)
		}
		return result, nil, parseErr
	}
	if operation == "deploy" || operation == "rollback" {
		if err := migrate.ValidatePLRTarget(result, plrPrinterAccount(printer)); err != nil {
			return result, runErr, err
		}
	}
	if err := migrate.ValidatePLRLifecycle(result); err != nil {
		return result, runErr, err
	}
	if err := requireReturnedManifest(operation, expectedManifest, result); err != nil {
		return result, runErr, err
	}
	return result, runErr, nil
}

func plrOperationError(result migrate.PLROperationResult, runErr error) error {
	diagnostic := result.DiagnosticSummary()
	if runErr != nil {
		if diagnostic != "" {
			return fmt.Errorf("%s: %w", diagnostic, runErr)
		}
		return runErr
	}
	if !result.Succeeded() {
		if diagnostic != "" {
			return fmt.Errorf("firmware operation refused: %s", diagnostic)
		}
		return fmt.Errorf("firmware operation reported failure")
	}
	return nil
}

func (a *app) runPLRStart(
	cmd *cobra.Command,
	printer inventory.Printer,
	manifest string,
	readyTimeout time.Duration,
	args ...string,
) error {
	result, runErr, err := a.readPLROperation(
		cmd.Context(), printer, "start", migrate.KlipperStartSchema, manifest, args...,
	)
	if err != nil {
		report := makeUnavailablePLRReport(
			"start", printer.Name, migrate.KlipperStartSchema, manifest, err,
		)
		if reportErr := writePLRReport(a, report); reportErr != nil {
			return errors.Join(err, reportErr)
		}
		return err
	}
	report := makePLRReport("start", printer.Name, result)
	ensurePLRFailureRecovery(&report, manifest)
	if operationErr := plrOperationError(result, runErr); operationErr != nil {
		if err := writePLRReport(a, report); err != nil {
			return err
		}
		return operationErr
	}

	client, clientErr := moonraker.New(printer, a.timeout())
	var ready moonraker.KlippyConnectionState
	if clientErr == nil {
		ready, clientErr = client.WaitForKlippyReady(
			cmd.Context(), readyTimeout, klippyReadyPollInterval,
		)
	}
	if clientErr != nil {
		report.OK = false
		report.Status = "systemd window passed; Klippy not ready"
		if ready.State != "" {
			report.KlipperState = ready.State
		}
		report.RollbackNext = plrRollbackGuidance
		report.Diagnostic = fmt.Sprintf(
			"Moonraker readiness failed: %v; no rollback was attempted", clientErr,
		)
		if err := writePLRReport(a, report); err != nil {
			return err
		}
		return fmt.Errorf(
			"%s: Klipper passed the bounded systemd stability window, but Moonraker did not prove "+
				"Klippy ready: %w; no rollback was attempted; use backup manifest %s",
			printer.Name, clientErr, manifest,
		)
	}
	report.Status = "Klippy ready"
	report.KlipperState = ready.State
	report.MoonrakerReady = true
	return writePLRReport(a, report)
}

func (a *app) ensurePLRRollbackSafe(
	ctx context.Context,
	printer inventory.Printer,
) error {
	client, err := moonraker.New(printer, a.timeout())
	if err != nil {
		return fmt.Errorf("%s: PLR rollback preflight failed: %w", printer.Name, err)
	}
	state, stateErr := client.PrintState(ctx)
	if stateErr == nil {
		switch state {
		case "standby", "complete", "cancelled", "error":
			return nil
		case "printing", "paused":
			return fmt.Errorf(
				"%s: PLR rollback blocked: print_stats.state is %q",
				printer.Name, state,
			)
		default:
			return fmt.Errorf(
				"%s: PLR rollback blocked: unknown print_stats.state %q",
				printer.Name, state,
			)
		}
	}
	if errors.Is(stateErr, moonraker.ErrMalformedState) {
		return fmt.Errorf(
			"%s: PLR rollback blocked by malformed print-state evidence: %w",
			printer.Name, stateErr,
		)
	}
	connection, connectionErr := client.KlippyConnection(ctx)
	if connectionErr != nil {
		return fmt.Errorf(
			"%s: PLR rollback could not verify an idle print or disconnected Klippy "+
				"(print query: %v; server info: %w)",
			printer.Name, stateErr, connectionErr,
		)
	}
	if connection.Connected {
		return fmt.Errorf(
			"%s: PLR rollback blocked: print state is unavailable but Moonraker says "+
				"Klippy is connected in state %q",
			printer.Name, connection.State,
		)
	}
	switch connection.State {
	case "disconnected", "error", "shutdown":
		return nil
	case "ready":
		return fmt.Errorf(
			"%s: PLR rollback blocked by contradictory Moonraker evidence: "+
				"klippy_connected=false with klippy_state=%q",
			printer.Name, connection.State,
		)
	default:
		return fmt.Errorf(
			"%s: PLR rollback blocked by non-terminal or unknown disconnected "+
				"klippy_state %q",
			printer.Name, connection.State,
		)
	}
}

func requireReturnedManifest(
	operation, expected string, result migrate.PLROperationResult,
) error {
	switch operation {
	case "start":
		if result.RollbackManifest != expected {
			return fmt.Errorf(
				"Klipper start returned rollback_manifest %q; expected exact input %q",
				result.RollbackManifest, expected,
			)
		}
	case "rollback":
		if result.BackupManifest != expected {
			return fmt.Errorf(
				"PLR rollback returned backup_manifest %q; expected exact input %q",
				result.BackupManifest, expected,
			)
		}
	}
	return nil
}

func makePLRReport(
	operation, printer string, result migrate.PLROperationResult,
) plrOperationReport {
	status := "unchanged"
	switch {
	case !result.Succeeded():
		status = "failed"
		if result.TransactionState != "" {
			status += "; " + strings.ReplaceAll(result.TransactionState, "_", " ")
		}
	case operation == "deploy" && result.DidChange():
		status = "deployed; Klipper stopped"
	case operation == "rollback" && result.DidChange():
		status = "rolled back; Klipper stopped"
	case operation == "start":
		status = "systemd stability window passed"
	}
	manifest := result.BackupManifest
	if manifest == "" {
		manifest = result.RollbackManifest
	}
	var paths *migrate.PLRPaths
	if result.TransactionState != "" {
		copy := result.Paths
		paths = &copy
	}
	rollbackNext := ""
	if result.NextCommand != "" && manifest != "" {
		// The firmware tool may describe its own low-level confirmation
		// syntax. Keep that private and surface Printerlab's stable operator UX.
		rollbackNext = plrRollbackGuidance
	}
	return plrOperationReport{
		Operation: operation, Printer: printer, OK: result.Succeeded(), Status: status,
		KlipperState: result.ObservedState, Phase: result.Phase,
		Account: result.Account, Paths: paths,
		TransactionState: result.TransactionState, BackupManifest: manifest,
		SafetyManifest: result.SafetyManifest,
		RollbackNext:   rollbackNext, FirmwareResult: result.Schema,
		Diagnostic: result.DiagnosticSummary(), NoImplicitRestart: true,
		ManifestFromTool: manifest != "",
		SafetyFromTool:   result.SafetyManifest != "",
	}
}

func makeUnavailablePLRReport(
	operation, printer, expectedSchema, manifest string, cause error,
) plrOperationReport {
	transactionState := ""
	if operation == "deploy" || operation == "rollback" {
		transactionState = migrate.PLRTransactionIndeterminate
	}
	rollbackNext := ""
	if manifest != "" {
		rollbackNext = plrRollbackGuidance
	}
	return plrOperationReport{
		Operation: operation, Printer: printer, OK: false,
		Status:           "failed; firmware result unavailable or invalid",
		TransactionState: transactionState,
		BackupManifest:   manifest,
		RollbackNext:     rollbackNext,
		FirmwareResult:   "unavailable (expected " + expectedSchema + ")",
		Diagnostic: fmt.Sprintf(
			"firmware operation returned no valid JSON result: %v; "+
				"no retry or rollback was attempted",
			cause,
		),
		NoImplicitRestart: true,
		ManifestFromTool:  false,
	}
}

func ensurePLRFailureRecovery(report *plrOperationReport, callerManifest string) {
	if report.OK {
		return
	}
	if report.BackupManifest == "" {
		report.BackupManifest = callerManifest
		report.ManifestFromTool = false
	}
	if report.RollbackNext == "" && report.BackupManifest != "" {
		report.RollbackNext = plrRollbackGuidance
	}
}

func writePLRReport(a *app, report plrOperationReport) error {
	table := output.Table{Headers: []string{
		"OPERATION", "PRINTER", "ACCOUNT", "PHASE", "TRANSACTION", "STATUS", "KLIPPER",
		"BACKUP MANIFEST", "SAFETY MANIFEST", "ROLLBACK NEXT", "DETAIL",
	}}
	table.AddRow(
		report.Operation, report.Printer, report.Account, report.Phase, report.TransactionState,
		report.Status, report.KlipperState, report.BackupManifest,
		report.SafetyManifest, report.RollbackNext, report.Diagnostic,
	)
	return output.Write(a.out, a.format, report, table)
}
