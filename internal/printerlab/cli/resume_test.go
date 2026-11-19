package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/printerlab/config"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
	"github.com/cl-tim/homelab-cli/internal/printerlab/stages"
)

func promptApp(input string) *app {
	return &app{in: strings.NewReader(input), out: io.Discard, errOut: io.Discard}
}

// configuredApp builds an inventory whose migration block resolves every stage,
// backed by artifacts that actually exist on disk.
func configuredApp(t *testing.T) (*app, inventory.Printer) {
	t.Helper()
	root := t.TempDir()
	artifacts := filepath.Join(root, "artifacts")
	klipper := filepath.Join(root, "klipper")
	katapult := filepath.Join(root, "katapult")
	for _, directory := range []string{artifacts, klipper, katapult} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	for _, name := range []string{
		migrate.MainboardImage, migrate.ToolheadImage, migrate.ToolheadKatapultImage,
	} {
		if err := os.WriteFile(filepath.Join(artifacts, name), []byte("bin"), 0o600); err != nil {
			t.Fatalf("write image: %v", err)
		}
	}
	a := &app{in: strings.NewReader(""), out: io.Discard, errOut: io.Discard,
		cfg: &config.Config{Defaults: config.Defaults{
			Migration: config.MigrationDefaults{
				KlipperDir: klipper, KatapultDir: katapult, ArtifactDir: artifacts,
			},
		}}}
	printer := inventory.Printer{
		Name: "printer-a", Address: "plus4.example", User: "mks", Port: 22,
		Model: "plus4", Profile: "plus4-x7-v1.0-beacon",
		Migration: inventory.Migration{
			BeaconSerial: "/dev/serial/by-id/usb-Beacon_Beacon_RevH_ABC-if00",
			ArtifactDir:  artifacts,
			ConfigOutput: filepath.Join(root, "generated", "printer-a"),
			SDCard:       filepath.Join(root, "sdcard"),
		},
	}
	return a, printer
}

func stageByID(t *testing.T, id string) stages.Stage {
	t.Helper()
	stage, ok := stages.Find(stages.Graph(), id)
	if !ok {
		t.Fatalf("no stage %q in the graph", id)
	}
	return stage
}

// --------------------------------------------------------------------------
// Executor coverage.
// --------------------------------------------------------------------------

// A stage added to the graph without a matching executor case would fall
// through to the empty runner and halt with an empty list of missing
// arguments, which reads as a bug rather than as a limitation.
func TestEveryStageIsEitherExecutableOrExplainsWhatItLacks(t *testing.T) {
	bare := &app{}
	configured, printer := configuredApp(t)
	for _, stage := range stages.Graph() {
		for _, subject := range []struct {
			name    string
			a       *app
			printer inventory.Printer
		}{
			{"unconfigured", bare, inventory.Printer{Name: "printer-a"}},
			{"configured", configured, printer},
		} {
			executor := subject.a.resumeExecutor(stage, subject.printer)
			reason := cannotRunReason(stage, executor)
			switch {
			case executor.Run != nil:
				if len(executor.Missing) > 0 {
					t.Errorf("%s/%s: runnable stages must not also report missing arguments",
						stage.ID, subject.name)
				}
			case stage.Impl == "":
				if !strings.Contains(reason, "operator") {
					t.Errorf("%s/%s: got %q, want a reason naming the operator",
						stage.ID, subject.name, reason)
				}
			case len(executor.Missing) == 0:
				t.Errorf("%s/%s: not executable and does not say what it lacks",
					stage.ID, subject.name)
			}
		}
	}
}

// With the migration block filled in, every stage plab implements can run.
func TestConfiguredInventoryMakesEveryImplementedStageExecutable(t *testing.T) {
	a, printer := configuredApp(t)

	for _, stage := range stages.Graph() {
		if stage.Impl == "" {
			continue
		}
		executor := a.resumeExecutor(stage, printer)
		if executor.Run == nil {
			t.Errorf("%s: not executable with a complete inventory: %s",
				stage.ID, cannotRunReason(stage, executor))
		}
	}
}

