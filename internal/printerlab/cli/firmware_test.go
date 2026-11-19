package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cl-tim/homelab-cli/internal/printerlab/config"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/journal"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
	"github.com/cl-tim/homelab-cli/internal/printerlab/probe"
	"github.com/cl-tim/homelab-cli/internal/printerlab/stages"
)

// Only the test executable enables these subprocess stubs. Rendering still
// uses the real embedded Go backend; no SSH, WSL build or download runs here.
func runFirmwareWorkflowHelper() bool {
	if os.Getenv("PLAB_FIRMWARE_WORKFLOW_HELPER") != "1" || len(os.Args) < 3 || os.Args[1] != "--internal-migration-backend" {
		return false
	}
	stage := os.Args[2]
	file, err := os.OpenFile(os.Getenv("PLAB_FIRMWARE_CALLS"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		os.Exit(2)
	}
	fmt.Fprintln(file, stage)
	file.Close()
	if os.Getenv("PLAB_FIRMWARE_PROGRESS_HELPER") == "1" {
		if stage == "probe" {
			book, err := journal.Load(journal.Path(config.Home(), "my-plus4"), "my-plus4", recipeName, stages.Digest(stages.Graph()))
			if err != nil {
				os.Exit(2)
			}
			completed := func(id string) bool {
				entry, ok := book.Entry(id)
				return ok && entry.Attempt == stages.AttemptCompleted
			}
			report := probe.Report{Schema: 1, Reachable: true}
			for id, satisfied := range map[string]bool{
				"bootstrap.receipt": completed("bootstrap-host"), "bootstrap.sources": completed("bootstrap-host"),
				"bootstrap.services": completed("bootstrap-host"), "bootstrap.uart-claimed": completed("bootstrap-host"),
				"host-tuning.installed": completed("bootstrap-host"), "config.smoke-test": completed("smoke-test"),
				"config.installed": completed("install-config"), "mcu.online": completed("smoke-test") || completed("install-config"),
			} {
				status := probe.Unsatisfied
				if satisfied {
					status = probe.Satisfied
				}
				report.Probes = append(report.Probes, probe.Result{Probe: id, Status: status})
			}
			json.NewEncoder(os.Stdout).Encode(report)
			return true
		}
		if stage == "stage-mainboard-sdcard" {
			value := func(flag string) string {
				for i := 3; i+1 < len(os.Args); i++ {
					if os.Args[i] == flag {
						return os.Args[i+1]
					}
				}
				return ""
			}
			raw, err := os.ReadFile(value("--image"))
			if err != nil {
				os.Exit(2)
			}
			if err := os.WriteFile(filepath.Join(value("--output"), "qd_mcu.CUR"), raw, 0600); err != nil {
				os.Exit(2)
			}
		}
	}
	switch stage {
	case "emmc-disks":
		json.NewEncoder(os.Stdout).Encode([]migrate.EMMCDisk{{Device: `\\.\PhysicalDrive3`, Number: 3, FriendlyName: "Fixture USB eMMC", SerialNumber: "FIXTURE-ONLY", Size: 8192}})
	case "emmc-plan":
		json.NewEncoder(os.Stdout).Encode(migrate.EMMCWritePlan{Schema: "printerlab/emmc-write/v1", Manifest: filepath.Join(config.Home(), "fixture-plan.json"), Image: "fixture.img.xz", ImageSHA256: strings.Repeat("1", 64), RawSHA256: strings.Repeat("2", 64), Bytes: 4096, Disk: migrate.EMMCDisk{Device: `\\.\PhysicalDrive3`, Number: 3, FriendlyName: "Fixture USB eMMC", SerialNumber: "FIXTURE-ONLY", Size: 8192}})
	case "render-config", "validate-render":
		return false
	case "beacon-devices":
		fmt.Println(`["/dev/serial/by-id/usb-Beacon_TEST-if00"]`)
	case "moonraker-api-key":
		fmt.Println(`{"apiKey":"0123456789abcdef0123456789abcdef"}`)
	case "probe":
		fmt.Println(`{"schema":1,"reachable":false,"probes":[]}`)
	default:
		fmt.Println("fixture " + stage)
	}
	return true
}

func TestEMMCCommandRequiresFreshDefaultNoApprovalWithoutInventory(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n", "y\n", "YES\n"} {
		t.Run(fmt.Sprintf("answer=%q", answer), func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
			log := filepath.Join(t.TempDir(), "calls")
			t.Setenv("PLAB_FIRMWARE_CALLS", log)
			var out bytes.Buffer
			root := NewCommand(strings.NewReader(answer), &out, &out)
			root.SetArgs([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml"), "firmware", "write-emmc", "--image", "fixture.img.xz", "--device", `\\.\PhysicalDrive3`, "-y"})
			err := root.Execute()
			approved := affirmative(strings.TrimSpace(answer))
			if approved != (err == nil) {
				t.Fatalf("approval=%t; error=%v\n%s", approved, err, out.String())
			}
			calls, _ := os.ReadFile(log)
			want := "emmc-plan\ncleanup-emmc-plan\n"
			if approved {
				want = "emmc-plan\nwrite-emmc\n"
			}
			if string(calls) != want {
				t.Fatalf("operations: %s", calls)
			}
			if !strings.Contains(out.String(), "[y/N]") || !strings.Contains(out.String(), "FIXTURE-ONLY") {
				t.Fatalf("missing prompt or target identity: %s", out.String())
			}
		})
	}
}

func TestEMMCInteractiveDiskSelectionRequiresExplicitNumber(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PLAB_FIRMWARE_CALLS", log)
	var out bytes.Buffer
	root := NewCommand(strings.NewReader("3\ny\n"), &out, &out)
	root.SetArgs([]string{"firmware", "write-emmc", "--image", "fixture.img.xz"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "emmc-disks\nemmc-plan\nwrite-emmc\n" {
		t.Fatalf("operations: %s", calls)
	}
}

func TestGuidedMigrationOffersEMMCWriteAndRemembersVerifiedResult(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PLAB_FIRMWARE_CALLS", log)
	images := filepath.Join(config.Home(), "artifacts", "armbian")
	if err := os.MkdirAll(images, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(images, "fixture.img.xz"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(standaloneInventory(t))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	a := &app{cfg: cfg, in: strings.NewReader("n\n3\ny\n"), out: &out, errOut: &out}
	p, err := a.onePrinter("my-plus4")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ensureFirmwareEMMC(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	a.in = strings.NewReader("")
	if err := a.ensureFirmwareEMMC(context.Background(), p); err != nil {
		t.Fatal("verified write was not remembered:", err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "emmc-disks\nemmc-plan\nwrite-emmc\n" {
		t.Fatalf("operations: %s", calls)
	}
}

func TestFactoryBackupUsesPromptInsteadOfConfirmationToken(t *testing.T) {
	for _, answer := range []string{"\n", "y\n"} {
		t.Run(fmt.Sprintf("answer=%q", answer), func(t *testing.T) {
			t.Setenv(config.EnvHome, t.TempDir())
			t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
			log := filepath.Join(t.TempDir(), "calls")
			t.Setenv("PLAB_FIRMWARE_CALLS", log)
			var out bytes.Buffer
			root := NewCommand(strings.NewReader(answer), &out, &out)
			root.SetArgs([]string{"--config", standaloneInventory(t), "firmware", "advanced", "image-factory-emmc", "--device", `\\.\PhysicalDrive3`, "--output", filepath.Join(t.TempDir(), "backup")})
			command, _, _ := root.Find([]string{"firmware", "advanced", "image-factory-emmc"})
			if command.Flags().Lookup("confirm") != nil {
				t.Fatal("public confirmation token still required")
			}
			err := root.Execute()
			calls, _ := os.ReadFile(log)
			if answer == "y\n" {
				if err != nil || string(calls) != "image-factory-emmc\n" {
					t.Fatalf("backup: %v; calls %s", err, calls)
				}
			} else if err == nil || len(calls) != 0 {
				t.Fatal("declined backup ran")
			}
		})
	}
}

func TestGuidedPhysicalWorkAndWriteUseOnePrompt(t *testing.T) {
	a := promptApp("y\n")
	var out bytes.Buffer
	a.errOut = &out
	a.guidedFirmware, a.yes = true, true
	if reason := a.authorizeStage(stages.Stage{ID: "fixture", Manual: "Check fixture wiring", Confirm: "PRIVATE-TOKEN"}, true, false); reason != "" {
		t.Fatal(reason)
	}
	if strings.Count(out.String(), "[y/N]") != 1 {
		t.Fatal("duplicate approval prompt")
	}
}

func TestFirmwareHelpShowsPublicWorkflowOperations(t *testing.T) {
	var out bytes.Buffer
	root := NewCommand(strings.NewReader(""), &out, &out)
	root.SetArgs([]string{"firmware", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	firmware, _, _ := root.Find([]string{"firmware"})
	var names []string
	for _, cmd := range firmware.Commands() {
		if !cmd.Hidden {
			names = append(names, cmd.Name())
		}
	}
	if !reflect.DeepEqual(names, []string{"advanced", "migrate", "prepare", "status", "update", "write-emmc"}) {
		t.Fatalf("public commands: %v", names)
	}
	legacy, _, err := root.Find([]string{"mainline", "build"})
	if err != nil || legacy.Name() != "build" {
		t.Fatal("lost compatibility command")
	}
}

func TestPreparationCombinesStagesAndPreservesRenderedEditsOnRerun(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PLAB_FIRMWARE_CALLS", log)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"status":{"print_stats":{"state":"standby"}}}}`)
	}))
	defer server.Close()
	path := standaloneInventory(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{cfg: cfg, in: strings.NewReader("yes\n"), out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	p, _ := a.onePrinter("my-plus4")
	p.MoonrakerURL = server.URL
	// Stubbed build commands use WSL paths without touching the real machine.
	a.cfg.Defaults.Migration.WSLDistro = "Ubuntu"
	a.cfg.Defaults.Migration.KlipperDir = "/fixture/klipper"
	a.cfg.Defaults.Migration.KatapultDir = "/fixture/katapult"
	// Persist build settings so reload after Beacon discovery retains them.
	if err := config.Edit(path, p.Name, map[string]string{"migration.wslDistro": "Ubuntu", "migration.klipperDir": "/fixture/klipper", "migration.katapultDir": "/fixture/katapult"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.prepareFirmware(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(log)
	want := "setup-ssh\naudit\nbackup\nbeacon-devices\nprepare-build\nbuild\nrender-config\narmbian-image\n"
	if string(calls) != want {
		t.Fatalf("stages: %s", calls)
	}
	p, _ = a.onePrinter(p.Name)
	hardware := filepath.Join(p.Migration.ConfigOutput, "hardware.cfg")
	raw, _ := os.ReadFile(hardware)
	raw = append(raw, []byte("\n# Local hardware review\n")...)
	if err := os.WriteFile(hardware, raw, 0600); err != nil {
		t.Fatal(err)
	}
	p.MoonrakerURL = server.URL
	a.in = strings.NewReader("yes\n")
	if err := a.prepareFirmware(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	actual, _ := os.ReadFile(hardware)
	if !bytes.Equal(raw, actual) {
		t.Fatal("rerun replaced local edits")
	}
	book, _ := a.journalFor(p)
	for _, id := range []string{"setup-ssh", "audit", "backup", "build", "render-config"} {
		entry, ok := book.Entry(id)
		if !ok || entry.Attempt != stages.AttemptCompleted {
			t.Fatalf("%s not completed", id)
		}
	}
	if _, ok := book.Entry("stage-mainboard"); ok {
		t.Fatal("preparation started cutover")
	}
}

func TestMainboardFlashNeedsMatchingCURAndRemovedBIN(t *testing.T) {
	card := t.TempDir()
	image := filepath.Join(t.TempDir(), "mainboard.bin")
	data := []byte("verified firmware")
	os.WriteFile(image, data, 0600)
	if err := verifyMainboardCard(card, image); err == nil {
		t.Fatal("missing CUR accepted")
	}
	os.WriteFile(filepath.Join(card, "qd_mcu.CUR"), []byte("wrong"), 0600)
	if err := verifyMainboardCard(card, image); err == nil {
		t.Fatal("wrong CUR accepted")
	}
	os.WriteFile(filepath.Join(card, "qd_mcu.CUR"), data, 0600)
	os.WriteFile(filepath.Join(card, "qd_mcu.bin"), data, 0600)
	if err := verifyMainboardCard(card, image); err == nil {
		t.Fatal("unflashed BIN accepted")
	}
	os.Remove(filepath.Join(card, "qd_mcu.bin"))
	if err := verifyMainboardCard(card, image); err != nil {
		t.Fatal(err)
	}
}

func TestDryRunOnlyProbesAndLeavesInventoryAndJournalUntouched(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PLAB_FIRMWARE_CALLS", log)
	path := standaloneInventory(t)
	before, _ := os.ReadFile(path)
	var out bytes.Buffer
	cmd := NewCommand(strings.NewReader(""), &out, &out)
	cmd.SetArgs([]string{"--config", path, "firmware", "migrate", "my-plus4", "--dry-run"})
	_ = cmd.Execute() // An unresolved stage returns a halt summary.
	calls, _ := os.ReadFile(log)
	if string(calls) != "probe\n" {
		t.Fatalf("dry run operations: %s", calls)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("dry run edited inventory")
	}
	if files, _ := filepath.Glob(filepath.Join(config.Home(), "runs", "*.json")); len(files) != 0 {
		t.Fatal("dry run wrote a journal")
	}
}

func TestPhysicalFlashAcknowledgementCannotBeSkippedWithYes(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	a := promptApp("")
	a.yes = true
	if err := a.verifyMainboardFlash(inventory.Printer{Name: "p"}); err == nil {
		t.Fatal("-y bypassed physical acknowledgement")
	}
}

func TestGuidedSetupAddsPrinterWithoutEditingOtherEntries(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	path := standaloneInventory(t)
	if err := config.Edit(path, "my-plus4", map[string]string{"migration.wslDistro": "Ubuntu", "migration.klipperDir": "/fixture/klipper", "migration.katapultDir": "/fixture/katapult"}, nil); err != nil {
		t.Fatal(err)
	}
	a := promptApp("new.invalid\n\n\nyes\n")
	a.configPath = path
	p, err := a.firmwareSettings(context.Background(), "new-printer")
	if err != nil {
		t.Fatal(err)
	}
	if p.Address != "new.invalid" || p.IdentityFile == "" || p.Migration.BeaconRef == "" || !strings.Contains(p.Migration.ArtifactDir, "new-printer") {
		t.Fatalf("new settings: %#v", p)
	}
	old, err := a.onePrinter("my-plus4")
	if err != nil || old.Address != "plus4.invalid" {
		t.Fatal("other printer changed")
	}
	if !strings.HasPrefix(p.Migration.ConfigOutput, config.Home()) {
		t.Fatal("config output not derived")
	}
	book, _ := a.journalFor(p)
	book.Start("stage-mainboard")
	book.Save()
	a.in = strings.NewReader("yes\n")
	if _, err := a.firmwareSettings(context.Background(), p.Name); err == nil || !strings.Contains(err.Error(), "already reached") {
		t.Fatalf("preparation repeated after cutover: %v", err)
	}
}

func TestDeclinedPreparationDoesNotSaveInventory(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	path := standaloneInventory(t)
	if err := config.Edit(path, "my-plus4", map[string]string{"migration.wslDistro": "Ubuntu", "migration.klipperDir": "/fixture/klipper", "migration.katapultDir": "/fixture/katapult"}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a := promptApp("new.invalid\n\n\n\n")
	a.configPath = path
	if _, err := a.firmwareSettings(context.Background(), "new-printer"); err == nil {
		t.Fatal("empty confirmation accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("declined setup edited inventory")
	}
}

func TestInterruptedFlashStopsGuidedMigrationWithoutAnotherOperation(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PLAB_FIRMWARE_CALLS", log)
	path := standaloneInventory(t)
	cfg, _ := config.Load(path)
	a := &app{cfg: cfg}
	p, _ := a.onePrinter("my-plus4")
	book, _ := a.journalFor(p)
	for _, id := range []string{"setup-ssh", "audit", "backup", "build", "render-config", "stage-mainboard", "bootstrap-host"} {
		book.Start(id)
		book.Complete(id)
	}
	book.Start("flash-toolhead-katapult")
	if err := book.Save(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := NewCommand(strings.NewReader("yes\nyes\n"), &out, &out)
	cmd.SetArgs([]string{"--config", path, "firmware", "migrate", "my-plus4", "-y"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("interrupted flash not halted")
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "probe\n" {
		t.Fatalf("ran operations after an uncertain flash: %s", calls)
	}
	if !strings.Contains(out.String(), "started and never finished") || !strings.Contains(out.String(), "flash-toolhead-katapult") {
		t.Fatalf("missing halt explanation: %s", out.String())
	}
}

func TestGuidedMigrationRunsFromPreparedImagesToCommissioning(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PLAB_FIRMWARE_WORKFLOW_HELPER", "1")
	t.Setenv("PLAB_FIRMWARE_PROGRESS_HELPER", "1")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PLAB_FIRMWARE_CALLS", log)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"result":{"status":{"print_stats":{"state":"standby"}}}}`)
	}))
	defer server.Close()
	path := standaloneInventory(t)
	if err := config.Edit(path, "my-plus4", nil, map[string]string{"moonrakerURL": server.URL}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load(path)
	a := &app{cfg: cfg}
	p, _ := a.onePrinter("my-plus4")
	os.MkdirAll(p.Migration.ArtifactDir, 0700)
	for _, image := range []string{"plus4-mainboard-klipper.bin", "plus4-toolhead-klipper.bin", "plus4-toolhead-katapult.bin"} {
		os.WriteFile(filepath.Join(p.Migration.ArtifactDir, image), []byte("validated-image-fixture"), 0600)
	}
	book, _ := a.journalFor(p)
	for _, id := range []string{"setup-ssh", "audit", "backup", "build", "render-config"} {
		book.Start(id)
		book.Complete(id)
	}
	book.Save()
	card := t.TempDir()
	input := strings.Join([]string{"y", card, "y", "y", "", "", "", "y", "y", "y", "y", "y", ""}, "\n")
	var out bytes.Buffer
	cmd := NewCommand(strings.NewReader(input), &out, &out)
	cmd.SetArgs([]string{"--config", path, "firmware", "migrate", p.Name})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("migration: %v\n%s", err, out.String())
	}
	raw, _ := os.ReadFile(log)
	var operations []string
	for _, id := range strings.Fields(string(raw)) {
		if id != "probe" {
			operations = append(operations, id)
		}
	}
	want := []string{"stage-mainboard-sdcard", "setup-ssh", "bootstrap-host", "moonraker-api-key", "flash-toolhead-katapult", "flash-toolhead", "smoke-test", "install-config"}
	if !reflect.DeepEqual(operations, want) {
		t.Fatalf("migration operations: %v", operations)
	}
	book, _ = a.journalFor(p)
	if entry, ok := book.Entry("install-config"); !ok || entry.Attempt != stages.AttemptCompleted {
		t.Fatal("installation not journalled")
	}
	raw, _ = os.ReadFile(path)
	if bytes.Contains(raw, []byte("0123456789abcdef0123456789abcdef")) || strings.Contains(out.String(), "0123456789abcdef0123456789abcdef") {
		t.Fatal("Moonraker secret leaked")
	}
	if os.Getenv("PLAB_MOONRAKER_MY_PLUS4_API_KEY") != "" {
		t.Fatal("transient API key left in process environment")
	}
}
