package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
	"github.com/cl-tim/hlab/internal/printerlab/stages"
)

func newMainlineCmd(a *app) *cobra.Command {
	command := &cobra.Command{
		Use:    "mainline",
		Short:  "Plan and run Plus4 firmware migration stages",
		Hidden: true, // Compatibility for existing scripts; public entry point is firmware.
	}
	command.AddCommand(
		newMainlinePlanCmd(a),
		newMainlinePreflightCmd(a),
		newMainlineStateCmd(a),
		newMainlineNextCmd(a),
		newMainlineResumeCmd(a),
		newMainlineSetupUserCmd(a),
		newMainlineMarkCmd(a),
		newMainlinePathsCmd(a),
		newMainlineArmbianImageCmd(a),
		newMainlineFactoryImageCmd(a),
		newMainlineConnectionStage(a, "setup-ssh", "Install a management SSH key", false, ""),
		newMainlineConnectionStage(a, "audit", "Audit Plus4 hardware and firmware", false, ""),
		newMainlineBackupCmd(a),
		newMainlineConnectionStage(a, "audit-host-tuning", "Audit mainline host latency tuning", false, ""),
		newMainlineConnectionStage(a, "tune-host", "Install persistent mainline host tuning", true, "TUNE-HOST"),
		newMainlineBootstrapCmd(a),
		newMainlineSmokeTestCmd(a),
		newMainlineFlashCmd(a, "flash-toolhead-katapult", "Erase the toolhead and install Katapult", "ERASE-TOOLHEAD"),
		newMainlineFlashCmd(a, "flash-toolhead", "Flash mainline Klipper through Katapult", "FLASH-TOOLHEAD"),
		newMainlineBuildCmd(a),
		newMainlineStageMainboardCmd(a),
		newMainlineRenderConfigCmd(a),
		newMainlineDeployPLRCmd(a),
		newMainlineStartKlipperCmd(a),
		newMainlineRollbackPLRCmd(a),
	)
	return command
}

type preflightCheck struct {
	Check  string `json:"check" yaml:"check"`
	Result string `json:"result" yaml:"result"`
	Detail string `json:"detail,omitempty" yaml:"detail,omitempty"`
}

func newMainlinePreflightCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "preflight PRINTER",
		Short: "Check migration prerequisites and printer idle state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			checks := []preflightCheck{
				{Check: "model", Result: "pass", Detail: printer.Model},
				{Check: "firmware-declaration", Result: "pass", Detail: printer.Firmware},
				{Check: "firmware-tool", Result: "pass"},
				{Check: "moonraker-idle", Result: "pass"},
			}
			failed := false
			if err := migrate.RequirePlus4(printer); err != nil {
				checks[0].Result, checks[0].Detail, failed = "fail", err.Error(), true
			}
			if printer.Firmware == inventory.FirmwareAuto {
				checks[1].Result = "warn"
				checks[1].Detail = "declare stock or mainline before migration"
			}
			if err := a.migrationRunner().Validate(); err != nil {
				checks[2].Result, checks[2].Detail, failed = "fail", err.Error(), true
			} else {
				checks[2].Detail, _ = a.migrationRunner().ToolRoot()
			}
			client, err := moonraker.New(printer, a.timeout())
			if err == nil {
				err = client.EnsureIdle(cmd.Context())
			}
			if err != nil {
				checks[3].Result, checks[3].Detail, failed = "fail", err.Error(), true
			} else {
				checks[3].Detail = "printer is idle"
			}

			table := output.Table{Headers: []string{"CHECK", "RESULT", "DETAIL"}}
			for _, check := range checks {
				table.AddRow(check.Check, check.Result, check.Detail)
			}
			if err := output.Write(a.out, a.format, checks, table); err != nil {
				return err
			}
			if failed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
}

func (a *app) migrationRunner() migrate.Runner {
	if a.cfg == nil {
		return migrate.Runner{Home: config.Home(), Out: a.out, ErrOut: a.errOut}
	}
	var bootstrapPasswordEnvironments []string
	if a.cfg.Inventory != nil {
		seen := map[string]struct{}{}
		for _, printer := range a.cfg.Inventory.Printers() {
			if printer.SSHPasswordEnv == "" {
				continue
			}
			if _, exists := seen[printer.SSHPasswordEnv]; exists {
				continue
			}
			seen[printer.SSHPasswordEnv] = struct{}{}
			bootstrapPasswordEnvironments = append(
				bootstrapPasswordEnvironments, printer.SSHPasswordEnv,
			)
		}
	}
	return migrate.Runner{
		Repo: a.cfg.Defaults.FirmwareRepo,
		Home: config.Home(),
		Out:  a.out, ErrOut: a.errOut,
		BootstrapPasswordEnvironments: bootstrapPasswordEnvironments,
	}
}

func newMainlinePlanCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "plan PRINTER",
		Short: "Show the Plus4 migration sequence",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			// Rendered from the same graph the resume engine reconciles
			// against, so the documented order cannot drift from the enforced
			// one.
			type stage struct {
				Order    int      `json:"order" yaml:"order"`
				Name     string   `json:"name" yaml:"name"`
				Requires []string `json:"requires,omitempty" yaml:"requires,omitempty"`
				Gate     string   `json:"gate" yaml:"gate"`
			}
			var listed []stage
			table := output.Table{
				Headers: []string{"ORDER", "STAGE", "REQUIRES", "SAFETY GATE"},
			}
			for index, item := range stages.Graph() {
				gate := "no approval required"
				if item.Confirm != "" {
					gate = "-y/--yes"
				}
				if item.Impl == "" {
					gate = "performed by the operator"
				}
				entry := stage{index + 1, item.ID, item.Requires, gate}
				listed = append(listed, entry)
				table.AddRow(entry.Order, entry.Name,
					strings.Join(entry.Requires, ","), entry.Gate)
			}
			return output.Write(a.out, a.format, listed, table)
		},
	}
}

func newMainlineConnectionStage(
	a *app,
	name, description string,
	mutating bool,
	confirmation string,
) *cobra.Command {
	var generate bool
	command := &cobra.Command{
		Use:   name + " PRINTER",
		Short: description,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if confirmation != "" {
				if err := a.requireApproval(name); err != nil {
					return err
				}
			}
			if mutating {
				if err := a.ensureIdle(cmd.Context(), printer, false); err != nil {
					return err
				}
			}
			connectionArgs := migrate.ConnectionArgs(printer)
			if name == "setup-ssh" {
				connectionArgs = migrate.BootstrapConnectionArgs(printer)
			}
			stageArgs := append([]string{name}, connectionArgs...)
			if name == "setup-ssh" && generate {
				stageArgs = append(stageArgs, "--generate")
			}
			if confirmation != "" {
				stageArgs = append(stageArgs, "--confirm", confirmation)
			}
			return a.runStage(cmd.Context(), printer, name, stageArgs...)
		},
	}
	if name == "setup-ssh" {
		command.Flags().BoolVar(&generate, "generate", false, "generate an ed25519 key if needed")
	}
	return command
}

func newMainlineBackupCmd(a *app) *cobra.Command {
	var outputDir string
	var includeGCodes, includeLogs, includeEnvironments bool
	command := &cobra.Command{
		Use:   "backup PRINTER",
		Short: "Create a streamed pre-migration backup",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			stageArgs := append([]string{"backup"}, migrate.ConnectionArgs(printer)...)
			stageArgs = append(stageArgs, "--name", printer.Name)
			if outputDir != "" {
				stageArgs = append(stageArgs, "--output", outputDir)
			}
			if includeGCodes {
				stageArgs = append(stageArgs, "--include-gcodes")
			}
			if includeLogs {
				stageArgs = append(stageArgs, "--include-logs")
			}
			if includeEnvironments {
				stageArgs = append(stageArgs, "--include-environments")
			}
			return a.runStage(cmd.Context(), printer, "backup", stageArgs...)
		},
	}
	command.Flags().StringVar(&outputDir, "output", "", "backup output directory")
	command.Flags().BoolVar(&includeGCodes, "include-gcodes", false, "include uploaded G-code")
	command.Flags().BoolVar(&includeLogs, "include-logs", false, "include printer logs")
	command.Flags().BoolVar(&includeEnvironments, "include-environments", false, "include Python environments")
	return command
}

