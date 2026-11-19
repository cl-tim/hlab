package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/stages"
)

func newFirmwareCmd(a *app) *cobra.Command {
	command := &cobra.Command{Use: "firmware", Short: "Prepare, migrate and check printer firmware"}
	advanced := newMainlineCmd(a)
	advanced.Use, advanced.Short, advanced.Hidden = "advanced", "Individual migration operations and recovery tools", false
	status := newMainlineStateCmd(a)
	status.Use, status.Short = "status PRINTER", "Show firmware migration progress and live verification"
	command.AddCommand(newFirmwarePrepareCmd(a), newFirmwareMigrateCmd(a), newFirmwareWriteEMMCCmd(a), newFirmwareUpdateCmd(a), status, advanced)
	return command
}

func newFirmwarePrepareCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "prepare PRINTER", Short: "Guide setup, back up stock firmware, build images and generate configuration",
		Long: "Save printer settings, install an SSH key, audit and back up the stock printer,\n" +
			"build MCU firmware, generate the commissioning configuration, and download Armbian.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.format != "table" {
				return fmt.Errorf("guided preparation requires table output")
			}
			printer, err := a.firmwareSettings(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.prepareFirmware(cmd.Context(), printer)
		},
	}
}

// ask reads a line without buffering ahead of SSH's own terminal prompts.
func (a *app) ask(label, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(a.errOut, "%s [%s]: ", label, fallback)
	} else {
		fmt.Fprintf(a.errOut, "%s: ", label)
	}
	answer, err := readLine(a.in)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	if answer == "" {
		answer = fallback
	}
	if answer == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	return answer, nil
}

func (a *app) acknowledge(message string) error {
	fmt.Fprintf(a.errOut, "%s [y/N]: ", message)
	answer, err := readLine(a.in)
	if err != nil || !affirmative(answer) {
		return fmt.Errorf("confirmation was not given")
	}
	return nil
}

func affirmative(answer string) bool {
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes")
}

func (a *app) saveFirmwareSettings(name string, defaults, fields map[string]string) (inventory.Printer, error) {
	if err := config.Edit(a.cfg.Path, name, defaults, fields); err != nil {
		return inventory.Printer{}, err
	}
	cfg, err := config.Load(a.cfg.Path)
	if err != nil {
		return inventory.Printer{}, err
	}
	a.cfg = cfg
	return a.onePrinter(name)
}

