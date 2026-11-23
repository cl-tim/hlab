package migrate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/shellword"
)

// nativeBackend runs workstation migration operations.
type nativeBackend struct {
	ctx               context.Context
	root, home, stage string
	out, errOut       io.Writer
	values            map[string]string
	switches          map[string]bool
	probes            []string
	// transport is a private test seam for reviewed SSH transactions.
	transport  func(string, []byte) ([]byte, int, error)
	httpClient *http.Client
	emmc       emmcPlatform // Private fixture seam; production uses Windows raw disks.
}

type repeatedStrings []string

func (s *repeatedStrings) String() string         { return strings.Join(*s, ",") }
func (s *repeatedStrings) Set(value string) error { *s = append(*s, value); return nil }

// MaybeRunBackend handles the migration subprocess entry point.
// The internal environment flag authorizes this mode.
func MaybeRunBackend(ctx context.Context, args []string, out, errOut io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != nativeBackendFlag {
		return false, 0
	}
	if os.Getenv("PLAB_INTERNAL_MIGRATION") != "1" {
		fmt.Fprintln(errOut, "internal migration entry point is unavailable")
		return true, 1
	}
	b := nativeBackend{ctx: ctx, root: os.Getenv("PLAB_MIGRATION_ROOT"), home: os.Getenv("PLAB_MIGRATION_HOME"), out: out, errOut: errOut}
	if b.home == "" {
		b.home = b.root
	}
	if err := b.execute(args[1:]); err != nil {
		fmt.Fprintln(errOut, err)
		return true, 1
	}
	return true, 0
}