func newMainlineBootstrapCmd(a *app) *cobra.Command {
	var klipperRef, moonrakerRef, katapultRef string
	var allowUnverified bool
	command := &cobra.Command{
		Use:   "bootstrap-host PRINTER",
		Short: "Install upstream Klipper, Moonraker, Katapult, and host tuning",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if err := a.requireApproval("mainline host bootstrap"); err != nil {
				return err
			}
			if err := a.ensureIdle(cmd.Context(), printer, allowUnverified); err != nil {
				return err
			}
			stageArgs := append([]string{"bootstrap-host"}, migrate.ConnectionArgs(printer)...)
			k, moon, kat := migrationRefs(printer.Migration)
			if klipperRef == "" {
				klipperRef = k
			}
			if moonrakerRef == "" {
				moonrakerRef = moon
			}
			if katapultRef == "" {
				katapultRef = kat
			}
			stageArgs = append(stageArgs,
				"--confirm", "BOOTSTRAP-HOST",
				"--klipper-ref", klipperRef,
				"--moonraker-ref", moonrakerRef,
				"--katapult-ref", katapultRef,
				"--artifacts", printer.Migration.ArtifactDir,
			)
			if printer.Migration.BeaconRef != "" {
				stageArgs = append(stageArgs, "--beacon-ref", printer.Migration.BeaconRef)
			}
			return a.runStage(cmd.Context(), printer, "bootstrap-host", stageArgs...)
		},
	}
	command.Flags().BoolVar(
		&allowUnverified, "allow-unverified-idle", false,
		"continue when Moonraker is unavailable after physically confirming the printer is idle",
	)
	command.Flags().StringVar(&klipperRef, "klipper-ref", "", "Klipper full commit SHA (default: inventory or bundled pin)")
	command.Flags().StringVar(&moonrakerRef, "moonraker-ref", "", "Moonraker full commit SHA (default: inventory or bundled pin)")
	command.Flags().StringVar(&katapultRef, "katapult-ref", "", "Katapult full commit SHA (default: inventory or bundled pin)")
	return command
}

func newMainlineSmokeTestCmd(a *app) *cobra.Command {
	var dataDir string
	var allowUnverified bool
	command := &cobra.Command{
		Use:   "smoke-test PRINTER",
		Short: "Install the inert two-MCU smoke-test configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if err := a.requireApproval("mainline smoke-test configuration installation"); err != nil {
				return err
			}
			if err := a.ensureIdle(cmd.Context(), printer, allowUnverified); err != nil {
				return err
			}
			stageArgs := append([]string{"smoke-test"}, migrate.ConnectionArgs(printer)...)
			stageArgs = append(stageArgs, "--data-dir", dataDir)
			return a.runStage(cmd.Context(), printer, "smoke-test", stageArgs...)
		},
	}
	command.Flags().StringVar(&dataDir, "data-dir", "/home/mks/printer_data", "remote printer data directory")
	command.Flags().BoolVar(
		&allowUnverified, "allow-unverified-idle", false,
		"continue when Klippy is unavailable after physically confirming the printer is idle",
	)
	return command
}

func newMainlineFlashCmd(a *app, name, description, confirmation string) *cobra.Command {
	var image, remoteDirectory, katapultDirectory string
	var allowUnverified bool
	command := &cobra.Command{
		Use:   name + " PRINTER",
		Short: description,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if err := a.requireApproval(name); err != nil {
				return err
			}
			if err := a.ensureIdle(cmd.Context(), printer, allowUnverified); err != nil {
				return err
			}
			if image == "" {
				image = imagePath(printer, flashImageName(name))
				if image == "" {
					return fmt.Errorf(
						"--image is required, or set defaults.migration.artifactDir")
				}
				// Show the resolved image before flashing.
				_, _ = fmt.Fprintf(a.errOut, "Using image from inventory: %s\n", image)
			}
			stageArgs := append([]string{name}, migrate.ConnectionArgs(printer)...)
			stageArgs = append(stageArgs,
				"--image", image,
				"--artifacts", filepath.Dir(image),
				"--remote-directory", remoteDirectory,
				"--confirm", confirmation,
			)
			if name == "flash-toolhead" {
				stageArgs = append(stageArgs, "--katapult-directory", katapultDirectory)
			}
			return a.runStage(cmd.Context(), printer, name, stageArgs...)
		},
	}
	command.Flags().StringVar(&image, "image", "",
		"validated firmware image (default: from defaults.migration.artifactDir)")
	command.Flags().StringVar(
		&remoteDirectory, "remote-directory", "/home/mks/qidi-mainline-migration",
		"remote staging directory",
	)
	command.Flags().BoolVar(
		&allowUnverified, "allow-unverified-idle", false,
		"continue when Klippy is unavailable after physically confirming the printer is idle",
	)
	if name == "flash-toolhead" {
		command.Flags().StringVar(
			&katapultDirectory, "katapult-directory", "/home/mks/katapult",
			"remote Katapult source directory",
		)
	}
	return command
}

