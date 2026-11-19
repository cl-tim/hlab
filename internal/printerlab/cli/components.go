package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/config"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/moonraker"
)

type componentReport struct {
	Printer    string                         `json:"printer" yaml:"printer"`
	Components map[string]inventory.Component `json:"components" yaml:"components"`
	Snapshot   *inventory.ComponentSnapshot   `json:"snapshot,omitempty" yaml:"snapshot,omitempty"`
	Error      string                         `json:"error,omitempty" yaml:"error,omitempty"`
}

func newComponentsCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use: "components [selector]", Short: "Show saved physical parts and configured hardware",
		Long: "Show recorded components and the latest Klipper configuration snapshot, offline.\n" +
			"Use set for physical identities and planned upgrades; capture reads configured\n" +
			"hardware from Moonraker without changing the printer. Missing identities stay unknown.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			reports := make([]componentReport, 0, len(printers))
			for _, printer := range printers {
				reports = append(reports, reportComponents(printer))
			}
			return writeComponents(a, reports)
		},
	}
	command.AddCommand(newComponentSetCmd(a), newComponentRemoveCmd(a), newComponentCaptureCmd(a))
	return command
}

func reportComponents(printer inventory.Printer) componentReport {
	components := printer.Components
	if components == nil {
		components = map[string]inventory.Component{}
	}
	return componentReport{Printer: printer.Name, Components: components, Snapshot: printer.ComponentSnapshot}
}

func newComponentSetCmd(a *app) *cobra.Command {
	values := make(map[string]*string)
	command := &cobra.Command{
		Use: "set PRINTER COMPONENT", Short: "Add or update a physical component in the local inventory",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, ok := a.cfg.Inventory.Printer(args[0])
			if !ok {
				return fmt.Errorf("unknown printer %q", args[0])
			}
			fields := make(map[string]string)
			for flag, value := range values {
				if cmd.Flags().Changed(flag) {
					key := flag
					if flag == "config-section" {
						key = "configSection"
					}
					fields[key] = *value
				}
			}
			if len(fields) == 0 {
				return fmt.Errorf("supply component fields; new components require --type and --model")
			}
			if err := config.EditComponent(a.cfg.Path, printer.Name, args[1], fields, false); err != nil {
				return err
			}
			return showSavedComponents(a, printer.Name)
		},
	}
	for _, flag := range []struct{ name, help string }{
		{"type", "component kind, e.g. hotend, toolhead, extruder, probe or controller"},
		{"model", "physical component model"}, {"manufacturer", "component manufacturer"},
		{"status", "installed (default), planned, spare or removed"},
		{"notes", "fitment, variant and other notes"}, {"source", "identity evidence or reference URL"},
		{"config-section", "optional matching Klipper section, e.g. extruder or mcu EBBCan"},
	} {
		values[flag.name] = command.Flags().String(flag.name, "", flag.help)
	}
	return command
}

func newComponentRemoveCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "remove PRINTER COMPONENT", Short: "Delete a local component record (use set --status removed to retain it)",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if _, ok := a.cfg.Inventory.Printer(args[0]); !ok {
				return fmt.Errorf("unknown printer %q", args[0])
			}
			if err := config.EditComponent(a.cfg.Path, args[0], args[1], nil, true); err != nil {
				return err
			}
			return showSavedComponents(a, args[0])
		},
	}
}

func showSavedComponents(a *app, name string) error {
	cfg, err := config.Load(a.cfg.Path)
	if err != nil {
		return err
	}
	printer, _ := cfg.Inventory.Printer(name)
	return writeComponents(a, []componentReport{reportComponents(printer)})
}

func newComponentCaptureCmd(a *app) *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use: "capture [selector]", Short: "Collect active Klipper hardware and save a local snapshot",
		Long: "Read hardware configuration through Moonraker GET requests. Save one snapshot per\n" +
			"selected printer, preserving physical part records. No homing, heating, SSH or\n" +
			"printer writes. If any selected printer fails, retain all previous snapshots.\n" +
			"Snapshots describe configuration at Klipper's last restart, not verified physical fitment.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			before, err := os.ReadFile(a.cfg.Path)
			if err != nil {
				return err
			}
			reports := collectComponents(cmd.Context(), a, printers)
			snapshots := make(map[string]inventory.ComponentSnapshot, len(reports))
			failed := false
			for _, report := range reports {
				if report.Error != "" {
					failed = true
					continue
				}
				snapshots[report.Printer] = *report.Snapshot
			}
			if !failed && !dryRun {
				current, err := os.ReadFile(a.cfg.Path)
				if err != nil {
					return err
				}
				if !bytes.Equal(current, before) {
					return fmt.Errorf("inventory changed during capture; retry")
				}
				if err := config.SaveComponentSnapshots(a.cfg.Path, snapshots); err != nil {
					return err
				}
			}
			if err := writeComponents(a, reports); err != nil {
				return err
			}
			if failed {
				return fmt.Errorf("component capture failed; no snapshots saved")
			}
			if !dryRun {
				_, err = fmt.Fprintf(a.errOut, "Saved component snapshots for %d printer(s) to %s\n", len(reports), a.cfg.Path)
			}
			return err
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview collected hardware without saving")
	return command
}

func collectComponents(ctx context.Context, a *app, printers []inventory.Printer) []componentReport {
	results := make([]componentReport, len(printers))
	sem := make(chan struct{}, max(1, a.parallelism()))
	var wg sync.WaitGroup
	for index, printer := range printers {
		wg.Go(func() {
			report := reportComponents(printer)
			// Failed collection has no fresh snapshot; never relabel old evidence.
			report.Snapshot = nil
			defer func() { results[index] = report }()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				report.Error = ctx.Err().Error()
				return
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				report.Error = err.Error()
				return
			}
			snapshot, err := client.ComponentInventory(ctx)
			if err != nil {
				report.Error = err.Error()
				return
			}
			report.Snapshot = &snapshot
		})
	}
	wg.Wait()
	return results
}

func writeComponents(a *app, reports []componentReport) error {
	table := output.Table{Headers: []string{"PRINTER", "COMPONENT / SECTION", "TYPE", "STATUS", "MODEL / SETTINGS", "NOTES / CAPTURED"}}
	for _, report := range reports {
		for _, id := range sortedComponentKeys(report.Components) {
			c := report.Components[id]
			table.AddRow(report.Printer, id, c.Type, c.EffectiveStatus(), strings.TrimSpace(c.Manufacturer+" "+c.Model), c.Notes)
		}
		if report.Snapshot != nil {
			for _, section := range sortedComponentKeys(report.Snapshot.Components) {
				c := report.Snapshot.Components[section]
				settings := make([]string, 0, len(c.Settings))
				for _, key := range sortedComponentKeys(c.Settings) {
					settings = append(settings, key+"="+c.Settings[key])
				}
				table.AddRow(report.Printer, "["+section+"]", c.Type, "configured", strings.Join(settings, "; "), report.Snapshot.CapturedAt)
			}
		}
		if report.Error != "" {
			table.AddRow(report.Printer, "-", "-", "error", report.Error, "")
		} else if len(report.Components) == 0 && (report.Snapshot == nil || len(report.Snapshot.Components) == 0) {
			table.AddRow(report.Printer, "-", "-", "unknown", "No component identities recorded", "")
		}
	}
	return output.Write(a.out, a.format, reports, table)
}

func sortedComponentKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