func (b *nativeBackend) execute(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("migration stage is required")
	}
	b.stage = args[0]
	b.values = map[string]string{}
	b.switches = map[string]bool{}
	defaults := map[string]string{"user": "mks", "port": "22", "profile": "mainline-klipper-beacon", "klipper-ref": KlipperRef, "moonraker-ref": MoonrakerRef, "katapult-ref": KatapultRef, "minimum-kernel": "6.18.54", "service": "klipper.service", "klipper-dir": "/home/mks/klipper", "printer-data": "/home/mks/printer_data", "scripts-dir": "/home/mks/scripts/plr", "remote-python": "/home/mks/klippy-env/bin/python", "remote-directory": "/home/mks/qidi-mainline-migration", "katapult-directory": "/home/mks/katapult", "new-user": "plab"}
	fs := flag.NewFlagSet(b.stage, flag.ContinueOnError)
	fs.SetOutput(b.errOut)
	names := strings.Fields("printer user printer-user port identity password-env klipper-dir katapult-dir output klipper-ref moonraker-ref katapult-ref beacon-ref wsl-distro url minimum-kernel data-dir facts-file name value artifacts confirm printer-data scripts-dir remote-python service rollback-manifest backup-manifest profile profile-dir beacon-serial image remote-directory backup-output katapult-directory new-user pubkey device plan moonraker-port wait-seconds api-key-env model firmware-version config-policy firmware-dir")
	pointers := map[string]*string{}
	for _, name := range names {
		pointers[name] = fs.String(name, defaults[name], "")
	}
	bools := map[string]*bool{}
	for _, name := range strings.Fields("list-only json include-facts include-gcodes include-logs include-environments commissioning force generate reuse check host-only box-motion-polling trim-runout-waits usb-event-log fast-probing box-drying fast-variable-saves") {
		bools[name] = fs.Bool(name, false, "")
	}
	var ids repeatedStrings
	fs.Var(&ids, "probe", "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected migration arguments: %v", fs.Args())
	}
	for k, v := range pointers {
		b.values[k] = *v
	}
	for k, v := range bools {
		b.switches[k] = *v
	}
	b.probes = ids
	switch b.stage {
	case "build":
		return b.buildFirmware()
	case "prepare-build":
		return b.prepareBuild()
	case "beacon-devices":
		raw, _, err := b.sshCapture(`for p in /dev/serial/by-id/usb-Beacon_*; do if [ -e "$p" ]; then printf '%s\n' "$p"; fi; done`, nil)
		if err != nil {
			return err
		}
		return b.writeJSON(strings.Fields(string(raw)))
	case "moonraker-api-key":
		raw, _, err := b.sshCapture(b.ownerCommand(quote("/home/"+b.account()+"/moonraker/scripts/fetch-apikey.sh")), nil)
		if err != nil {
			return err
		}
		key := strings.TrimSpace(string(raw))
		if !regexpAPIKey.MatchString(key) {
			return fmt.Errorf("Moonraker returned an invalid API key")
		}
		return b.writeJSON(map[string]string{"apiKey": key})
	case "armbian-image":
		return b.armbianImage()
	case "render-config":
		return b.renderConfig()
	case "validate-render":
		return b.validateRender()
	case "install-config":
		return b.installConfig()
	case "stage-mainboard-sdcard":
		return b.stageMainboard()
	case "image-factory-emmc":
		return b.imageFactoryEMMC()
	case "emmc-disks":
		disks, err := b.emmcPlatform().List()
		if err != nil {
			return err
		}
		return b.writeJSON(disks)
	case "emmc-plan":
		return b.prepareEMMCWrite()
	case "write-emmc":
		return b.writeArmbianEMMC()
	case "cleanup-emmc-plan":
		_, dir, err := b.loadEMMCPlan()
		if err != nil {
			return err
		}
		return os.RemoveAll(dir) // loadEMMCPlan proves this is a private staging directory.
	case "probes":
		return b.writeJSON(nativeProbeDefinitions)
	case "probe":
		return b.probePrinter()
	case "audit":
		return b.audit()
	case "backup":
		return b.backupPrinter()
	case "setup-ssh":
		return b.setupSSH()
	case "setup-management-user":
		return b.setupManagementUser()
	case "bootstrap-host":
		return b.bootstrapHost()
	case "audit-host-tuning", "tune-host":
		return b.hostTuning()
	case "validate-mainboard", "validate-toolhead", "validate-toolhead-katapult", "flash-mainboard", "flash-toolhead", "flash-toolhead-katapult":
		return b.remoteAction()
	case "smoke-test":
		return b.smokeTest()
	case "deploy-plr-hardening", "rollback-plr-hardening":
		return b.plrTransaction()
	case "start-klipper":
		return b.startKlipper()
	case "refresh-klippee-agent":
		return b.refreshAgent()
	case "upgrade-klippee-agent":
		return b.upgradeAgent()
	case "set-klippee-variable":
		return b.setKlippeeVariable()
	case "update-klipper":
		return b.updateKlipper()
	case "update-qidi":
		return b.updateQIDI()
	case "qidi-fixes":
		return b.qidiFixes()
	default:
		return fmt.Errorf("unknown migration stage %q", b.stage)
	}
}
func (b *nativeBackend) value(key string) string { return b.values[key] }
func (b *nativeBackend) confirm(expected string) error {
	if b.value("confirm") != expected {
		return fmt.Errorf("this action requires --confirm %s", expected)
	}
	return nil
}
func (b *nativeBackend) required(key string) (string, error) {
	value := b.value(key)
	if value == "" {
		return "", fmt.Errorf("--%s is required", key)
	}
	return value, nil
}
func (b *nativeBackend) asset(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(b.root, filepath.FromSlash(name)))
}
func (b *nativeBackend) helper(name string) ([]byte, error) {
	return b.asset("tools/migration/" + name)
}
func (b *nativeBackend) writeJSON(value any) error {
	enc := json.NewEncoder(b.out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}
func (b *nativeBackend) run(name string, args []string, input []byte) error {
	cmd := exec.CommandContext(b.ctx, name, args...)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Stdout = b.out
	cmd.Stderr = b.errOut
	return cmd.Run()
}
func (b *nativeBackend) capture(name string, args []string, input []byte) ([]byte, int, error) {
	cmd := exec.CommandContext(b.ctx, name, args...)
	cmd.Stdin = strings.NewReader(string(input))
	stderr := tailBuffer{limit: maxCapturedDiagnosticBytes}
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err == nil {
		return raw, 0, nil
	}
	if e, ok := err.(*exec.ExitError); ok {
		return raw, e.ExitCode(), fmt.Errorf("command exited %d: %s", e.ExitCode(), SafeDiagnosticSummary(stderr.data))
	}
	return raw, -1, err
}
func quote(value string) string { return shellword.Quote(value) }
func quoted(parts ...string) string {
	values := make([]string, len(parts))
	for i, v := range parts {
		values[i] = quote(v)
	}
	return strings.Join(values, " ")
}
func localPath(value string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		value = filepath.Join(home, strings.TrimLeft(value[1:], "/\\"))
	}
	return filepath.Abs(value)
}
func hashBytes(raw []byte) string { digest := sha256.Sum256(raw); return hex.EncodeToString(digest[:]) }
func hashFile(path string) (string, error) {
	stream, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, stream); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func randomToken() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw)
}
func utcStamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func exclusiveWrite(path string, raw []byte) error {
	stream, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := stream.Write(raw)
	syncErr := stream.Sync()
	closeErr := stream.Close()
	if writeErr != nil {
		return writeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
func atomicWrite(path string, raw []byte) error {
	stream, err := os.CreateTemp(filepath.Dir(path), ".plab-write-")
	if err != nil {
		return err
	}
	name := stream.Name()
	defer os.Remove(name)
	if _, err = stream.Write(raw); err != nil {
		stream.Close()
		return err
	}
	if err = stream.Sync(); err != nil {
		stream.Close()
		return err
	}
	if err = stream.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func decodeObject(raw []byte) (map[string]any, error) {
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return result, nil
}
