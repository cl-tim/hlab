package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"go.yaml.in/yaml/v3"

	plabconfig "github.com/cl-tim/homelab-cli/internal/printerlab/config"
)

const testPLRManifest = "/home/mks/scripts/plr/backups/plr-hardening-20260806T000000Z-test/backup-manifest.json"
const testPLRSafetyManifest = "/home/mks/scripts/plr/backups/plr-rollback-safety-20260806T000000Z-test/safety-manifest.json"

const testPLRTargetFields = `
	"account":"mks",
	"paths":{
		"home":"/home/mks",
		"klipper_dir":"/home/mks/klipper",
		"printer_data":"/home/mks/printer_data",
		"scripts_dir":"/home/mks/scripts/plr",
		"python":"/home/mks/klippy-env/bin/python",
		"gcode_root":"/home/mks/printer_data/gcodes",
		"checkpoint":"/home/mks/scripts/plr/plr_checkpoint.json",
		"record":"/home/mks/scripts/plr/plr_record",
		"output":"/home/mks/printer_data/gcodes/.plr/plr.gcode"
	},`

type plrFixture struct {
	configPath string
	toolPath   string
	argsPath   string
}

func newPLRFixture(t *testing.T, moonrakerURL string) plrFixture {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "qidi-plus4-firmware")
	tool := "--internal-migration-backend"
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(repo, "tools", "migration", "qidi-plus4-remote.sh")
	if err := os.MkdirAll(filepath.Dir(helper), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, []byte("# fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"defaults": map[string]any{
			"firmwareRepo": repo,
		},
		"printers": []map[string]any{{
			"name": "fixturePrinterA", "address": "fixturePrinterA.example", "model": "plus4",
			"user": "plab", "printerUser": "mks", "port": 22,
			"moonrakerURL": moonrakerURL,
		}},
		"groups": map[string][]string{},
	}
	raw, err := yaml.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "printerlab.yaml")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return plrFixture{
		configPath: configPath, toolPath: tool,
		argsPath: filepath.Join(t.TempDir(), "args.json"),
	}
}

func bindPLRTarget(document string) string {
	return strings.Replace(document, "{", "{"+testPLRTargetFields, 1)
}