func TestOperatorPerformedStageIsNeverExecutable(t *testing.T) {
	a, printer := configuredApp(t)
	stage := stageByID(t, "install-config")

	executor := a.resumeExecutor(stage, printer)

	if executor.Run != nil {
		t.Fatal("install-config has no plab implementation and must not be runnable")
	}
	if reason := cannotRunReason(stage, executor); !strings.Contains(reason, "operator") {
		t.Fatalf("got %q, want a reason naming the operator", reason)
	}
}

// A configured artifact directory whose image is absent must read as "cannot
// supply", not as a stage that starts and then fails against a missing file.
func TestConfiguredButAbsentImageIsNotRunnable(t *testing.T) {
	a, printer := configuredApp(t)
	if err := os.Remove(filepath.Join(printer.Migration.ArtifactDir, migrate.ToolheadImage)); err != nil {
		t.Fatalf("remove image: %v", err)
	}

	executor := a.resumeExecutor(stageByID(t, "flash-toolhead"), printer)

	if executor.Run != nil {
		t.Fatal("a missing firmware image must not be runnable")
	}
	if reason := cannotRunReason(stageByID(t, "flash-toolhead"), executor); !strings.Contains(reason, "not found") {
		t.Fatalf("got %q, want the absent path named", reason)
	}
}

func TestMissingBeaconSerialBlocksRenderConfig(t *testing.T) {
	a, printer := configuredApp(t)
	printer.Migration.BeaconSerial = ""

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)

	if executor.Run != nil {
		t.Fatal("render-config must not run without a Beacon serial")
	}
	if reason := cannotRunReason(stageByID(t, "render-config"), executor); !strings.Contains(reason, "beaconSerial") {
		t.Fatalf("got %q, want beaconSerial named", reason)
	}
}

func TestMissingArgumentsAreNamedInTheReason(t *testing.T) {
	a := &app{}
	stage := stageByID(t, "build")

	reason := cannotRunReason(stage, a.resumeExecutor(stage, inventory.Printer{}))

	for _, want := range []string{"klipperDir", "katapultDir"} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason %q does not mention %s", reason, want)
		}
	}
}

// The resolved arguments are shown before anything destructive is authorized,
// so a stale inventory path is visible rather than discovered afterwards.
func TestResolvedArgumentsAreReportedForReview(t *testing.T) {
	a, printer := configuredApp(t)

	executor := a.resumeExecutor(stageByID(t, "flash-toolhead-katapult"), printer)

	joined := strings.Join(executor.Detail, " ")
	if !strings.Contains(joined, migrate.ToolheadKatapultImage) {
		t.Fatalf("got %q, want the resolved image named", joined)
	}
}

func TestRenderConfigUsesThePrinterProfile(t *testing.T) {
	a, printer := configuredApp(t)
	printer.Profile = "custom-profile"

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)

	if !strings.Contains(strings.Join(executor.Detail, " "), "custom-profile") {
		t.Fatalf("got %q, want the printer's own profile", executor.Detail)
	}
}

func TestRenderConfigFallsBackToTheReviewedProfile(t *testing.T) {
	a, printer := configuredApp(t)
	printer.Profile = ""

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)

	if !strings.Contains(strings.Join(executor.Detail, " "), defaultConfigProfile) {
		t.Fatalf("got %q, want the default profile", executor.Detail)
	}
}

func TestRenderConfigUsesExternalProfileDirectory(t *testing.T) {
	a, printer := configuredApp(t)
	profileDir := filepath.Join(t.TempDir(), "qidi-plus4-configs", "printers", "mainline-klipper-beacon")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "printer.cfg"), []byte("[printer]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	printer.Migration.ProfileDir = profileDir

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)

	if executor.Run == nil {
		t.Fatalf("external profile should be runnable: %v", executor.Missing)
	}
	want := "--profile-dir " + profileDir
	if len(executor.Detail) == 0 || executor.Detail[0] != want {
		t.Fatalf("got %q, want first detail %q", executor.Detail, want)
	}
	for _, detail := range executor.Detail {
		if strings.HasPrefix(detail, "--profile ") {
			t.Fatalf("external profile also supplied the bundled profile: %q", executor.Detail)
		}
	}
}

