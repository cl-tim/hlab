package migrate

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
)

func (b *nativeBackend) account() string {
	if value := b.value("printer-user"); value != "" {
		return value
	}
	return b.value("user")
}

// ValidateSSHConnection checks target syntax without connecting or changing it.
// The same rules apply to inventory entries and transient firmware targets.
func ValidateSSHConnection(printer inventory.Printer) error {
	return validateKlippeeDiagnosticsSSHPrinter(printer)
}
func (b *nativeBackend) connection() (inventory.Printer, error) {
	port, err := strconv.Atoi(b.value("port"))
	if err != nil {
		return inventory.Printer{}, fmt.Errorf("invalid SSH port")
	}
	p := inventory.Printer{Address: b.value("printer"), User: b.value("user"), PrinterUser: b.value("printer-user"), Port: port, IdentityFile: b.value("identity")}
	if err := validateKlippeeDiagnosticsSSHPrinter(p); err != nil {
		return p, err
	}
	return p, nil
}
func (b *nativeBackend) authOptions() ([]string, error) {
	if name := b.value("password-env"); name != "" {
		if b.stage != "setup-ssh" {
			return nil, fmt.Errorf("--password-env is permitted only for setup-ssh")
		}
		if _, err := passwordValue(name); err != nil {
			return nil, err
		}
		path := os.Getenv("SSH_ASKPASS")
		info, err := os.Stat(path)
		if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() || os.Getenv("SSH_ASKPASS_REQUIRE") != "force" {
			return nil, fmt.Errorf("password bootstrap requires the forced Printerlab SSH askpass helper")
		}
		return []string{"-o", "BatchMode=no", "-o", "PubkeyAuthentication=no", "-o", "PreferredAuthentications=password,keyboard-interactive", "-o", "NumberOfPasswordPrompts=1"}, nil
	}
	if identity := b.value("identity"); identity != "" {
		if strings.ContainsAny(identity, "\x00\r\n") {
			return nil, fmt.Errorf("SSH identity path contains a control character")
		}
		path, err := localPath(identity)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("SSH identity not found: %s", path)
		}
		return []string{"-i", path, "-o", "IdentitiesOnly=yes"}, nil
	}
	return nil, nil
}
func (b *nativeBackend) sshArgs(command string) ([]string, error) {
	p, err := b.connection()
	if err != nil {
		return nil, err
	}
	auth, err := b.authOptions()
	if err != nil {
		return nil, err
	}
	args := []string{"-p", strconv.Itoa(p.Port), "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2"}
	args = append(args, auth...)
	return append(args, "--", p.User+"@"+p.Address, command), nil
}
func (b *nativeBackend) sshRun(command string, input []byte) error {
	args, err := b.sshArgs(command)
	if err != nil {
		return err
	}
	if b.transport != nil {
		raw, _, err := b.transport(command, input)
		if len(raw) > 0 {
			_, _ = b.out.Write(raw)
		}
		return err
	}
	return b.run("ssh", args, input)
}
func (b *nativeBackend) sshCapture(command string, input []byte) ([]byte, int, error) {
	args, err := b.sshArgs(command)
	if err != nil {
		return nil, -1, err
	}
	if b.transport != nil {
		return b.transport(command, input)
	}
	return b.capture("ssh", args, input)
}
func (b *nativeBackend) sshWrite(command string, input []byte, writer io.Writer) error {
	args, err := b.sshArgs(command)
	if err != nil {
		return err
	}
	if b.transport != nil {
		raw, _, err := b.transport(command, input)
		if err != nil {
			return err
		}
		_, err = writer.Write(raw)
		return err
	}
	cmd := exec.CommandContext(b.ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = writer
	cmd.Stderr = b.errOut
	return cmd.Run()
}
func (b *nativeBackend) target() string { return b.value("user") + "@" + b.value("printer") }
func (b *nativeBackend) ownerCommand(command string) string {
	owner := b.account()
	if owner != b.value("user") {
		return "sudo -n -u " + quote(owner) + " -H bash -c " + quote(command)
	}
	return command
}
func (b *nativeBackend) collectFacts() (map[string]any, string, error) {
	if _, err := b.connection(); err != nil {
		return nil, "", err
	}
	if _, err := b.authOptions(); err != nil {
		return nil, "", err
	}
	helper, err := b.helper("qidi-plus4-remote.sh")
	if err != nil {
		return nil, "", err
	}
	raw, code, err := b.sshCapture("bash -s -- probe "+quoted(b.value("data-dir"), b.value("printer-user")), helper)
	if err != nil {
		if code < 0 {
			return nil, "", err
		}
		return nil, fmt.Sprintf("probe command exited %d: %s", code, err), nil
	}
	facts, err := decodeObject(raw)
	if err != nil {
		return nil, "probe output was not valid JSON: " + err.Error(), nil
	}
	return facts, "", nil
}
func (b *nativeBackend) audit() error {
	if b.switches["json"] {
		facts, reason, err := b.collectFacts()
		if err != nil {
			return err
		}
		if facts == nil {
			return b.writeJSON(map[string]any{"schema": 1, "reachable": false, "reason": reason})
		}
		return b.writeJSON(facts)
	}
	helper, err := b.helper("qidi-plus4-remote.sh")
	if err != nil {
		return err
	}
	return b.sshRun("bash -s -- audit", helper)
}
func (b *nativeBackend) bootstrapHost() error {
	if err := b.confirm("BOOTSTRAP-HOST"); err != nil {
		return err
	}
	for _, key := range []string{"klipper-ref", "moonraker-ref", "katapult-ref", "beacon-ref"} {
		ref := b.value(key)
		if key == "beacon-ref" && ref == "" {
			continue
		}
		if !commitPattern.MatchString(ref) {
			return fmt.Errorf("--%s must be a full pinned commit", key)
		}
	}
	if artifacts := b.value("artifacts"); artifacts != "" {
		metadata, err := verifyBuildMetadata(artifacts)
		if err != nil {
			return err
		}
		if metadata["klipper_commit"] != b.value("klipper-ref") || metadata["katapult_commit"] != b.value("katapult-ref") {
			return fmt.Errorf("bootstrap pins do not match the built firmware")
		}
	}
	tuning, err := b.helper("qidi-host-tuning.sh")
	if err != nil {
		return err
	}
	bootstrap, err := b.helper("bootstrap-mainline.sh")
	if err != nil {
		return err
	}
	payload := []byte("set -euo pipefail\numask 077\nexport QIDI_HOST_TUNING_HELPER=$(mktemp \"$HOME/.qidi-host-tuning.XXXXXX\")\ntrap 'rm -f \"$QIDI_HOST_TUNING_HELPER\"' EXIT\ncat >\"$QIDI_HOST_TUNING_HELPER\" <<'QIDI_TUNING_EOF'\n")
	payload = append(payload, tuning...)
	payload = append(payload, []byte("\nQIDI_TUNING_EOF\n")...)
	payload = append(payload, bootstrap...)
	environment := "env KLIPPER_REF=" + quote(b.value("klipper-ref")) + " MOONRAKER_REF=" + quote(b.value("moonraker-ref")) + " KATAPULT_REF=" + quote(b.value("katapult-ref")) + " BEACON_REF=" + quote(b.value("beacon-ref")) + " bash -s"
	if b.account() != b.value("user") {
		environment = "sudo -n -u " + quote(b.account()) + " -H " + environment
	}
	return b.sshRun(environment, payload)
}
func (b *nativeBackend) hostTuning() error {
	helper, err := b.helper("qidi-host-tuning.sh")
	if err != nil {
		return err
	}
	if b.stage == "audit-host-tuning" {
		return b.sshRun("bash -s -- audit", helper)
	}
	if err := b.confirm("TUNE-HOST"); err != nil {
		return err
	}
	// The installer copies its own $0 into /usr/local/sbin. It must run from
	// a real private file even though Go sends the source through SSH stdin.
	command := `set -eu; umask 077; helper=$(mktemp /tmp/qidi-host-tuning.XXXXXXXX); trap 'rm -f -- "$helper"' EXIT; cat >"$helper"; chmod 700 "$helper"; sudo -n bash "$helper" install --printer-user ` + quote(b.account())
	return b.sshRun(command, helper)
}
func tarFiles(files map[string][]byte) ([]byte, error) {
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for name, raw := range files {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0700, Size: int64(len(raw)), ModTime: time.Unix(0, 0)}); err != nil {
			return nil, err
		}
		if _, err := archive.Write(raw); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
func (b *nativeBackend) stageImage(kind, image string) (string, string, error) {
	directory := b.value("remote-directory")
	if !strings.HasPrefix(directory, "/") || strings.ContainsAny(directory, "\x00\r\n") {
		return "", "", fmt.Errorf("remote directory must be an absolute path without control characters")
	}
	helper, err := b.helper("qidi-plus4-remote.sh")
	if err != nil {
		return "", "", err
	}
	layouts, err := b.helper("mcu-layouts.json")
	if err != nil {
		return "", "", err
	}
	raw, err := os.ReadFile(image)
	if err != nil {
		return "", "", err
	}
	bundle, err := tarFiles(map[string][]byte{"qidi-plus4-remote.sh": helper, "mcu-layouts.json": layouts, kind + ".bin": raw})
	if err != nil {
		return "", "", err
	}
	if err := b.sshRun(b.ownerCommand("set -eu; umask 077; mkdir -p "+quote(directory)+"; tar -xf - -C "+quote(directory)), bundle); err != nil {
		return "", "", err
	}
	return directory + "/qidi-plus4-remote.sh", directory + "/" + kind + ".bin", nil
}
func (b *nativeBackend) remoteAction() error {
	if b.stage == "flash-mainboard" {
		return fmt.Errorf("direct SSH mainboard flashing is disabled; stage qd_mcu.bin on a FAT32 microSD card and follow QIDI_MAINLINE_KLIPPER.md")
	}
	kind := "toolhead-app"
	action := "validate"
	confirmation := ""
	switch b.stage {
	case "validate-mainboard":
		kind = "mainboard"
	case "validate-toolhead-katapult":
		kind = "toolhead-katapult"
	case "flash-toolhead-katapult":
		kind = "toolhead-katapult"
		action = b.stage
		confirmation = "ERASE-TOOLHEAD"
	case "flash-toolhead":
		action = b.stage
		confirmation = "FLASH-TOOLHEAD"
	}
	if confirmation != "" {
		if err := b.confirm(confirmation); err != nil {
			return err
		}
	}
	image, err := b.verifiedImage(kind)
	if err != nil {
		return err
	}
	helper, remoteImage, err := b.stageImage(kind, image)
	if err != nil {
		return err
	}
	parts := []string{helper, action}
	if action == "validate" {
		parts = append(parts, kind, remoteImage)
	} else {
		digest, err := hashFile(image)
		if err != nil {
			return err
		}
		parts = append(parts, remoteImage, digest, confirmation)
		if action == "flash-toolhead-katapult" {
			directory := b.value("remote-directory") + "/backups"
			backup := directory + "/toolhead-before-erase-" + randomToken() + ".bin"
			if err := b.sshRun(b.ownerCommand(quoted("bash", helper, "backup-toolhead", backup)), nil); err != nil {
				return err
			}
			raw, _, err := b.sshCapture(b.ownerCommand("cat "+quote(backup)), nil)
			if err != nil {
				return err
			}
			if len(raw) != 65536 {
				return fmt.Errorf("toolhead backup is not 64 KiB; refusing erase")
			}
			output := b.value("backup-output")
			if output == "" {
				output = filepath.Join(b.home, "backups")
			}
			if err := os.MkdirAll(output, 0700); err != nil {
				return err
			}
			local := filepath.Join(output, filepath.Base(backup))
			if err := exclusiveWrite(local, raw); err != nil {
				return err
			}
			backupSHA := hashBytes(raw)
			readback, err := hashFile(local)
			if err != nil || readback != backupSHA {
				return fmt.Errorf("workstation backup verification failed; refusing erase")
			}
			if err := exclusiveWrite(local+".sha256", []byte(backupSHA+"  "+filepath.Base(local)+"\n")); err != nil {
				return err
			}
			fmt.Fprintf(b.out, "Verified toolhead recovery dump on workstation: %s\n", local)
			parts = append(parts, directory, backupSHA)
		} else {
			parts = append(parts, b.value("katapult-directory"))
		}
	}
	return b.sshRun(b.ownerCommand("chmod 700 "+quote(helper)+" && "+quoted(parts...)), nil)
}
func publicKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(raw))
	if (!strings.HasPrefix(key, "ssh-") && !strings.HasPrefix(key, "ecdsa-")) || len(strings.Fields(key)) < 2 || strings.ContainsAny(key, "\r\n\x00") {
		return "", fmt.Errorf("%s must contain one OpenSSH public key", path)
	}
	return key, nil
}
func (b *nativeBackend) setupSSH() error {
	p, err := b.connection()
	if err != nil {
		return err
	}
	if _, err := b.authOptionsForBootstrap(); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	private := b.value("identity")
	if private == "" {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			candidate := filepath.Join(home, ".ssh", name)
			if _, err := os.Stat(candidate + ".pub"); err == nil {
				private = candidate
				break
			}
		}
		if private == "" {
			private = filepath.Join(home, ".ssh", "id_ed25519")
		}
	}
	private = strings.TrimSuffix(private, ".pub")
	private, err = localPath(private)
	if err != nil {
		return err
	}
	public := private + ".pub"
	if _, err := os.Stat(public); os.IsNotExist(err) {
		if !b.switches["generate"] {
			return fmt.Errorf("public key not found: %s; use --generate or --identity", public)
		}
		if _, err := os.Lstat(private); err == nil {
			return fmt.Errorf("refusing to overwrite existing private key without its public key")
		}
		if err := os.MkdirAll(filepath.Dir(private), 0700); err != nil {
			return err
		}
		if err := b.run("ssh-keygen", []string{"-t", "ed25519", "-N", "", "-f", private}, nil); err != nil {
			return err
		}
	}
	key, err := publicKey(public)
	if err != nil {
		return err
	}
	auth, err := b.authOptionsForBootstrap()
	if err != nil {
		return err
	}
	args := []string{"-p", strconv.Itoa(p.Port), "-o", "ConnectTimeout=10"}
	args = append(args, auth...)
	install := `set -e; install -d -m 700 "$HOME/.ssh"; touch "$HOME/.ssh/authorized_keys"; chmod 600 "$HOME/.ssh/authorized_keys"; key="$(cat)"; grep -qxF "$key" "$HOME/.ssh/authorized_keys" || printf "%s\n" "$key" >> "$HOME/.ssh/authorized_keys"`
	args = append(args, "--", p.User+"@"+p.Address, install)
	fmt.Fprintf(b.out, "Installing %s on %s...\n", filepath.Base(public), b.target())
	if err := b.run("ssh", args, []byte(key+"\n")); err != nil {
		return err
	}
	verify := []string{"-p", strconv.Itoa(p.Port), "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-i", private, "-o", "IdentitiesOnly=yes", "--", b.target(), "true"}
	if err := b.run("ssh", verify, nil); err != nil {
		return fmt.Errorf("key installed but key login verification failed: %w", err)
	}
	fmt.Fprintf(b.out, "SSH key verified: %s\n", private)
	return nil
}
func (b *nativeBackend) authOptionsForBootstrap() ([]string, error) {
	if b.value("password-env") != "" {
		return b.authOptions()
	}
	return nil, nil
}
func (b *nativeBackend) setupManagementUser() error {
	if err := b.confirm("CREATE-MANAGEMENT-USER"); err != nil {
		return err
	}
	newUser := b.value("new-user")
	if !plrAccountPattern.MatchString(newUser) {
		return fmt.Errorf("invalid management account")
	}
	public, err := b.required("pubkey")
	if err != nil {
		return err
	}
	public, err = localPath(public)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(public, ".pub") {
		public += ".pub"
	}
	key, err := publicKey(public)
	if err != nil {
		return err
	}
	helper, err := b.helper("qidi-plus4-remote.sh")
	if err != nil {
		return err
	}
	if err := b.sshRun("bash -s -- "+quoted("setup-management-user", newUser, key, "CREATE-MANAGEMENT-USER"), helper); err != nil {
		return err
	}
	private := strings.TrimSuffix(public, ".pub")
	if _, err := os.Stat(private); err != nil {
		fmt.Fprintln(b.out, "Account created. Supply its private key to verify login.")
		return nil
	}
	p, err := b.connection()
	if err != nil {
		return err
	}
	if err := b.run("ssh", []string{"-p", strconv.Itoa(p.Port), "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-i", private, "-o", "IdentitiesOnly=yes", "--", newUser + "@" + p.Address, "id -un; sudo -n true && echo sudo=ok"}, nil); err != nil {
		return err
	}
	fmt.Fprintf(b.out, "%s is ready. Set inventory user to this account and printerUser to the Klipper owner.\n", newUser)
	return nil
}
func (b *nativeBackend) smokeTest() error {
	helper, err := b.helper("smoke-test-printer.cfg")
	if err != nil {
		return err
	}
	dataDir := b.value("data-dir")
	if dataDir == "" {
		dataDir = "/home/" + b.account() + "/printer_data"
		b.values["data-dir"] = dataDir
	}
	command := `set -e; cfg=` + quote(dataDir+"/config/printer.cfg") + `; tmp=$(mktemp); trap 'rm -f "$tmp"' EXIT; cat >"$tmp"; owner=` + quote(b.account()) + `; ts=$(date -u +%Y%m%dT%H%M%SZ); if [ -s "$cfg" ]; then sudo -n cp -p "$cfg" "$cfg.$ts.bak"; echo "Backed up existing printer.cfg to $cfg.$ts.bak"; fi; sudo -n install -m 644 -o "$owner" -g "$owner" "$tmp" "$cfg"; sudo -n systemctl restart klipper`
	fmt.Fprintln(b.out, "Installing inert smoke-test configuration and restarting Klipper...")
	if err := b.sshRun(command, helper); err != nil {
		return err
	}
	last := "no facts collected"
	for attempt := 0; attempt < 45; attempt++ {
		facts, reason, err := b.collectFacts()
		if err != nil {
			return err
		}
		if facts != nil {
			config := evaluateNativeProbe(nativeProbeDefinitions[7], facts, hashBytes(helper))
			mcu := evaluateNativeProbe(nativeProbeDefinitions[9], facts, hashBytes(helper))
			last = config.Detail + "; " + mcu.Detail
			objects, _ := lookupFact(facts, "moonraker.mcu_objects")
			names, _ := factList(objects)
			if config.Status == "satisfied" && mcu.Status == "satisfied" && len(names) == 2 && listHas(names, "mcu") && listHas(names, "mcu toolhead") {
				tuning, err := b.helper("qidi-host-tuning.sh")
				if err != nil {
					return err
				}
				if err := b.sshRun("bash -s -- audit", tuning); err != nil {
					return err
				}
				fmt.Fprintln(b.out, "Smoke test passed: inert config, both MCUs ready, runtime tuning verified.")
				return nil
			}
		} else {
			last = reason
		}
		if attempt < 44 {
			if err := waitContext(b.ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("smoke test failed: %s; inert config remains installed", last)
}
