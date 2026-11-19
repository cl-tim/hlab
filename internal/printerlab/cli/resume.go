package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/probe"
	"github.com/cl-tim/hlab/internal/printerlab/stages"
)

// stageRunner executes a stage for one printer. Missing lists unresolved settings;
// Run is nil in that case. Detail lists arguments for operator review.
type stageRunner struct {
	Run     func(ctx context.Context) error
	Missing []string
	Detail  []string
}

// requirePath validates that a configured path exists and has the required type.
func requirePath(setting, value string, directory bool) string {
	if value == "" {
		return setting + " is not set in the inventory"
	}
	info, err := os.Stat(value)
	if err != nil {
		return fmt.Sprintf("%s not found at %s", setting, value)
	}
	if directory != info.IsDir() {
		want := "a file"
		if directory {
			want = "a directory"
		}
		return fmt.Sprintf("%s at %s is not %s", setting, value, want)
	}
	return ""
}

func requireProfileDir(value string) string {
	if problem := requirePath("migration.profileDir", value, true); problem != "" {
		return problem
	}
	entries, err := os.ReadDir(value)
	if err != nil {
		return fmt.Sprintf("cannot read migration.profileDir at %s: %v", value, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".cfg" {
			return ""
		}
	}
	return fmt.Sprintf("migration.profileDir at %s has no top-level .cfg files", value)
}

func imagePath(printer inventory.Printer, name string) string {
	if printer.Migration.ArtifactDir == "" {
		return ""
	}
	return filepath.Join(printer.Migration.ArtifactDir, name)
}

func migrationRefs(m inventory.Migration) (string, string, string) {
	k, moon, kat := m.KlipperRef, m.MoonrakerRef, m.KatapultRef
	if k == "" {
		k = migrate.KlipperRef
	}
	if moon == "" {
		moon = migrate.MoonrakerRef
	}
	if kat == "" {
		kat = migrate.KatapultRef
	}
	return k, moon, kat
}

// resumeExecutor resolves a stage's arguments from the inventory.
// Unresolved settings are returned in Missing.
func (a *app) resumeExecutor(stage stages.Stage, printer inventory.Printer) stageRunner {
	return a.resumeExecutorWithIdle(stage, printer, false)
}

func (a *app) resumeExecutorWithIdle(stage stages.Stage, printer inventory.Printer, interactive bool) stageRunner {
	remote := func(extra ...string) []string {
		return append(append([]string{stage.Impl},
			migrate.ConnectionArgs(printer)...), extra...)
	}
	bootstrap := func(extra ...string) []string {
		return append(append([]string{stage.Impl},
			migrate.BootstrapConnectionArgs(printer)...), extra...)
	}
	direct := func(args []string, detail ...string) stageRunner {
		return stageRunner{Detail: detail, Run: func(ctx context.Context) error {
			return a.runStage(ctx, printer, stage.Impl, args...)
		}}
	}
	gated := func(args []string, detail ...string) stageRunner {
		return stageRunner{Detail: detail, Run: func(ctx context.Context) error {
			if err := a.ensureTransitionIdle(ctx, printer, interactive); err != nil {
				return err
			}
			return a.runStage(ctx, printer, stage.Impl, args...)
		}}
	}

	switch stage.ID {
	case "setup-ssh":
		return direct(bootstrap())
	case "audit":
		return direct(remote())
	case "backup":
		return direct(remote("--name", printer.Name))
	case "bootstrap-host":
		k, moon, kat := migrationRefs(printer.Migration)
		args := remote("--confirm", "BOOTSTRAP-HOST",
			"--klipper-ref", k, "--moonraker-ref", moon,
			"--katapult-ref", kat, "--artifacts", printer.Migration.ArtifactDir)
		if printer.Migration.BeaconRef != "" {
			args = append(args, "--beacon-ref", printer.Migration.BeaconRef)
		}
		return gated(args,
			"--klipper-ref "+k, "--moonraker-ref "+moon, "--katapult-ref "+kat)
	case "tune-host":
		return gated(remote("--confirm", "TUNE-HOST"))
	case "smoke-test":
		owner := printer.PrinterUser
		if owner == "" {
			owner = printer.User
		}
		if owner == "" {
			owner = "mks"
		}
		return gated(remote("--data-dir", "/home/"+owner+"/printer_data"))

	case "build":
		defaults := a.migrationDefaults()
		var missing []string
		for _, required := range []struct{ setting, value string }{
			{"migration.klipperDir", defaults.KlipperDir},
			{"migration.katapultDir", defaults.KatapultDir},
		} {
			if defaults.WSLDistro != "" {
				if required.value == "" {
					missing = append(missing, required.setting+" is not set")
				}
				continue // Linux paths are validated by the utility inside WSL.
			}
			if problem := requirePath(required.setting, required.value, true); problem != "" {
				missing = append(missing, problem)
			}
		}
		if len(missing) > 0 {
			return stageRunner{Missing: missing}
		}
		args := []string{"build", "--klipper-dir", defaults.KlipperDir,
			"--katapult-dir", defaults.KatapultDir}
		k, _, kat := migrationRefs(printer.Migration)
		args = append(args, "--klipper-ref", k, "--katapult-ref", kat)
		if defaults.WSLDistro != "" {
			args = append(args, "--wsl-distro", defaults.WSLDistro)
		}
		detail := []string{"--klipper-dir " + defaults.KlipperDir,
			"--katapult-dir " + defaults.KatapultDir}
		if printer.Migration.ArtifactDir != "" {
			args = append(args, "--output", printer.Migration.ArtifactDir)
			detail = append(detail, "--output "+printer.Migration.ArtifactDir)
		}
		return direct(args, detail...)

	case "stage-mainboard":
		image := imagePath(printer, migrate.MainboardImage)
		var missing []string
		if problem := requirePath("built "+migrate.MainboardImage, image, false); problem != "" {
			missing = append(missing, problem)
		}
		if printer.Migration.SDCard == "" {
			missing = append(missing, "migration.sdcard is not set in the inventory")
		}
		if len(missing) > 0 {
			return stageRunner{Missing: missing}
		}
		return direct(
			[]string{"stage-mainboard-sdcard", "--image", image,
				"--output", printer.Migration.SDCard, "--artifacts", printer.Migration.ArtifactDir},
			"--image "+image, "--output "+printer.Migration.SDCard)

	case "flash-toolhead-katapult":
		return a.flashRunner(stage, printer, migrate.ToolheadKatapultImage, "ERASE-TOOLHEAD", interactive)
	case "flash-toolhead":
		return a.flashRunner(stage, printer, migrate.ToolheadImage, "FLASH-TOOLHEAD", interactive)

	case "render-config":
		var missing []string
		if printer.Migration.BeaconSerial == "" {
			missing = append(missing,
				"migration.beaconSerial is not set for this printer")
		}
		if printer.Migration.ConfigOutput == "" {
			missing = append(missing, "migration.configOutput is not set in the inventory")
		}
		if printer.Migration.ProfileDir != "" {
			if problem := requireProfileDir(printer.Migration.ProfileDir); problem != "" {
				missing = append(missing, problem)
			}
		}
		if len(missing) > 0 {
			return stageRunner{Missing: missing}
		}
		profile := printer.Profile
		if printer.Migration.ProfileDir == "" && profile == "" {
			profile = defaultConfigProfile
		}
		args := []string{"render-config", "--commissioning"}
		args = append(args, renderConfigSourceArgs(profile, printer.Migration.ProfileDir)...)
		args = append(args,
			"--beacon-serial", printer.Migration.BeaconSerial,
			"--output", printer.Migration.ConfigOutput,
		)
		detail := renderConfigSourceArgs(profile, printer.Migration.ProfileDir)
		detail = append(detail,
			"--beacon-serial", printer.Migration.BeaconSerial,
			"--output", printer.Migration.ConfigOutput,
		)
		return direct(
			args,
			joinFlagDetails(detail)...)

	default:
		return stageRunner{}
	}
}

func joinFlagDetails(args []string) []string {
	detail := make([]string, 0, len(args)/2)
	for index := 0; index+1 < len(args); index += 2 {
		detail = append(detail, args[index]+" "+args[index+1])
	}
	return detail
}

func (a *app) flashRunner(
	stage stages.Stage,
	printer inventory.Printer,
	name, token string,
	interactive bool,
) stageRunner {
	image := imagePath(printer, name)
	if problem := requirePath("built "+name, image, false); problem != "" {
		return stageRunner{Missing: []string{problem}}
	}
	args := append([]string{stage.Impl}, migrate.ConnectionArgs(printer)...)
	args = append(args, "--image", image, "--confirm", token)
	args = append(args, "--artifacts", printer.Migration.ArtifactDir)
	return stageRunner{
		Detail: []string{"--image " + image},
		Run: func(ctx context.Context) error {
			if err := a.ensureTransitionIdle(ctx, printer, interactive); err != nil {
				return err
			}
			return a.runStage(ctx, printer, stage.Impl, args...)
		},
	}
}

type resumeSummary struct {
	Printer  string   `json:"printer" yaml:"printer"`
	Complete bool     `json:"complete" yaml:"complete"`
	Executed []string `json:"executed" yaml:"executed"`
	Halted   string   `json:"halted,omitempty" yaml:"halted,omitempty"`
	Reason   string   `json:"reason" yaml:"reason"`
	Command  string   `json:"command,omitempty" yaml:"command,omitempty"`
	// Prompts lists stages requiring interaction in a dry run.
	Prompts []string `json:"prompts,omitempty" yaml:"prompts,omitempty"`
}

func newMainlineResumeCmd(a *app) *cobra.Command {
	var dryRun, nonInteractive bool
	command := &cobra.Command{
		Use:   "resume PRINTER",
		Short: "Continue migration from the current printer state",
		Long: "Check the migration journal and live printer state, then run consecutive stages.\n" +
			"Each stage checks its prerequisites and prompts for physical work or approval.\n" +
			"Migration stops when a check fails or required settings are missing.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if dryRun {
				return a.resumeDryRun(cmd.Context(), printer)
			}
			return a.resumeRun(cmd.Context(), printer, a.yes, nonInteractive)
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false,
		"show the stages resume would attempt without running anything")
	command.Flags().BoolVar(&nonInteractive, "non-interactive", false,
		"halt instead of prompting; pass -y/--yes to approve destructive stages")
	return command
}

