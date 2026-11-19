package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

type klippeeBoxGuardReport struct {
	Printer  string                   `json:"printer" yaml:"printer"`
	Safe     bool                     `json:"safe" yaml:"safe"`
	Issues   []string                 `json:"issues,omitempty" yaml:"issues,omitempty"`
	Evidence moonraker.BoxGuardStatus `json:"evidence" yaml:"evidence"`
}

func newKlippeeBoxGuardStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "box-guard-status PRINTER",
		Short: "Verify the active QIDI Box watchdog and output-clock mitigations",
		Long: "Check the Box output-clock guard, motion sensor polling, and automatic\n" +
			"filament insertion settings through Moonraker.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			evidence, err := client.BoxGuardStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: QIDI Box guard status failed: %w", printer.Name, err)
			}
			report := evaluateKlippeeBoxGuardStatus(printer.Name, evidence)
			table := output.Table{Headers: []string{
				"PRINTER", "SAFE", "BOXES", "CLOCK GUARD", "INSERT GUARD", "B OUTPUT", "E OUTPUT", "USE IRQ", "DEBOUNCE US", "AUTO INSERT", "ISSUES",
			}}
			autoInsert := "disabled (default)"
			if evidence.AutoInsertVariablePresent {
				autoInsert = "disabled (stored)"
			}
			if evidence.AutoInsertEnabled {
				autoInsert = "enabled"
			}
			useIRQ := "unknown"
			if evidence.UseIRQKnown {
				useIRQ = fmt.Sprint(evidence.UseIRQ)
			}
			debounce := "absent"
			if evidence.DebounceUSPresent {
				debounce = "invalid"
				if evidence.DebounceUSKnown {
					debounce = fmt.Sprint(evidence.DebounceUS)
				}
			}
			table.AddRow(
				report.Printer,
				report.Safe,
				evidence.BoxCount,
				evidence.State,
				evidence.ReloadAllGuarded,
				boxOutputSummary(evidence.Outputs["b_output"]),
				boxOutputSummary(evidence.Outputs["e_output"]),
				useIRQ,
				debounce,
				autoInsert,
				strings.Join(report.Issues, "; "),
			)
			if err := output.Write(a.out, a.format, report, table); err != nil {
				return err
			}
			if !report.Safe {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
}

func boxOutputSummary(status moonraker.BoxOutputClockStatus) string {
	if status.State == "" {
		return "missing"
	}
	return fmt.Sprintf("%s (%s -> %s)", status.State, status.PreviousMCU, status.PinMCU)
}

func evaluateKlippeeBoxGuardStatus(
	printer string,
	evidence moonraker.BoxGuardStatus,
) klippeeBoxGuardReport {
	report := klippeeBoxGuardReport{Printer: printer, Evidence: evidence}
	if evidence.BoxCount < 1 || evidence.BoxCount > 4 {
		report.Issues = append(report.Issues, fmt.Sprintf("detected Box count is %d", evidence.BoxCount))
	}
	if !evidence.ConfigSectionLoaded {
		report.Issues = append(report.Issues, "box_output_clock_guard is not loaded")
	}
	if !evidence.ReloadAllGuarded {
		report.Issues = append(report.Issues, "RELOAD_ALL automatic-insertion guard is not active")
	}
	if evidence.State != "corrected" && evidence.State != "already-correct" {
		report.Issues = append(report.Issues, fmt.Sprintf("clock guard state is %q", evidence.State))
	}
	for _, name := range []string{"b_output", "e_output"} {
		status, present := evidence.Outputs[name]
		if !present {
			report.Issues = append(report.Issues, name+" status is missing")
			continue
		}
		if status.State != "corrected" && status.State != "already-correct" {
			report.Issues = append(report.Issues, fmt.Sprintf("%s state is %q", name, status.State))
		}
		if status.PinMCU != "mcu_box1" {
			report.Issues = append(report.Issues, fmt.Sprintf("%s pin MCU is %q", name, status.PinMCU))
		}
	}
	if !evidence.MotionSensorSectionLoaded {
		report.Issues = append(report.Issues, "box_motion_sensor config is not loaded")
	} else if !evidence.UseIRQKnown {
		report.Issues = append(report.Issues, "box_motion_sensor use_irq is missing or invalid")
	} else if evidence.UseIRQ {
		report.Issues = append(report.Issues, "box_motion_sensor use_irq is enabled")
	}
	if evidence.DebounceUSPresent {
		if evidence.DebounceUSKnown {
			report.Issues = append(
				report.Issues,
				fmt.Sprintf("box_motion_sensor debounce_us is configured as %d", evidence.DebounceUS),
			)
		} else {
			report.Issues = append(report.Issues, "box_motion_sensor debounce_us is invalid")
		}
	}
	if evidence.AutoInsertEnabled {
		report.Issues = append(report.Issues, "automatic QIDI Box insertion is enabled")
	}
	report.Safe = len(report.Issues) == 0
	return report
}
