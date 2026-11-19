package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

type klippeeServiceRestartReport struct {
	Printer      string `json:"printer" yaml:"printer"`
	Service      string `json:"service" yaml:"service"`
	PreviousPID  int    `json:"previousPid" yaml:"previousPid"`
	CurrentPID   int    `json:"currentPid" yaml:"currentPid"`
	ServiceState string `json:"serviceState" yaml:"serviceState"`
	KlippyState  string `json:"klippyState" yaml:"klippyState"`
}

func newKlippeeServiceRestartCmd(a *app) *cobra.Command {
	var readyTimeout time.Duration
	command := &cobra.Command{
		Use:   "service-restart PRINTER",
		Short: "Restart the Klipper host process after installing Python modules",
		Long: "Restart klipper.service through SSH and wait for Moonraker to report Klippy ready.\n" +
			"Requires an idle printer. Use after installing or upgrading Klipper Python modules.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("Klipper service restart"); err != nil {
				return err
			}
			if readyTimeout <= 0 || readyTimeout > maximumKlippyReadyTimeout {
				return fmt.Errorf(
					"--ready-timeout must be greater than zero and at most %s",
					maximumKlippyReadyTimeout,
				)
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
				return fmt.Errorf("%s: Klipper service restart blocked: %w", printer.Name, err)
			}

			restartCtx, cancel := context.WithTimeout(cmd.Context(), readyTimeout)
			defer cancel()
			raw, err := a.migrationRunner().KlipperServiceRestartSSHOutput(
				restartCtx, printer, a.timeout(),
			)
			if err != nil {
				return fmt.Errorf(
					"%s: guarded Klipper service restart failed; outcome may be ambiguous and was not retried: %w",
					printer.Name, err,
				)
			}
			proof, err := migrate.ParseKlipperServiceRestart(raw)
			if err != nil {
				return fmt.Errorf(
					"%s: Klipper service restart returned invalid proof; do not retry before checking status: %w",
					printer.Name, err,
				)
			}
			ready, err := client.WaitForKlippyReady(
				restartCtx, readyTimeout, klippyReadyPollInterval,
			)
			if err != nil {
				return fmt.Errorf(
					"%s: klipper.service restarted with PID %d, but Klippy did not become ready: %w",
					printer.Name, proof.CurrentPID, err,
				)
			}
			report := klippeeServiceRestartReport{
				Printer: printer.Name, Service: proof.Service,
				PreviousPID: proof.PreviousPID, CurrentPID: proof.CurrentPID,
				ServiceState: proof.State, KlippyState: ready.State,
			}
			table := output.Table{Headers: []string{
				"PRINTER", "SERVICE", "PREVIOUS PID", "CURRENT PID", "SERVICE STATE", "KLIPPY",
			}}
			table.AddRow(
				report.Printer, report.Service, report.PreviousPID, report.CurrentPID,
				report.ServiceState, report.KlippyState,
			)
			return output.Write(a.out, a.format, report, table)
		},
	}
	command.Flags().DurationVar(
		&readyTimeout, "ready-timeout", defaultKlippyReadyTimeout,
		"maximum wait for the service restart and Klippy readiness",
	)
	return command
}