func idleMoonraker(t *testing.T, requests *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.RequestURI() != "/printer/objects/query?print_stats" {
			t.Errorf("unexpected request %s", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"result":{"status":{"print_stats":{"state":"standby"}}}}`))
	}))
}

func setMigrationHelper(t *testing.T, fixture plrFixture, document string) {
	t.Helper()
	t.Setenv(cliHelperEnvironment, "1")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "0")
	t.Setenv("PLAB_MIGRATION_JSON", document)
	t.Setenv("PLAB_CLI_ARGS_FILE", fixture.argsPath)
	t.Setenv(plabconfig.EnvHome, t.TempDir())
}

func readMigrationArgs(t *testing.T, fixture plrFixture) []string {
	t.Helper()
	raw, err := os.ReadFile(fixture.argsPath)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	if err := json.Unmarshal(raw, &args); err != nil {
		t.Fatal(err)
	}
	return args
}

func TestMainlineDeployPLRIsIdleGatedAndSurfacesExactManifest(t *testing.T) {
	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := `{
		"schema":"qidi-plus4-plr-deployment/v1","ok":true,"changed":true,
		"transaction_state":"applied",
		"applied":true,"verified":true,"klipper_left_stopped":true,
		"backup_manifest":"` + testPLRManifest + `",
		"installation":{"backup_manifest":"` + testPLRManifest + `"}
	}`
	setMigrationHelper(t, fixture, bindPLRTarget(document))

	stdout, stderr, err := executeMainlineCLI(
		t, fixture.configPath, "deploy-plr-hardening", "fixturePrinterA",
		"-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if requests.Load() != 1 {
		t.Fatalf("idle requests = %d", requests.Load())
	}
	if !strings.Contains(stdout, testPLRManifest) ||
		!strings.Contains(stdout, "deployed; Klipper stopped") {
		t.Fatalf("manifest/lifecycle not surfaced: %q", stdout)
	}
	want := []string{
		fixture.toolPath, "deploy-plr-hardening", "--printer", "fixturePrinterA.example",
		"--user", "plab", "--port", "22", "--printer-user", "mks",
		"--confirm", "DEPLOY-PLR-HARDENING",
	}
	if got := readMigrationArgs(t, fixture); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestMainlineDeployPLRSurfacesStructuredFailureAndNestedBackup(t *testing.T) {
	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, bindPLRTarget(`{
		"schema":"qidi-plus4-plr-deployment/v1","ok":false,"changed":true,
		"phase":"apply",
		"transaction_state":"partial",
		"applied":null,"verified":false,"klipper_left_stopped":true,
		"error":"Remote PLR installer exited 1",
		"installation":{"backup_manifest":"`+testPLRManifest+`",
			"error":"installation failed and rollback was attempted: owner mismatch"}
	}`))
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "1")

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "deploy-plr-hardening", "fixturePrinterA",
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "owner mismatch") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("idle requests = %d", requests.Load())
	}
	for _, wanted := range []string{
		"failed", "apply", "partial", "Remote PLR installer exited 1",
		"installer: installation failed and rollback was attempted: owner mismatch",
		testPLRManifest, "rollback-plr-hardening",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("structured failure output %q missing %q", stdout, wanted)
		}
	}
}

func TestMainlineDeployPLRInvalidJSONIncludesSafeStdoutAndStderrDiagnostics(t *testing.T) {
	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, "remote stdout: installer could not resolve account")
	t.Setenv("PLAB_KLIPPEE_CLI_STDERR", "remote stderr:\n\x1b[31mpermission denied")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "1")

	stdout, stderr, err := executeMainlineCLI(
		t, fixture.configPath, "deploy-plr-hardening", "fixturePrinterA",
		"-y", "--output", "json",
	)
	if err == nil {
		t.Fatal("expected invalid firmware result to fail")
	}
	if !strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("returned error did not retain a safe stderr diagnostic: %v", err)
	}
	for _, wanted := range []string{
		"remote stdout: installer could not resolve account",
		"remote stderr: [31mpermission denied",
		"no retry or rollback was attempted",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("structured diagnostic %q missing %q", stdout, wanted)
		}
	}
	if strings.Contains(stdout, "\x1b") {
		t.Fatalf("structured diagnostic retained terminal escape: %q", stdout)
	}
	if !strings.Contains(stderr, "permission denied") {
		t.Fatalf("live child stderr was not preserved: %q", stderr)
	}
	if requests.Load() != 1 {
		t.Fatalf("idle requests = %d", requests.Load())
	}
}

func TestMainlineDeployPLRRejectsUnboundAccountOrCanonicalPath(t *testing.T) {
	for name, mutate := range map[string]func(string) string{
		"account": func(document string) string {
			return strings.Replace(document, `"account":"mks"`, `"account":"root"`, 1)
		},
		"scripts path": func(document string) string {
			return strings.Replace(
				document, `"scripts_dir":"/home/mks/scripts/plr"`,
				`"scripts_dir":"/home/mks/scripts/other"`, 1,
			)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := idleMoonraker(t, &requests)
			defer server.Close()
			fixture := newPLRFixture(t, server.URL)
			document := bindPLRTarget(`{
				"schema":"qidi-plus4-plr-deployment/v1","ok":true,
				"transaction_state":"not_started","changed":false,
				"applied":false,"verified":true,"klipper_left_stopped":false
			}`)
			setMigrationHelper(t, fixture, mutate(document))

			stdout, _, err := executeMainlineCLI(
				t, fixture.configPath, "deploy-plr-hardening", "fixturePrinterA",
				"-y",
			)
			if err == nil {
				t.Fatal("expected target binding rejection")
			}
			if !strings.Contains(stdout, "indeterminate") ||
				!strings.Contains(stdout, "firmware result unavailable or invalid") {
				t.Fatalf("unbound result was not surfaced safely: %q", stdout)
			}
		})
	}
}

func TestMainlinePLRApprovalPrecedesNetworkAndTool(t *testing.T) {
	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, `{}`)

	_, _, err := executeMainlineCLI(
		t, fixture.configPath, "deploy-plr-hardening", "fixturePrinterA",
	)
	if err == nil || !strings.Contains(err.Error(), "-y/--yes") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("missing approval triggered %d network requests", requests.Load())
	}
	if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
		t.Fatalf("missing approval spawned firmware tool: %v", err)
	}
}

func TestMainlinePLRRejectsNonCanonicalCallerManifestBeforeNetworkAndTool(t *testing.T) {
	for _, operation := range []string{"start-klipper", "rollback-plr-hardening"} {
		t.Run(operation, func(t *testing.T) {
			var requests atomic.Int32
			server := idleMoonraker(t, &requests)
			defer server.Close()
			fixture := newPLRFixture(t, server.URL)
			setMigrationHelper(t, fixture, `{}`)
			_, _, err := executeMainlineCLI(
				t, fixture.configPath, operation, "fixturePrinterA",
				"--backup-manifest", "/tmp/backup-manifest.json",
				"-y",
			)
			if err == nil || !strings.Contains(err.Error(), "non-canonical backup_manifest") {
				t.Fatalf("error = %v", err)
			}
			if requests.Load() != 0 {
				t.Fatalf("invalid handle triggered %d network requests", requests.Load())
			}
			if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
				t.Fatalf("invalid handle spawned firmware tool: %v", err)
			}
		})
	}
}

func TestMainlineStartKlipperRequiresAndEchoesDeployManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/server/info" {
			t.Fatalf("unexpected readiness request: %s", r.URL.RequestURI())
		}
		_, _ = w.Write([]byte(`{"result":{"klippy_connected":true,"klippy_state":"ready"}}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := `{
		"schema":"qidi-plus4-klipper-start/v1","ok":true,
		"service":"klipper.service","observed_state":"active",
		"start_exit":0,"state_exit":0,
		"stability_checks_required":2,"stability_interval_seconds":1,
		"stability_samples":[{"state":"active","exit":0},{"state":"active","exit":0}],
		"rollback_manifest":"` + testPLRManifest + `"
	}`
	setMigrationHelper(t, fixture, document)

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "start-klipper", "fixturePrinterA",
		"--backup-manifest", testPLRManifest, "-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Klippy ready") || !strings.Contains(stdout, testPLRManifest) {
		t.Fatalf("start result = %q", stdout)
	}
	got := strings.Join(readMigrationArgs(t, fixture), "\x00")
	for _, want := range []string{
		"start-klipper", "--confirm\x00START-KLIPPER",
		"--rollback-manifest\x00" + testPLRManifest,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("args %q missing %q", got, want)
		}
	}
}

