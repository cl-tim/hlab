package migrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

func TestConnectionArgs(t *testing.T) {
	printer := inventory.Printer{
		Address: "mainline-plus4.example", User: "mks", Port: 2222, IdentityFile: "key",
	}
	want := []string{
		"--printer", "mainline-plus4.example", "--user", "mks", "--port", "2222",
		"--identity", "key",
	}
	if got := ConnectionArgs(printer); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func testRunner(t *testing.T) Runner {
	t.Helper()
	repo := t.TempDir()
	helper := filepath.Join(repo, "tools", "migration", "qidi-plus4-remote.sh")
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("# fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return Runner{Repo: repo}
}

func TestRequirePlus4(t *testing.T) {
	if err := RequirePlus4(inventory.Printer{Name: "x", Model: "x-max3"}); err == nil {
		t.Fatal("expected unsupported model error")
	}
}

func TestRunnerBindsOutputsToPrinterlabHome(t *testing.T) {
	t.Setenv("PLAB_MIGRATION_HOME", "unrelated-output-directory")
	t.Setenv("PLAB_INTERNAL_MIGRATION", "0")
	runner := testRunner(t)
	runner.Home = t.TempDir()
	command, cleanup, err := runner.command(context.Background(), "audit")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	want := map[string]string{"PLAB_MIGRATION_HOME": runner.Home, "PLAB_MIGRATION_ROOT": runner.Repo, "PLAB_INTERNAL_MIGRATION": "1"}
	counts := map[string]int{}
	for _, entry := range command.Env {
		name, value, _ := strings.Cut(entry, "=")
		if expected, ok := want[name]; ok {
			counts[name]++
			if value != expected {
				t.Fatalf("%s = %q, want %q", name, value, expected)
			}
		}
	}
	for name := range want {
		if counts[name] != 1 {
			t.Fatalf("%s appears %d times in child environment", name, counts[name])
		}
	}
}

func TestConnectionArgsSendsPrinterUserOnlyWhenItDiffers(t *testing.T) {
	base := inventory.Printer{Address: "plus4.example", User: "plab", Port: 22}

	same := base
	same.PrinterUser = "plab"
	if got := strings.Join(ConnectionArgs(same), " "); strings.Contains(got, "--printer-user") {
		t.Errorf("identical users should not be sent: %s", got)
	}

	differs := base
	differs.PrinterUser = "mks"
	if got := strings.Join(ConnectionArgs(differs), " "); !strings.Contains(got, "--printer-user mks") {
		t.Errorf("got %s, want --printer-user mks", got)
	}

	unset := base
	if got := strings.Join(ConnectionArgs(unset), " "); strings.Contains(got, "--printer-user") {
		t.Errorf("an unset printerUser must not be sent: %s", got)
	}
}

func TestBootstrapConnectionArgsPassesOnlyPasswordEnvironmentName(t *testing.T) {
	t.Setenv("FIXTURE_PRINTER_BOOTSTRAP_PASSWORD", "secret-that-must-not-enter-arguments")
	printer := inventory.Printer{
		Name:           "fixturePrinterA",
		Address:        "fixturePrinterA.example",
		User:           "mks",
		Port:           22,
		IdentityFile:   "dedicated-key",
		SSHPasswordEnv: "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD",
	}
	ordinary := strings.Join(ConnectionArgs(printer), " ")
	if strings.Contains(ordinary, "--password-env") {
		t.Fatalf("ordinary connection received bootstrap password metadata: %s", ordinary)
	}
	joined := strings.Join(BootstrapConnectionArgs(printer), " ")
	if !strings.Contains(joined, "--password-env FIXTURE_PRINTER_BOOTSTRAP_PASSWORD") {
		t.Fatalf("password environment name missing: %s", joined)
	}
	if strings.Contains(joined, "secret-that-must-not-enter-arguments") {
		t.Fatalf("password leaked into arguments: %s", joined)
	}
	if err := ValidateBootstrapConnection(printer); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBootstrapConnectionFailsFastForMissingOrInvalidPasswordEnvironment(t *testing.T) {
	missing := inventory.Printer{Name: "fixturePrinterA", SSHPasswordEnv: "QIDI1_MISSING_PASSWORD"}
	if err := ValidateBootstrapConnection(missing); err == nil || !strings.Contains(err.Error(), "empty or unset") {
		t.Fatalf("got %v, want missing environment error", err)
	}

	t.Setenv("SSH_ASKPASS", "not-a-password")
	reserved := inventory.Printer{Name: "fixturePrinterA", SSHPasswordEnv: "SSH_ASKPASS"}
	if err := ValidateBootstrapConnection(reserved); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("got %v, want reserved environment error", err)
	}
}

func TestPasswordEnvironmentArgumentsRejectEmptyAndDuplicateValues(t *testing.T) {
	for name, args := range map[string][]string{
		"empty":     {"audit", "--password-env="},
		"duplicate": {"audit", "--password-env", "FIRST", "--password-env", "SECOND"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := passwordEnvironmentFromArgs(args); err == nil {
				t.Fatal("expected invalid password environment arguments to fail")
			}
		})
	}
}

func TestRunnerConfiguresGatedAskpassWithoutPasswordArguments(t *testing.T) {
	t.Setenv("FIXTURE_PRINTER_BOOTSTRAP_PASSWORD", "makerbase-test-secret")
	runner := testRunner(t)
	command, cleanup, err := runner.command(
		context.Background(),
		"setup-ssh",
		"--printer", "fixturePrinterA.example",
		"--password-env", "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	environment := map[string]string{}
	for _, entry := range command.Env {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	if environment["SSH_ASKPASS_REQUIRE"] != "force" {
		t.Fatalf("SSH_ASKPASS_REQUIRE = %q", environment["SSH_ASKPASS_REQUIRE"])
	}
	if !filepath.IsAbs(environment["SSH_ASKPASS"]) {
		t.Fatalf("SSH_ASKPASS is not absolute: %q", environment["SSH_ASKPASS"])
	}
	if environment[askpassPasswordNameEnv] != "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD" {
		t.Fatalf("askpass environment name = %q", environment[askpassPasswordNameEnv])
	}
	joinedArgs := strings.Join(command.Args, " ")
	if strings.Contains(joinedArgs, "makerbase-test-secret") {
		t.Fatalf("password leaked into command args: %s", joinedArgs)
	}
	gate := environment[askpassGateFileEnv]
	if info, err := os.Lstat(gate); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("askpass gate is not a regular file: %v", err)
	}

	for _, name := range []string{
		internalAskpassTokenEnv,
		askpassGateFileEnv,
		askpassPasswordNameEnv,
	} {
		t.Setenv(name, environment[name])
	}
	var stdout bytes.Buffer
	handled, code := MaybeRunAskpass([]string{"mks@fixturePrinterA's password:"}, &stdout)
	if !handled || code != 0 {
		t.Fatalf("handled=%v code=%d", handled, code)
	}
	if stdout.String() != "makerbase-test-secret\n" {
		t.Fatalf("askpass output = %q", stdout.String())
	}
	stdout.Reset()
	handled, code = MaybeRunAskpass([]string{"Password:"}, &stdout)
	if !handled || code == 0 || stdout.Len() != 0 {
		t.Fatalf(
			"consumed gate was reusable: handled=%v code=%d output=%q",
			handled, code, stdout.String(),
		)
	}

	cleanup()
	if _, err := os.Stat(gate); !os.IsNotExist(err) {
		t.Fatalf("askpass gate remains after cleanup: %v", err)
	}
}

func TestRunnerRejectsPasswordEnvironmentOutsideSetupSSH(t *testing.T) {
	t.Setenv("FIXTURE_PRINTER_BOOTSTRAP_PASSWORD", "must-not-be-used")
	runner := testRunner(t)
	for _, commandName := range []string{
		"audit", "probe", "backup", "refresh-klippee-agent",
		"install-box-output-clock-guard", "upgrade-klippee-agent",
	} {
		t.Run(commandName, func(t *testing.T) {
			command, cleanup, err := runner.command(
				context.Background(), commandName,
				"--password-env", "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD",
			)
			if err == nil || !strings.Contains(err.Error(), "only for the setup-ssh") {
				if cleanup != nil {
					cleanup()
				}
				t.Fatalf("command=%v error=%v", command, err)
			}
		})
	}
}

func TestRunnerRemovesInventoryBootstrapSecretsFromOrdinaryChildEnvironment(t *testing.T) {
	t.Setenv("FIXTURE_PRINTER_BOOTSTRAP_PASSWORD", "must-not-reach-child")
	runner := testRunner(t)
	runner.BootstrapPasswordEnvironments = []string{"FIXTURE_PRINTER_BOOTSTRAP_PASSWORD"}

	ordinary, cleanup, err := runner.command(context.Background(), "audit")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, entry := range ordinary.Env {
		if strings.HasPrefix(strings.ToUpper(entry), "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD=") {
			t.Fatalf("ordinary child inherited bootstrap secret: %q", entry)
		}
	}

	bootstrap, bootstrapCleanup, err := runner.command(
		context.Background(), "setup-ssh",
		"--password-env", "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrapCleanup()
	found := false
	for _, entry := range bootstrap.Env {
		if entry == "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD=must-not-reach-child" {
			found = true
		}
	}
	if !found {
		t.Fatal("setup-ssh child did not receive its explicitly selected bootstrap secret")
	}
}

func TestValidateConnectionArgumentsRejectsBootstrapPasswordOnOrdinaryStage(t *testing.T) {
	t.Setenv("FIXTURE_PRINTER_BOOTSTRAP_PASSWORD", "must-not-be-used")
	printer := inventory.Printer{
		Name: "fixturePrinterA", SSHPasswordEnv: "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD",
	}
	if err := ValidateConnectionArguments(printer, []string{
		"audit", "--password-env", "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD",
	}); err == nil || !strings.Contains(err.Error(), "only for the setup-ssh") {
		t.Fatalf("error = %v", err)
	}
	if err := ValidateConnectionArguments(printer, append(
		[]string{"setup-ssh"}, BootstrapConnectionArgs(printer)...,
	)); err != nil {
		t.Fatal(err)
	}
}

func TestSafeDiagnosticSummaryIsBoundedAndRemovesControls(t *testing.T) {
	raw := []byte("first line\n\x1b[31m" + strings.Repeat("x", 3000) + " root cause\r\n")
	diagnostic := SafeDiagnosticSummary(raw)
	if strings.ContainsAny(diagnostic, "\r\n\x1b") {
		t.Fatalf("diagnostic retained control characters: %q", diagnostic)
	}
	if !strings.Contains(diagnostic, "root cause") {
		t.Fatalf("diagnostic lost its actionable tail: %q", diagnostic)
	}
	if len(diagnostic) > maxDiagnosticSummaryBytes+3 {
		t.Fatalf("diagnostic length = %d, want at most %d", len(diagnostic), maxDiagnosticSummaryBytes+3)
	}
}

func TestTailBufferRetainsOnlyBoundedTail(t *testing.T) {
	buffer := tailBuffer{limit: 8}
	for _, part := range []string{"abc", "defghi", "jkl"} {
		if written, err := buffer.Write([]byte(part)); err != nil || written != len(part) {
			t.Fatalf("Write(%q) = %d, %v", part, written, err)
		}
	}
	if got := string(buffer.data); got != "efghijkl" {
		t.Fatalf("tail = %q, want %q", got, "efghijkl")
	}
}

func TestAskpassGateRejectsOrdinaryOrTamperedInvocationSilently(t *testing.T) {
	var stdout bytes.Buffer
	if handled, _ := MaybeRunAskpass([]string{"password:"}, &stdout); handled {
		t.Fatal("ordinary invocation entered internal askpass mode")
	}

	t.Setenv("FIXTURE_PRINTER_BOOTSTRAP_PASSWORD", "do-not-print")
	runner := testRunner(t)
	command, cleanup, err := runner.command(
		context.Background(), "setup-ssh", "--password-env", "FIXTURE_PRINTER_BOOTSTRAP_PASSWORD",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	environment := map[string]string{}
	for _, entry := range command.Env {
		name, value, _ := strings.Cut(entry, "=")
		environment[name] = value
	}
	for _, name := range []string{
		internalAskpassTokenEnv,
		askpassGateFileEnv,
		askpassPasswordNameEnv,
	} {
		t.Setenv(name, environment[name])
	}
	if err := os.WriteFile(environment[askpassGateFileEnv], []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	handled, code := MaybeRunAskpass([]string{"Password:"}, &stdout)
	if !handled || code == 0 || stdout.Len() != 0 {
		t.Fatalf("handled=%v code=%d output=%q", handled, code, stdout.String())
	}
}
