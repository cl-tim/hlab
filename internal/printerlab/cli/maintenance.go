package cli

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

type maintenanceReport struct {
	Printer           string     `json:"printer" yaml:"printer"`
	Component         string     `json:"component,omitempty" yaml:"component,omitempty"`
	State             string     `json:"state" yaml:"state"`
	IntervalHours     float64    `json:"intervalHours,omitempty" yaml:"intervalHours,omitempty"`
	LastServicedAt    *time.Time `json:"lastServicedAt,omitempty" yaml:"lastServicedAt,omitempty"`
	PrintHoursElapsed *float64   `json:"printHoursElapsed,omitempty" yaml:"printHoursElapsed,omitempty"`
	Error             string     `json:"error,omitempty" yaml:"error,omitempty"`
}

func newMaintenanceCmd(a *app) *cobra.Command {
	command := &cobra.Command{Use: "maintenance", Short: "Track component service by accumulated print time"}
	var intervalHours float64
	set := &cobra.Command{
		Use:   "set PRINTER COMPONENT",
		Short: "Set an existing component's service interval in print hours",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if intervalHours <= 0 || math.IsNaN(intervalHours) || math.IsInf(intervalHours, 0) {
				return fmt.Errorf("--interval-hours must be a positive finite number")
			}
			var report maintenanceReport
			err := config.EditComponentMaintenance(a.cfg.Path, args[0], args[1], func(component inventory.Component) (inventory.Maintenance, error) {
				var maintenance inventory.Maintenance
				if component.Maintenance != nil {
					maintenance = *component.Maintenance
				}
				maintenance.IntervalHours = intervalHours
				report = newMaintenanceReport(args[0], args[1], "scheduled", maintenance)
				return maintenance, nil
			})
			if err != nil {
				return err
			}
			return writeMaintenance(a, []maintenanceReport{report})
		},
	}
	set.Flags().Float64Var(&intervalHours, "interval-hours", 0, "service interval in accumulated print hours")
	command.AddCommand(set, newMaintenanceRecordCmd(a), newMaintenanceDueCmd(a))
	return command
}

func newMaintenanceRecordCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "record PRINTER COMPONENT",
		Short: "Record service now and capture the current print-time baseline",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var report maintenanceReport
			err := config.EditComponentMaintenance(a.cfg.Path, args[0], args[1], func(component inventory.Component) (inventory.Maintenance, error) {
				if component.EffectiveStatus() != "installed" || component.Maintenance == nil {
					return inventory.Maintenance{}, fmt.Errorf("component %q must be installed and have a maintenance interval", args[1])
				}
				// Resolve the live endpoint within the edit so changes made while
				// the counter is read are covered by the inventory's edit check.
				current, err := config.Load(a.cfg.Path)
				if err != nil {
					return inventory.Maintenance{}, err
				}
				printer, ok := current.Inventory.Printer(args[0])
				if !ok {
					return inventory.Maintenance{}, fmt.Errorf("unknown printer %q", args[0])
				}
				client, err := moonraker.New(printer, a.timeout())
				if err != nil {
					return inventory.Maintenance{}, err
				}
				seconds, err := client.TotalPrintSeconds(cmd.Context())
				if err != nil {
					return inventory.Maintenance{}, fmt.Errorf("read print time for %s: %w", printer.Name, err)
				}
				now := time.Now().UTC()
				maintenance := *component.Maintenance
				maintenance.LastServicedAt, maintenance.BaselinePrintSeconds = &now, &seconds
				report = newMaintenanceReport(printer.Name, args[1], "recorded", maintenance)
				return maintenance, nil
			})
			if err != nil {
				return err
			}
			return writeMaintenance(a, []maintenanceReport{report})
		},
	}
}

func newMaintenanceDueCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "due [selector]",
		Short: "Show service status for installed components with a service interval",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			byPrinter := readFleet(ctx, a, printers, func(p inventory.Printer, client *moonraker.Client, readErr error) []maintenanceReport {
				fetchTotal := func() (float64, error) {
					if readErr != nil {
						return 0, readErr
					}
					return client.TotalPrintSeconds(ctx)
				}
				return checkMaintenance(p, fetchTotal)
			})
			reports := make([]maintenanceReport, 0)
			for _, printerReports := range byPrinter {
				reports = append(reports, printerReports...)
			}
			return writeMaintenance(a, reports)
		},
	}
}

func checkMaintenance(p inventory.Printer, fetchTotal func() (float64, error)) []maintenanceReport {
	var reports []maintenanceReport
	readTotal := sync.OnceValues(fetchTotal)
	for _, id := range sortedComponentKeys(p.Components) {
		component := p.Components[id]
		if component.EffectiveStatus() != "installed" || component.Maintenance == nil {
			continue
		}
		m := *component.Maintenance
		report := newMaintenanceReport(p.Name, id, "unrecorded", m)
		if m.BaselinePrintSeconds != nil {
			total, err := readTotal()
			switch {
			case err != nil:
				report.State, report.Error = "unknown", err.Error()
			case total < *m.BaselinePrintSeconds:
				report.State, report.Error = "unknown", "Moonraker print total is below the recorded baseline"
			default:
				hours := (total - *m.BaselinePrintSeconds) / 3600
				report.PrintHoursElapsed = &hours
				report.State = "ok"
				if hours >= m.IntervalHours {
					report.State = "due"
				}
			}
		}
		reports = append(reports, report)
	}
	return reports
}

func newMaintenanceReport(printer, component, state string, m inventory.Maintenance) maintenanceReport {
	return maintenanceReport{
		Printer: printer, Component: component, State: state,
		IntervalHours: m.IntervalHours, LastServicedAt: m.LastServicedAt,
	}
}

func writeMaintenance(a *app, reports []maintenanceReport) error {
	table := output.Table{Headers: []string{"PRINTER", "COMPONENT", "STATUS", "INTERVAL (H)", "USED (H)", "LAST SERVICED (UTC)", "ISSUE"}}
	if len(reports) == 0 {
		table.AddRow("-", "-", "-", "-", "-", "-", "No installed components with maintenance intervals")
	}
	failed := false
	for _, report := range reports {
		used, serviced := "-", "-"
		if report.PrintHoursElapsed != nil {
			used = fmt.Sprintf("%.2f", *report.PrintHoursElapsed)
		}
		if report.LastServicedAt != nil {
			serviced = report.LastServicedAt.UTC().Format(time.RFC3339)
		}
		failed = failed || report.Error != ""
		table.AddRow(report.Printer, report.Component, report.State, report.IntervalHours, used, serviced, report.Error)
	}
	if err := output.Write(a.out, a.format, reports, table); err != nil {
		return err
	}
	if failed {
		return &ExitError{Code: 1}
	}
	return nil
}