// flashImageName maps a flash stage to the artifact `build` produces for it.
func flashImageName(stage string) string {
	if stage == "flash-toolhead-katapult" {
		return migrate.ToolheadKatapultImage
	}
	return migrate.ToolheadImage
}

func newMainlineBuildCmd(a *app) *cobra.Command {
	var klipperDir, katapultDir, outputDir, forPrinter string
	command := &cobra.Command{
		Use:   "build",
		Short: "Build and validate all Plus4 MCU artifacts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			defaults := a.migrationDefaults()
			if klipperDir == "" {
				klipperDir = defaults.KlipperDir
			}
			if katapultDir == "" {
				katapultDir = defaults.KatapultDir
			}
			if outputDir == "" {
				outputDir = defaults.ArtifactDir
			}
			for flag, value := range map[string]string{
				"--klipper-dir": klipperDir, "--katapult-dir": katapultDir,
			} {
				if value == "" {
					return fmt.Errorf(
						"%s is required, or set defaults.migration in the inventory", flag)
				}
			}
			args := []string{
				"build",
				"--klipper-dir", klipperDir,
				"--katapult-dir", katapultDir,
			}
			refs := inventory.Migration{KlipperRef: defaults.KlipperRef, KatapultRef: defaults.KatapultRef}
			if forPrinter != "" {
				printer, err := a.onePrinter(forPrinter)
				if err != nil {
					return err
				}
				refs = printer.Migration
				if !cmd.Flags().Changed("output") {
					outputDir = printer.Migration.ArtifactDir
				}
			}
			k, _, kat := migrationRefs(refs)
			args = append(args, "--klipper-ref", k, "--katapult-ref", kat)
			if defaults.WSLDistro != "" {
				args = append(args, "--wsl-distro", defaults.WSLDistro)
			}
			if outputDir != "" {
				args = append(args, "--output", outputDir)
			}
			return a.runWorkstationStage(cmd.Context(), forPrinter, "build", args...)
		},
	}
	command.Flags().StringVar(&klipperDir, "klipper-dir", "",
		"local upstream Klipper checkout (default: defaults.migration.klipperDir)")
	command.Flags().StringVar(&katapultDir, "katapult-dir", "",
		"local upstream Katapult checkout (default: defaults.migration.katapultDir)")
	command.Flags().StringVar(&outputDir, "output", "",
		"artifact output directory (default: defaults.migration.artifactDir)")
	command.Flags().StringVar(&forPrinter, "for", "", "record this stage in a printer's migration journal")
	return command
}

func newMainlineStageMainboardCmd(a *app) *cobra.Command {
	var image, outputDir, forPrinter string
	var force bool
	command := &cobra.Command{
		Use:   "stage-mainboard",
		Short: "Validate and stage qd_mcu.bin onto a FAT32 microSD",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if forPrinter != "" {
				printer, err := a.onePrinter(forPrinter)
				if err != nil {
					return err
				}
				if image == "" {
					image = imagePath(printer, migrate.MainboardImage)
				}
				if outputDir == "" {
					outputDir = printer.Migration.SDCard
				}
			}
			if image == "" {
				return fmt.Errorf(
					"--image is required, or pass --for with defaults.migration.artifactDir set")
			}
			if outputDir == "" {
				return fmt.Errorf(
					"--output is required, or pass --for with migration.sdcard set")
			}
			args := []string{"stage-mainboard-sdcard", "--image", image, "--output", outputDir,
				"--artifacts", filepath.Dir(image)}
			if force {
				args = append(args, "--force")
			}
			return a.runWorkstationStage(cmd.Context(), forPrinter, "stage-mainboard", args...)
		},
	}
	command.Flags().StringVar(&image, "image", "",
		"mainboard Klipper image (default: from defaults.migration.artifactDir)")
	command.Flags().StringVar(&outputDir, "output", "",
		"microSD root or staging directory (default: migration.sdcard)")
	command.Flags().StringVar(&forPrinter, "for", "", "record this stage in a printer's migration journal")
	command.Flags().BoolVar(&force, "force", false, "overwrite existing qd_mcu.bin")
	return command
}