func (a *app) resumeRun(
	ctx context.Context,
	printer inventory.Printer,
	approved bool,
	nonInteractive bool,
) error {
	graph := stages.Graph()
	summary := resumeSummary{Printer: printer.Name, Executed: []string{}}
	ran := map[string]bool{}

	// One iteration per stage is the most a correct run can need; the bound is
	// a backstop against a stage that never changes what the probes report.
	for range graph {
		report, err := a.reconcile(ctx, printer)
		if err != nil {
			return err
		}
		if commissioned(report) {
			summary.Complete = true
			summary.Reason = "mainline config and both MCUs are ready; historical unrecorded stages do not require repeating"
			return a.writeResumeSummary(summary, false)
		}
		decision, found := stages.Next(report.Stages)
		if !found {
			summary.Complete = true
			summary.Reason = "every stage is satisfied"
			return a.writeResumeSummary(summary, false)
		}
		if decision.Action == stages.ActionStop {
			summary.Halted, summary.Reason = decision.Stage, decision.Reason
			return a.writeResumeSummary(summary, true)
		}

		stage, ok := stages.Find(graph, decision.Stage)
		if !ok {
			return fmt.Errorf("unknown stage %q in reconciliation", decision.Stage)
		}
		if a.guidedFirmware {
			if err := a.guideFirmwareStage(ctx, &stage, &printer); err != nil {
				return err
			}
		}
		executor := a.resumeExecutorWithIdle(stage, printer, !nonInteractive)
		if a.guidedFirmware && stage.ID == "install-config" {
			executor = a.installFirmwareConfig(printer)
		}
		if a.guidedFirmware && stage.ID == "bootstrap-host" && executor.Run != nil {
			run := executor.Run
			executor.Run = func(ctx context.Context) error {
				if err := a.setupFirmwareSSH(ctx, printer); err != nil {
					return err
				}
				if err := run(ctx); err != nil {
					return err
				}
				return a.firmwareAPIKey(ctx, &printer, true)
			}
		}
		if executor.Run == nil {
			summary.Halted = stage.ID
			summary.Reason = cannotRunReason(stage, executor)
			summary.Command = stageCommand(stage, printer)
			return a.writeResumeSummary(summary, true)
		}
		// A stage that ran but left the probes unchanged would otherwise be
		// selected again forever.
		if ran[stage.ID] {
			summary.Halted = stage.ID
			summary.Reason = "stage ran but the printer's state did not change; " +
				"it may have failed silently"
			return a.writeResumeSummary(summary, true)
		}

		a.describeStage(stage, executor)
		if reason := a.authorizeStage(stage, approved, nonInteractive); reason != "" {
			summary.Halted, summary.Reason = stage.ID, reason
			summary.Command = stageCommand(stage, printer)
			return a.writeResumeSummary(summary, true)
		}

		if err := executor.Run(ctx); err != nil {
			summary.Halted = stage.ID
			summary.Reason = "stage failed: " + err.Error()
			_ = a.writeResumeSummary(summary, false)
			return err
		}
		ran[stage.ID] = true
		summary.Executed = append(summary.Executed, stage.ID)
	}

	summary.Reason = "stopped after one pass over the sequence without converging"
	return a.writeResumeSummary(summary, true)
}