func (a *app) firmwareSettings(ctx context.Context, name string) (inventory.Printer, error) {
	if name == "all" || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`).MatchString(name) {
		return inventory.Printer{}, fmt.Errorf("guided setup needs an exact printer name containing letters, numbers, - or _")
	}
	path := a.configPath
	if path == "" {
		path = os.Getenv(config.EnvConfigPath)
	}
	if path == "" {
		path = config.DefaultPath()
	}
	cfg, err := config.Load(path)
	if os.IsNotExist(err) {
		// Obtain built-in defaults without consulting a missing explicit path.
		cfg = &config.Config{Path: path}
		err = nil
	}
	if err != nil {
		return inventory.Printer{}, err
	}
	a.cfg = cfg
	p := inventory.Printer{Name: name, Model: "plus4", Firmware: inventory.FirmwareStock, User: "mks", PrinterUser: "mks", Profile: defaultConfigProfile}
	existing := false
	if cfg.Inventory != nil {
		for _, item := range cfg.Inventory.Printers() {
			if item.Name == name {
				p, existing = item, true
				break
			}
		}
	}
	if err := migrate.RequirePlus4(p); err != nil {
		return p, err
	}
	if p.Firmware == inventory.FirmwareMainline {
		return p, fmt.Errorf("%s already declares mainline firmware; use firmware status or advanced operations", name)
	}
	book, err := a.journalFor(p)
	if err != nil {
		return p, err
	}
	if book.Stale(stages.Digest(stages.Graph())) || book.Printer != p.Name || book.Recipe != recipeName {
		return p, fmt.Errorf("migration journal identity or recipe changed; inspect it with firmware advanced before continuing")
	}
	for _, id := range []string{"stage-mainboard", "bootstrap-host", "flash-toolhead-katapult", "flash-toolhead", "install-config"} {
		if entry, ok := book.Entry(id); ok && entry.Attempt != stages.AttemptNone {
			return p, fmt.Errorf("migration has already reached %s; continue with hlab printer firmware migrate %s", id, name)
		}
	}
	fields := map[string]string{}
	if !existing {
		for _, item := range []struct{ key, label, fallback string }{
			{"address", "Printer hostname or IP", ""},
			{"user", "SSH account", "mks"},
		} {
			value, err := a.ask(item.label, item.fallback)
			if err != nil {
				return p, err
			}
			fields[item.key] = value
		}
		fields["model"], fields["firmware"], fields["profile"] = p.Model, p.Firmware, p.Profile
		fields["printerUser"] = fields["user"]
	}
	if p.IdentityFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		key, err := a.ask("SSH key file (created if missing)", filepath.Join(home, ".ssh", name+"-plab-ed25519"))
		if err != nil {
			return p, err
		}
		fields["identityFile"] = key
	}
	if p.Migration.BeaconRef == "" {
		fields["migration.beaconRef"] = "3eb0134607664734ccf5cd94cb51840e1c8b27b3"
	}
	if !existing {
		fields["migration.artifactDir"] = filepath.Join(config.Home(), "artifacts", "firmware", name)
	}
	d := cfg.Defaults.Migration
	defaults := map[string]string{}
	linuxHome := ""
	if runtime.GOOS == "windows" {
		if d.WSLDistro == "" {
			d.WSLDistro, err = a.ask("WSL distribution for builds", "Ubuntu")
			if err != nil {
				return p, err
			}
			defaults["migration.wslDistro"] = d.WSLDistro
		}
		if strings.HasPrefix(d.WSLDistro, "-") || strings.ContainsAny(d.WSLDistro, "\x00\r\n") {
			return p, fmt.Errorf("invalid WSL distribution")
		}
		if d.KlipperDir == "" || d.KatapultDir == "" {
			raw, err := exec.CommandContext(ctx, "wsl", "-d", d.WSLDistro, "--", "sh", "-c", `printf '%s\n' "$HOME"`).Output()
			if err != nil {
				return p, fmt.Errorf("read WSL home: %w; install/start the selected distribution first", err)
			}
			linuxHome = strings.TrimSpace(string(raw))
		}
	} else {
		linuxHome, err = os.UserHomeDir()
		if err != nil {
			return p, err
		}
	}
	if d.KlipperDir == "" || d.KatapultDir == "" {
		if !strings.HasPrefix(linuxHome, "/") || strings.ContainsAny(linuxHome, "\x00\r\n") {
			return p, fmt.Errorf("cannot resolve an absolute Linux home")
		}
		if d.KlipperDir == "" {
			defaults["migration.klipperDir"] = linuxHome + "/.local/share/printerlab/sources/klipper"
		}
		if d.KatapultDir == "" {
			defaults["migration.katapultDir"] = linuxHome + "/.local/share/printerlab/sources/katapult"
		}
	}
	address, user := p.Address, p.User
	if value := fields["address"]; value != "" {
		address = value
	}
	if value := fields["user"]; value != "" {
		user = value
	}
	fmt.Fprintf(a.out, "Inventory: %s\nTarget: %s (%s@%s), model %s\n", a.cfg.Path, p.Name, user, address, p.Model)
	if err := a.acknowledge("Save these settings and prepare this printer with stock firmware idle and all heaters off? Review the X-7 V1.0/Beacon hardware scope in QIDI_MAINLINE_KLIPPER.md first."); err != nil {
		return p, err
	}
	return a.saveFirmwareSettings(name, defaults, fields)
}

func (a *app) setupFirmwareSSH(ctx context.Context, p inventory.Printer) error {
	// An expired password environment must not prevent normal terminal login.
	if p.SSHPasswordEnv != "" && os.Getenv(p.SSHPasswordEnv) == "" {
		p.SSHPasswordEnv = ""
	}
	args := append([]string{"setup-ssh"}, migrate.BootstrapConnectionArgs(p)...)
	args = append(args, "--generate")
	return a.runStage(ctx, p, "setup-ssh", args...)
}

func (a *app) prepareFirmware(ctx context.Context, p inventory.Printer) error {
	if err := a.ensureTransitionIdle(ctx, p, true); err != nil {
		return err
	}
	if err := a.setupFirmwareSSH(ctx, p); err != nil {
		return err
	}
	for _, id := range []string{"audit", "backup"} {
		stage, _ := stages.Find(stages.Graph(), id)
		if id == "backup" {
			if err := a.acknowledge("Review the audit above. Does it describe the expected printer and hardware?"); err != nil {
				return err
			}
		}
		executor := a.resumeExecutor(stage, p)
		a.describeStage(stage, executor)
		if err := executor.Run(ctx); err != nil {
			return err
		}
	}
	raw, err := a.migrationRunner().Output(ctx, append([]string{"beacon-devices"}, migrate.ConnectionArgs(p)...)...)
	if err != nil {
		return err
	}
	var devices []string
	if err := json.Unmarshal(raw, &devices); err != nil {
		return err
	}
	serial := p.Migration.BeaconSerial
	if len(devices) == 1 {
		serial = devices[0]
	}
	if len(devices) != 1 {
		fmt.Fprintf(a.errOut, "Detected Beacon devices: %s\n", strings.Join(devices, ", "))
		serial, err = a.ask("Exact Beacon /dev/serial/by-id path", serial)
		if err != nil {
			return err
		}
	}
	if !strings.HasPrefix(serial, "/dev/serial/by-id/usb-Beacon_") || strings.ContainsAny(serial, " \t\r\n\x00") {
		return fmt.Errorf("invalid Beacon serial path")
	}
	p, err = a.saveFirmwareSettings(p.Name, nil, map[string]string{"migration.beaconSerial": serial})
	if err != nil {
		return err
	}
	d := a.migrationDefaults()
	k, _, kat := migrationRefs(p.Migration)
	args := []string{"prepare-build", "--klipper-dir", d.KlipperDir, "--katapult-dir", d.KatapultDir, "--klipper-ref", k, "--katapult-ref", kat}
	if d.WSLDistro != "" {
		args = append(args, "--wsl-distro", d.WSLDistro)
	}
	if err := a.migrationRunner().Run(ctx, args...); err != nil {
		return err
	}
	for _, id := range []string{"build", "render-config"} {
		if id == "render-config" {
			if _, err := os.Lstat(p.Migration.ConfigOutput); err == nil {
				args := []string{"validate-render", "--output", p.Migration.ConfigOutput, "--beacon-serial", p.Migration.BeaconSerial}
				args = append(args, renderConfigSourceArgs(p.Profile, p.Migration.ProfileDir)...)
				if err := a.runStage(ctx, p, "render-config", args...); err != nil {
					return err
				}
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		stage, _ := stages.Find(stages.Graph(), id)
		executor := a.resumeExecutor(stage, p)
		if executor.Run == nil {
			return fmt.Errorf("%s", cannotRunReason(stage, executor))
		}
		a.describeStage(stage, executor)
		if err := executor.Run(ctx); err != nil {
			return err
		}
	}
	if err := a.migrationRunner().Run(ctx, "armbian-image", "--reuse", "--output", filepath.Join(config.Home(), "artifacts", "armbian")); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "\nPrepared %s.\nFirmware: %s\nConfiguration: %s\nArmbian: %s\nBackups: %s\nNext: hlab printer firmware migrate %s\n", p.Name, p.Migration.ArtifactDir, p.Migration.ConfigOutput, filepath.Join(config.Home(), "artifacts", "armbian"), filepath.Join(config.Home(), "backups"), p.Name)
	return nil
}