func TestMissingExternalProfileDirectoryBlocksRenderConfig(t *testing.T) {
	a, printer := configuredApp(t)
	printer.Migration.ProfileDir = filepath.Join(t.TempDir(), "missing-profile")

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)

	if executor.Run != nil {
		t.Fatal("render-config must not start with a missing external profile directory")
	}
	reason := cannotRunReason(stageByID(t, "render-config"), executor)
	if !strings.Contains(reason, "migration.profileDir") || !strings.Contains(reason, "not found") {
		t.Fatalf("got %q, want the missing profile directory named", reason)
	}
}

func TestEmptyExternalProfileDirectoryBlocksRenderConfig(t *testing.T) {
	a, printer := configuredApp(t)
	printer.Migration.ProfileDir = t.TempDir()

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)

	if executor.Run != nil {
		t.Fatal("render-config must not start with an empty external profile directory")
	}
	reason := cannotRunReason(stageByID(t, "render-config"), executor)
	if !strings.Contains(reason, "no top-level .cfg files") {
		t.Fatalf("got %q, want the empty profile directory explained", reason)
	}
}

func TestProfileDirectoryFileBlocksRenderConfig(t *testing.T) {
	a, printer := configuredApp(t)
	profileFile := filepath.Join(t.TempDir(), "profile.cfg")
	if err := os.WriteFile(profileFile, []byte("[printer]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	printer.Migration.ProfileDir = profileFile

	executor := a.resumeExecutor(stageByID(t, "render-config"), printer)

	if executor.Run != nil {
		t.Fatal("render-config must not treat a regular file as a profile directory")
	}
	reason := cannotRunReason(stageByID(t, "render-config"), executor)
	if !strings.Contains(reason, "not a directory") {
		t.Fatalf("got %q, want the wrong path kind explained", reason)
	}
}

func TestRenderConfigSourceArgumentsPreferExternalDirectory(t *testing.T) {
	tests := []struct {
		name       string
		profile    string
		profileDir string
		want       []string
	}{
		{name: "bundled profile", profile: "compatibility-profile",
			want: []string{"--profile", "compatibility-profile"}},
		{name: "external directory", profile: "ignored-compatibility-profile",
			profileDir: `C:\src\qidi-plus4-configs\printers\mainline-klipper-beacon`,
			want:       []string{"--profile-dir", `C:\src\qidi-plus4-configs\printers\mainline-klipper-beacon`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := renderConfigSourceArgs(test.profile, test.profileDir)
			if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestStageCommandUsesForOnWorkstationStages(t *testing.T) {
	printer := inventory.Printer{Name: "printer-a"}

	if got := stageCommand(stageByID(t, "build"), printer); !strings.Contains(got, "--for printer-a") {
		t.Errorf("got %q, want --for on a workstation stage", got)
	}
	got := stageCommand(stageByID(t, "flash-toolhead"), printer)
	if !strings.Contains(got, "flash-toolhead printer-a") {
		t.Errorf("got %q, want a positional printer", got)
	}
	if !strings.Contains(got, " -y") {
		t.Errorf("got %q, want the approval flag", got)
	}
}

func TestStageCommandIsEmptyForOperatorStages(t *testing.T) {
	if got := stageCommand(stageByID(t, "install-config"), inventory.Printer{Name: "p"}); got != "" {
		t.Fatalf("got %q, want no command for an operator-performed stage", got)
	}
}

// --------------------------------------------------------------------------
// Authorization.
// --------------------------------------------------------------------------

func TestUnconfirmedDestructiveStageHalts(t *testing.T) {
	a := promptApp("")
	stage := stageByID(t, "tune-host")

	reason := a.authorizeStage(stage, false, true)

	if !strings.Contains(reason, "-y/--yes") {
		t.Fatalf("got %q, want a halt naming the approval flag", reason)
	}
}

func TestPreApprovedStageProceeds(t *testing.T) {
	a := promptApp("")
	stage := stageByID(t, "tune-host")

	if reason := a.authorizeStage(stage, true, true); reason != "" {
		t.Fatalf("got %q, want the stage to proceed", reason)
	}
}

func TestInteractiveYesAuthorizes(t *testing.T) {
	a := promptApp("yes\n")
	stage := stageByID(t, "tune-host")

	if reason := a.authorizeStage(stage, false, false); reason != "" {
		t.Fatalf("got %q, want yes to authorize", reason)
	}
}

func TestInteractiveNoIsRejected(t *testing.T) {
	a := promptApp("no\n")
	stage := stageByID(t, "tune-host")

	if reason := a.authorizeStage(stage, false, false); reason == "" {
		t.Fatal("no must not approve the stage")
	}
}

// No flag can prove an ST-Link is attached, so physical prerequisites are
// never pre-authorizable and always need an answer in the moment.
func TestPhysicalPrerequisiteIsNotPreAuthorizable(t *testing.T) {
	a := promptApp("")
	stage := stageByID(t, "bootstrap-host")

	reason := a.authorizeStage(stage, true, true)

	if !strings.Contains(reason, "physical") {
		t.Fatalf("got %q, want a halt on the physical prerequisite", reason)
	}
}

func TestPhysicalAcknowledgementThenApprovalProceeds(t *testing.T) {
	a := promptApp("yes\nyes\n")
	stage := stageByID(t, "bootstrap-host")

	if reason := a.authorizeStage(stage, false, false); reason != "" {
		t.Fatalf("got %q, want the stage to proceed", reason)
	}
}

func TestDeclinedPhysicalAcknowledgementHalts(t *testing.T) {
	a := promptApp("no\n")
	stage := stageByID(t, "bootstrap-host")

	if reason := a.authorizeStage(stage, false, false); reason == "" {
		t.Fatal("declining the physical step must halt")
	}
}

// Closed stdin must not read as consent.
func TestEndOfInputIsNotConsent(t *testing.T) {
	a := promptApp("")
	stage := stageByID(t, "bootstrap-host")

	if reason := a.authorizeStage(stage, false, false); reason == "" {
		t.Fatal("end of input must not authorize a destructive stage")
	}
}

func TestUngatedStageNeedsNoAuthorization(t *testing.T) {
	a := promptApp("")
	stage := stageByID(t, "audit")

	if reason := a.authorizeStage(stage, false, true); reason != "" {
		t.Fatalf("got %q, want audit to need no authorization", reason)
	}
}

// --------------------------------------------------------------------------
// Line reading, which shares stdin with the stage commands.
// --------------------------------------------------------------------------

func TestReadLineHandlesLineEndingsAndEOF(t *testing.T) {
	for input, want := range map[string]string{
		"yes\n":         "yes",
		"yes\r\n":       "yes",
		"  yes  \n":     "yes",
		"yes":           "yes",
		"yes\nsecond\n": "yes",
	} {
		got, err := readLine(strings.NewReader(input))
		if err != nil {
			t.Errorf("%q: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("%q: got %q, want %q", input, got, want)
		}
	}
}

// Reading must stop at the newline so a stage command can still read the rest
// of stdin for its own password prompt.
func TestReadLineLeavesTheRestOfStdin(t *testing.T) {
	reader := strings.NewReader("yes\nprinter-password\n")

	if _, err := readLine(reader); err != nil {
		t.Fatalf("readLine: %v", err)
	}

	remaining, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read remainder: %v", err)
	}
	if string(remaining) != "printer-password\n" {
		t.Fatalf("got %q, want the untouched remainder", string(remaining))
	}
}

func TestReadLineReportsEmptyInput(t *testing.T) {
	if _, err := readLine(strings.NewReader("")); err == nil {
		t.Fatal("expected an error on closed input")
	}
}