// describeStage shows the resolved arguments before anything is authorized, so
// a stale inventory path is visible rather than discovered afterwards.
func (a *app) describeStage(stage stages.Stage, executor stageRunner) {
	_, _ = fmt.Fprintf(a.errOut, "\n==> %s: %s\n", stage.ID, stage.Summary)
	for _, detail := range executor.Detail {
		_, _ = fmt.Fprintf(a.errOut, "    %s\n", detail)
	}
}

// promptsFor names what a stage will ask a person for, if anything.
func promptsFor(stage stages.Stage) string {
	var needs []string
	if stage.Manual != "" {
		needs = append(needs, "physical work")
	}
	if stage.Confirm != "" {
		needs = append(needs, "approval")
	}
	return strings.Join(needs, " + ")
}

func cannotRunReason(stage stages.Stage, executor stageRunner) string {
	if stage.Impl == "" {
		return "this stage is performed by the operator; plab cannot run it"
	}
	if len(executor.Missing) == 0 {
		return "resume has no executor for this stage"
	}
	return "resume cannot supply required arguments: " + strings.Join(executor.Missing, "; ")
}

func stageCommand(stage stages.Stage, printer inventory.Printer) string {
	if stage.Impl == "" {
		return ""
	}
	command := "hlab printer firmware advanced " + stage.Impl
	switch stage.ID {
	case "build", "stage-mainboard", "render-config":
		command += " --for " + printer.Name
	default:
		command += " " + printer.Name
	}
	if stage.Confirm != "" {
		command += " -y"
	}
	return command
}