func TestMainlineStartKlipperFailsClosedWhenMoonrakerReportsKlippyError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"klippy_connected":true,"klippy_state":"error"}}`))
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := `{
		"schema":"qidi-plus4-klipper-start/v1","ok":true,
		"service":"klipper.service","observed_state":"active",
		"start_exit":0,"state_exit":0,
		"stability_checks_required":2,"stability_interval_seconds":1,
		"stability_samples":[{"state":"active","exit":0},{"state":"active","exit":0}],
		"rollback_manifest":"` + testPLRManifest + `"
	}`
	setMigrationHelper(t, fixture, document)

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "start-klipper", "fixturePrinterA",
		"--backup-manifest", testPLRManifest, "-y",
	)
	if err == nil || !strings.Contains(err.Error(), "no rollback was attempted") {
		t.Fatalf("error = %v", err)
	}
	for _, wanted := range []string{
		"systemd window passed; Klippy not ready", testPLRManifest, "rollback-plr-hardening",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("output %q missing %q", stdout, wanted)
		}
	}
}

func TestMainlineStartKlipperDoesNotPollMoonrakerAfterFirmwareStartFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be queried", http.StatusInternalServerError)
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, `{
		"schema":"qidi-plus4-klipper-start/v1","ok":false,
		"service":"klipper.service","observed_state":"failed",
		"start_exit":0,"state_exit":3,
		"stability_checks_required":2,"stability_interval_seconds":1,
		"stability_samples":[{"state":"failed","exit":3}],
		"rollback_manifest":"`+testPLRManifest+`",
		"error":"Klipper activation did not remain active",
		"next_command":"rollback-plr-hardening --backup-manifest `+testPLRManifest+` --confirm ROLLBACK-PLR-HARDENING"
	}`)
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "1")

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "start-klipper", "fixturePrinterA",
		"--backup-manifest", testPLRManifest, "-y",
	)
	if err == nil || !strings.Contains(err.Error(), "activation did not remain active") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("failed firmware start triggered %d Moonraker polls", requests.Load())
	}
	if !strings.Contains(stdout, testPLRManifest) || !strings.Contains(stdout, "rollback-plr-hardening") {
		t.Fatalf("failure did not surface rollback hint: %q", stdout)
	}
	if strings.Contains(stdout, "--confirm") || !strings.Contains(stdout, "-y") {
		t.Fatalf("failure surfaced firmware confirmation syntax instead of Printerlab approval: %q", stdout)
	}
}

func TestMainlineStartKlipperNoJSONFailureRetainsCallerRecovery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be queried", http.StatusInternalServerError)
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, " ")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "1")

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "start-klipper", "fixturePrinterA",
		"--backup-manifest", testPLRManifest, "-y",
	)
	if err == nil || !strings.Contains(err.Error(), "no JSON result") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("no-JSON firmware failure triggered %d Moonraker polls", requests.Load())
	}
	for _, wanted := range []string{
		"firmware result unavailable or invalid", testPLRManifest, "rollback-plr-hardening",
		"no retry or rollback was attempted",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("fallback output %q missing %q", stdout, wanted)
		}
	}
}

func TestMainlineRollbackPLRIsIdleGatedAndPassesOnlyExactManifest(t *testing.T) {
	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	document := `{
		"schema":"qidi-plus4-plr-rollback/v1","ok":true,"changed":true,
		"transaction_state":"rolled_back",
		"applied":true,"verified":true,"klipper_left_stopped":true,
		"backup_manifest":"` + testPLRManifest + `",
		"safety_manifest":"` + testPLRSafetyManifest + `"
	}`
	setMigrationHelper(t, fixture, bindPLRTarget(document))

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "rollback-plr-hardening", "fixturePrinterA",
		"--backup-manifest", testPLRManifest,
		"-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || !strings.Contains(stdout, "rolled back; Klipper stopped") ||
		!strings.Contains(stdout, testPLRSafetyManifest) {
		t.Fatalf("requests=%d output=%q", requests.Load(), stdout)
	}
	got := strings.Join(readMigrationArgs(t, fixture), "\x00")
	if !strings.Contains(got, "--backup-manifest\x00"+testPLRManifest) {
		t.Fatalf("manifest not passed exactly: %q", got)
	}
}

func TestMainlineRollbackSurfacesPartialStateAndBothRecoveryManifests(t *testing.T) {
	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, bindPLRTarget(`{
		"schema":"qidi-plus4-plr-rollback/v1","ok":false,
		"transaction_state":"partial","changed":true,"applied":null,
		"verified":false,"klipper_left_stopped":true,
		"error":"rollback compensation incomplete",
		"rollback":{"backup_manifest":"`+testPLRManifest+`",
			"safety_manifest":"`+testPLRSafetyManifest+`"}
	}`))
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "1")

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "rollback-plr-hardening", "fixturePrinterA",
		"--backup-manifest", testPLRManifest,
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "rollback compensation incomplete") {
		t.Fatalf("error = %v", err)
	}
	for _, wanted := range []string{
		"partial", testPLRManifest, testPLRSafetyManifest, "rollback compensation incomplete",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("partial rollback output %q missing %q", stdout, wanted)
		}
	}
}

func TestMainlineRollbackUnchangedRequiresExactManifestEcho(t *testing.T) {
	for name, returnedManifest := range map[string]string{
		"missing":  "",
		"mismatch": "/home/mks/scripts/plr/backups/other/backup-manifest.json",
	} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			server := idleMoonraker(t, &requests)
			defer server.Close()
			fixture := newPLRFixture(t, server.URL)
			setMigrationHelper(t, fixture, bindPLRTarget(`{
				"schema":"qidi-plus4-plr-rollback/v1","ok":true,
				"transaction_state":"not_started",
				"changed":false,"applied":false,"verified":true,
				"klipper_left_stopped":false,
				"backup_manifest":"`+returnedManifest+`"
			}`))

			stdout, _, err := executeMainlineCLI(
				t, fixture.configPath, "rollback-plr-hardening", "fixturePrinterA",
				"--backup-manifest", testPLRManifest,
				"-y",
			)
			if err == nil || !strings.Contains(err.Error(), "backup_manifest") {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(stdout, testPLRManifest) ||
				!strings.Contains(stdout, "rollback-plr-hardening") {
				t.Fatalf("caller recovery was not retained: %q", stdout)
			}
		})
	}

	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, bindPLRTarget(`{
		"schema":"qidi-plus4-plr-rollback/v1","ok":true,
		"transaction_state":"not_started",
		"changed":false,"applied":false,"verified":true,
		"klipper_left_stopped":false,
		"backup_manifest":"`+testPLRManifest+`"
	}`))
	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "rollback-plr-hardening", "fixturePrinterA",
		"--backup-manifest", testPLRManifest,
		"-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "unchanged") || !strings.Contains(stdout, testPLRManifest) {
		t.Fatalf("unchanged rollback result = %q", stdout)
	}
}

func TestMainlineRollbackNoJSONFailureRetainsCallerRecovery(t *testing.T) {
	var requests atomic.Int32
	server := idleMoonraker(t, &requests)
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, " ")
	t.Setenv("PLAB_KLIPPEE_CLI_EXIT", "1")

	stdout, _, err := executeMainlineCLI(
		t, fixture.configPath, "rollback-plr-hardening", "fixturePrinterA",
		"--backup-manifest", testPLRManifest,
		"-y",
	)
	if err == nil || !strings.Contains(err.Error(), "no JSON result") {
		t.Fatalf("error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("idle requests = %d", requests.Load())
	}
	for _, wanted := range []string{
		"firmware result unavailable or invalid", testPLRManifest, "rollback-plr-hardening",
		"no retry or rollback was attempted",
	} {
		if !strings.Contains(stdout, wanted) {
			t.Fatalf("fallback output %q missing %q", stdout, wanted)
		}
	}
}

func TestMainlineRollbackAllowsAuthoritativeDisconnectedKlippy(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.RequestURI() {
		case "/printer/objects/query?print_stats":
			http.Error(w, "Klippy disconnected", http.StatusServiceUnavailable)
		case "/server/info":
			_, _ = w.Write([]byte(`{"result":{"klippy_connected":false,"klippy_state":"disconnected"}}`))
		default:
			t.Fatalf("unexpected request: %s", r.URL.RequestURI())
		}
	}))
	defer server.Close()
	fixture := newPLRFixture(t, server.URL)
	setMigrationHelper(t, fixture, bindPLRTarget(`{
		"schema":"qidi-plus4-plr-rollback/v1","ok":true,"changed":true,
		"transaction_state":"rolled_back",
		"applied":true,"verified":true,"klipper_left_stopped":true,
		"backup_manifest":"`+testPLRManifest+`",
		"safety_manifest":"`+testPLRSafetyManifest+`"
	}`))

	_, _, err := executeMainlineCLI(
		t, fixture.configPath, "rollback-plr-hardening", "fixturePrinterA",
		"--backup-manifest", testPLRManifest,
		"-y",
	)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want object query + server info", requests.Load())
	}
}

func TestMainlineRollbackRejectsUnsafeOrUnprovenStateBeforeTool(t *testing.T) {
	tests := []struct {
		name         string
		objectStatus int
		objectBody   string
		serverBody   string
	}{
		{name: "printing", objectStatus: 200, objectBody: `{"result":{"status":{"print_stats":{"state":"printing"}}}}`},
		{name: "unknown print state", objectStatus: 200, objectBody: `{"result":{"status":{"print_stats":{"state":"mystery"}}}}`},
		{name: "malformed print state", objectStatus: 200, objectBody: `{"result":{"status":{"print_stats":{}}}}`},
		{name: "connected without print state", objectStatus: 503, serverBody: `{"result":{"klippy_connected":true,"klippy_state":"ready"}}`},
		{name: "contradictory disconnected", objectStatus: 503, serverBody: `{"result":{"klippy_connected":false,"klippy_state":"ready"}}`},
		{name: "transitional disconnected", objectStatus: 503, serverBody: `{"result":{"klippy_connected":false,"klippy_state":"startup"}}`},
		{name: "malformed server info", objectStatus: 503, serverBody: `{"result":{"klippy_state":"disconnected"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/printer/objects/query" {
					if test.objectStatus != 200 {
						http.Error(w, "unavailable", test.objectStatus)
						return
					}
					_, _ = w.Write([]byte(test.objectBody))
					return
				}
				if r.URL.Path == "/server/info" {
					_, _ = w.Write([]byte(test.serverBody))
					return
				}
				http.NotFound(w, r)
			}))
			defer server.Close()
			fixture := newPLRFixture(t, server.URL)
			setMigrationHelper(t, fixture, `{}`)

			_, _, err := executeMainlineCLI(
				t, fixture.configPath, "rollback-plr-hardening", "fixturePrinterA",
				"--backup-manifest", testPLRManifest,
				"-y",
			)
			if err == nil {
				t.Fatal("expected unsafe rollback rejection")
			}
			if _, err := os.Stat(fixture.argsPath); !os.IsNotExist(err) {
				t.Fatalf("unsafe preflight spawned firmware tool: %v", err)
			}
		})
	}
}