func newMainlineRenderConfigCmd(a *app) *cobra.Command {
	var profile, profileDir, beaconSerial, outputPath, forPrinter string
	var force, commissioning bool
	command := &cobra.Command{
		Use:   "render-config",
		Short: "Generate the mainline printer configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("profile-dir") && profileDir == "" {
				return fmt.Errorf("--profile-dir must not be empty")
			}
			if forPrinter != "" {
				printer, err := a.onePrinter(forPrinter)
				if err != nil {
					return err
				}
				if profileDir == "" && !cmd.Flags().Changed("profile") {
					profileDir = printer.Migration.ProfileDir
				}
				if profileDir == "" && profile == "" {
					profile = printer.Profile
				}
				if beaconSerial == "" {
					beaconSerial = printer.Migration.BeaconSerial
				}
				if outputPath == "" {
					outputPath = printer.Migration.ConfigOutput
				}
			}
			if profileDir == "" && profile == "" {
				profile = defaultConfigProfile
			}
			if beaconSerial == "" {
				return fmt.Errorf("--beacon-serial is required, or pass --for with " +
					"migration.beaconSerial set for that printer")
			}
			if outputPath == "" {
				return fmt.Errorf("--output is required, or pass --for with " +
					"defaults.migration.configOutput set")
			}
			args := []string{"render-config"}
			if commissioning {
				args = append(args, "--commissioning")
			}
			args = append(args, renderConfigSourceArgs(profile, profileDir)...)
			args = append(args,
				"--beacon-serial", beaconSerial,
				"--output", outputPath,
			)
			if force {
				args = append(args, "--force")
			}
			return a.runWorkstationStage(cmd.Context(), forPrinter, "render-config", args...)
		},
	}
	command.Flags().StringVar(&forPrinter, "for", "",
		"resolve settings from this printer and record the stage in its journal")
	command.Flags().StringVar(&profile, "profile", "",
		"configuration profile (default: the printer's profile, else "+defaultConfigProfile+")")
	command.Flags().StringVar(&profileDir, "profile-dir", "",
		"exact external profile directory (default: the printer's migration.profileDir)")
	command.Flags().StringVar(&beaconSerial, "beacon-serial", "",
		"Beacon serial path (default: the printer's migration.beaconSerial)")
	command.Flags().StringVar(&outputPath, "output", "",
		"output directory (default: the printer's migration.configOutput)")
	command.Flags().BoolVar(&force, "force", false, "overwrite an existing rendered config")
	command.Flags().BoolVar(&commissioning, "commissioning", false,
		"enable only hardware/Beacon with conservative uncalibrated defaults")
	command.MarkFlagsMutuallyExclusive("profile", "profile-dir")
	return command
}

func renderConfigSourceArgs(profile, profileDir string) []string {
	if profileDir != "" {
		return []string{"--profile-dir", profileDir}
	}
	if profile == "" {
		profile = defaultConfigProfile
	}
	return []string{"--profile", profile}
}

func (a *app) ensureIdle(
	ctx context.Context,
	printer inventory.Printer,
	allowUnverified bool,
) error {
	client, err := moonraker.New(printer, a.timeout())
	if err == nil {
		err = client.EnsureIdle(ctx)
	}
	if err == nil {
		return nil
	}
	if allowUnverified && client != nil && client.IdleUnavailable(ctx, err) {
		_, _ = fmt.Fprintf(
			a.errOut,
			"WARNING: Moonraker idle state could not be verified for %s: %v\n",
			printer.Name,
			err,
		)
		return nil
	}
	return fmt.Errorf(
		"%s: %w; use --allow-unverified-idle only where the command exposes it "+
			"and you have physically confirmed the printer is idle",
		printer.Name,
		err,
	)
}

// A fresh replacement image cannot report print_stats yet. Resume can ask for
// physical confirmation at that transition, but -y and non-interactive mode
// never provide it and authoritative refusal remains final.
func (a *app) ensureTransitionIdle(ctx context.Context, printer inventory.Printer, interactive bool) error {
	client, err := moonraker.New(printer, a.timeout())
	if err != nil {
		return err
	}
	err = client.EnsureIdle(ctx)
	if err == nil {
		return nil
	}
	if !interactive || !client.IdleUnavailable(ctx, err) {
		return err
	}
	if confirmErr := a.acknowledge(fmt.Sprintf("\n%s cannot report idle while its host is unavailable. Confirm physically that the printer is idle and heaters are off.", printer.Name)); confirmErr != nil {
		return fmt.Errorf("physical idle confirmation was not given: %w", err)
	}
	return nil
}