// authorizeStage returns a halt reason or permits execution.
// Physical prerequisites require an interactive acknowledgement.
func (a *app) authorizeStage(
	stage stages.Stage,
	approved bool,
	nonInteractive bool,
) string {
	if stage.Manual != "" {
		if nonInteractive {
			return "stage needs physical work first: " + stage.Manual
		}
		_, _ = fmt.Fprintf(a.errOut, "\n%s requires physical work first:\n  %s\n",
			stage.ID, stage.Manual)
		_, _ = fmt.Fprint(a.errOut, "Prerequisites complete; proceed? [y/N]: ")
		answer, err := readLine(a.in)
		if err != nil || !affirmative(answer) {
			return "physical prerequisite was not acknowledged: " + stage.Manual
		}
		if a.guidedFirmware {
			return ""
		} // One prompt covers the physical step and this operation.
	}
	if stage.Confirm != "" && (!approved || a.guidedFirmware) {
		if nonInteractive {
			return "destructive stage needs -y/--yes"
		}
		_, _ = fmt.Fprintf(a.errOut,
			"\n%s is destructive. Proceed? [y/N]: ", stage.ID)
		answer, err := readLine(a.in)
		if err != nil || !affirmative(answer) {
			return "approval was not given"
		}
	}
	return ""
}

// resumeDryRun checks the printer once, then simulates successful stages in memory.
func (a *app) resumeDryRun(ctx context.Context, printer inventory.Printer) error {
	graph := stages.Graph()
	report, err := a.reconcile(ctx, printer)
	if err != nil {
		return err
	}
	if commissioned(report) {
		return a.writeResumeSummary(resumeSummary{Printer: printer.Name, Complete: true,
			Executed: []string{}, Reason: "mainline config and both MCUs are ready; historical unrecorded stages do not require repeating"}, false)
	}
	attempts := map[string]stages.Attempt{}
	for _, decision := range report.Stages {
		attempts[decision.Stage] = decision.Attempt
	}
	probes := report.statuses()

	summary := resumeSummary{Printer: printer.Name, Executed: []string{}}
	for range graph {
		decisions := stages.Reconcile(graph, stages.State{Attempts: attempts, Probes: probes})
		decision, found := stages.Next(decisions)
		if !found {
			summary.Complete = true
			summary.Reason = "every stage is satisfied"
			break
		}
		if decision.Action == stages.ActionStop {
			summary.Halted, summary.Reason = decision.Stage, decision.Reason
			break
		}
		stage, _ := stages.Find(graph, decision.Stage)
		executor := a.resumeExecutor(stage, printer)
		if a.guidedFirmware {
			if stage.ID == "install-config" {
				executor = a.installFirmwareConfig(printer)
			}
			if stage.ID == "stage-mainboard" && len(executor.Missing) == 1 && executor.Missing[0] == "migration.sdcard is not set in the inventory" {
				executor = stageRunner{Run: func(context.Context) error { return nil }} // Guided run asks for the card.
			}
		}
		if executor.Run == nil {
			summary.Halted = stage.ID
			summary.Reason = cannotRunReason(stage, executor)
			summary.Command = stageCommand(stage, printer)
			break
		}
		summary.Executed = append(summary.Executed, stage.ID)
		needs := promptsFor(stage)
		if a.guidedFirmware && stage.ID == "stage-mainboard" {
			needs = "verified Armbian spare eMMC or interactive write-emmc; microSD write approval"
		}
		if needs != "" {
			summary.Prompts = append(summary.Prompts, stage.ID+" ("+needs+")")
		}
		// Simulate success: the attempt is recorded and the stage's own probes
		// start reporting satisfied. Without the second half, the next pass
		// would read "completed but unsatisfied" as a regression.
		attempts[stage.ID] = stages.AttemptCompleted
		for _, id := range stage.Verify {
			probes[id] = probe.Satisfied
		}
	}
	if summary.Reason == "" {
		summary.Reason = "stopped after one pass over the sequence without converging"
	}
	return a.writeResumeSummary(summary, false)
}

func commissioned(report engineReport) bool {
	statuses := report.statuses()
	return statuses["config.installed"] == probe.Satisfied &&
		statuses["mcu.online"] == probe.Satisfied && statuses["bootstrap.receipt"] == probe.Satisfied
}

func (a *app) writeResumeSummary(summary resumeSummary, halted bool) error {
	table := output.Table{Headers: []string{"FIELD", "VALUE"}}
	table.AddRow("printer", summary.Printer)
	if len(summary.Executed) > 0 {
		table.AddRow("executed", strings.Join(summary.Executed, ", "))
	} else {
		table.AddRow("executed", "(nothing)")
	}
	for index, prompt := range summary.Prompts {
		label := ""
		if index == 0 {
			label = "will prompt for"
		}
		table.AddRow(label, prompt)
	}
	if summary.Halted != "" {
		table.AddRow("halted at", summary.Halted)
	}
	table.AddRow("reason", summary.Reason)
	if summary.Command != "" {
		table.AddRow("continue with", summary.Command)
	}
	if err := output.Write(a.out, a.format, summary, table); err != nil {
		return err
	}
	if halted {
		return &ExitError{Code: 1}
	}
	return nil
}

// readLine reads one byte at a time to leave SSH password input for child processes.
func readLine(reader io.Reader) (string, error) {
	var line []byte
	single := make([]byte, 1)
	for {
		read, err := reader.Read(single)
		if read > 0 {
			if single[0] == '\n' {
				break
			}
			if single[0] != '\r' {
				line = append(line, single[0])
			}
		}
		if err != nil {
			if len(line) > 0 {
				break
			}
			return "", err
		}
	}
	return strings.TrimSpace(string(line)), nil
}
