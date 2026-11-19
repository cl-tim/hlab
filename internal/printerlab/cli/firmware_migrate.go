package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/stages"
)

func newFirmwareMigrateCmd(a *app) *cobra.Command {
	var dryRun bool
	command := &cobra.Command{
		Use: "migrate PRINTER", Short: "Guide physical flashing and continue the migration from saved progress",
		Long: "Write and verify Armbian on a spare eMMC, stage the mainboard microSD,\n" +
			"set up the host, flash the toolhead, and install the commissioning configuration.\n" +
			"Run after firmware prepare. Re-run to continue a paused migration.\n" +
			"Disk writes, physical steps, and configuration installation require [y/N] approval.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(p); err != nil {
				return err
			}
			a.guidedFirmware = true
			defer func() { a.guidedFirmware = false }()
			defer a.restoreFirmwareSecrets()
			if dryRun {
				return a.resumeDryRun(cmd.Context(), p)
			}
			if a.format != "table" {
				return fmt.Errorf("guided migration requires table output; use --dry-run for structured preview")
			}
			book, err := a.journalFor(p)
			if err != nil {
				return err
			}
			if book.Stale(stages.Digest(stages.Graph())) || book.Printer != p.Name || book.Recipe != recipeName {
				return fmt.Errorf("migration journal identity or recipe changed; inspect it with firmware advanced before continuing")
			}
			for _, id := range []string{"setup-ssh", "audit", "backup", "build", "render-config"} {
				entry, ok := book.Entry(id)
				if !ok || entry.Attempt != stages.AttemptCompleted {
					return fmt.Errorf("preparation is incomplete (%s); run hlab printer firmware prepare %s before migration", id, p.Name)
				}
			}
			return a.resumeRun(cmd.Context(), p, a.yes, false)
		},
	}
	command.Flags().BoolVar(&dryRun, "dry-run", false, "preview migration progress and stages")
	return command
}

func (a *app) guideFirmwareStage(ctx context.Context, stage *stages.Stage, p *inventory.Printer) error {
	if stage.ID == "flash-toolhead-katapult" || stage.ID == "flash-toolhead" || stage.ID == "tune-host" || stage.ID == "smoke-test" || stage.ID == "install-config" {
		if err := a.firmwareAPIKey(ctx, p, false); err != nil {
			return err
		}
	}
	switch stage.ID {
	case "backup":
		return a.acknowledge("The previous stock backup has expired. Review the refreshed audit before taking a new backup.")
	case "stage-mainboard":
		if err := a.ensureFirmwareEMMC(ctx, *p); err != nil {
			return err
		}
		card, err := a.ask("Verified FAT32 microSD root (for example E:\\)", p.Migration.SDCard)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(card) {
			return fmt.Errorf("microSD path must be absolute")
		}
		if err := a.acknowledge("Write qd_mcu.bin to the microSD at " + card + "?"); err != nil {
			return err
		}
		updated, err := a.saveFirmwareSettings(p.Name, nil, map[string]string{"migration.sdcard": card})
		if err != nil {
			return err
		}
		*p = updated
		stage.Manual = "" // Stage the card before prompting for the physical flash.
		return nil
	case "bootstrap-host":
		if err := a.verifyMainboardFlash(*p); err != nil {
			return err
		}
		stage.Manual = "With power OFF, swap in the spare eMMC containing the verified Armbian image. Retain the factory module. Boot with Ethernet and finish Armbian account setup (normally mks) and passwordless sudo. Verify the new SSH host fingerprint at the console before accepting it."
		fmt.Fprintln(a.errOut, "Enter the Armbian host address and accounts.")
		address, err := a.ask("Replacement host hostname or IP", p.Address)
		if err != nil {
			return err
		}
		user, err := a.ask("Replacement host SSH account", p.User)
		if err != nil {
			return err
		}
		ownerDefault := p.PrinterUser
		if ownerDefault == "" {
			ownerDefault = user
		}
		owner, err := a.ask("Klipper service owner", ownerDefault)
		if err != nil {
			return err
		}
		updated, err := a.saveFirmwareSettings(p.Name, nil, map[string]string{"address": address, "user": user, "printerUser": owner})
		if err != nil {
			return err
		}
		*p = updated
	case "flash-toolhead-katapult":
		stage.Manual = "Power OFF before wiring. Connect J1 pin 1 SWDIO, 3 SWCLK, 5 RESET, 6 GND to ST-Link. Check numbering against your PCB. Installed board: power from the printer harness, leave J1 pins 2 and 4 unconnected. Isolated removed board: disconnect harness and use compatible 3.3 V on pin 2. Use ONE power source. Secure wires, then boot host; connect ST-Link USB to the printer host."
	case "flash-toolhead":
		stage.Manual = "Power OFF, disconnect ST-Link and all temporary leads, restore the printer harness and toolhead cover, then power on before the UART application flash."
	case "install-config":
		stage.Manual = "Review " + p.Migration.ConfigOutput + " for this printer's pin mappings, fans, probe, heater limits, and X-7 board revision. Installation backs up the remote configuration, installs the commissioning files, and restarts Klipper."
	}
	return nil
}

// Fetch through verified SSH, retaining the key only for this process. The
// inventory stores an environment variable name, never the key itself.
func (a *app) firmwareAPIKey(ctx context.Context, p *inventory.Printer, force bool) error {
	if _, fetched := a.firmwareSecrets[p.APIKeyEnv]; !force && fetched {
		return nil
	}
	raw, err := a.migrationRunner().Output(ctx, append([]string{"moonraker-api-key"}, migrate.ConnectionArgs(*p)...)...)
	if err != nil {
		return err
	}
	var result struct {
		APIKey string `json:"apiKey"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("could not decode Moonraker authentication")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`).MatchString(result.APIKey) {
		return fmt.Errorf("Moonraker returned an invalid API key")
	}
	if p.APIKeyEnv == "" {
		name := "PLAB_MOONRAKER_" + strings.ToUpper(strings.ReplaceAll(p.Name, "-", "_")) + "_API_KEY"
		updated, err := a.saveFirmwareSettings(p.Name, nil, map[string]string{"apiKeyEnv": name})
		if err != nil {
			return err
		}
		*p = updated
	}
	if a.firmwareSecrets == nil {
		a.firmwareSecrets = map[string]string{}
	}
	if _, saved := a.firmwareSecrets[p.APIKeyEnv]; !saved {
		a.firmwareSecrets[p.APIKeyEnv] = os.Getenv(p.APIKeyEnv)
	}
	return os.Setenv(p.APIKeyEnv, result.APIKey)
}

func (a *app) restoreFirmwareSecrets() {
	for name, value := range a.firmwareSecrets {
		if value == "" {
			_ = os.Unsetenv(name)
		} else {
			_ = os.Setenv(name, value)
		}
	}
	a.firmwareSecrets = nil
}

func (a *app) verifyMainboardFlash(p inventory.Printer) error {
	book, err := a.journalFor(p)
	if err != nil {
		return err
	}
	if entry, ok := book.Entry("mainboard-flash"); ok && entry.Attempt == stages.AttemptCompleted {
		return nil
	}
	if err := a.acknowledge("Safely eject the staged microSD. Power OFF, insert it into the MAINBOARD slot, power ON and wait for the bootloader update. Power OFF before removing the card; return it to the workstation. qd_mcu.bin must become qd_mcu.CUR. Leave the printer OFF before swapping eMMC."); err != nil {
		return err
	}
	if err := verifyMainboardCard(p.Migration.SDCard, imagePath(p, migrate.MainboardImage)); err != nil {
		return err
	}
	book.Start("mainboard-flash")
	book.Complete("mainboard-flash")
	if err := book.Save(); err != nil {
		return err
	}
	fmt.Fprintln(a.out, "Verified qd_mcu.CUR matches the prepared mainboard firmware.")
	return nil
}

func verifyMainboardCard(card, image string) error {
	if card == "" {
		return fmt.Errorf("microSD path is missing; firmware migrate needs the flashed card back on the workstation")
	}
	if _, err := os.Lstat(filepath.Join(card, "qd_mcu.bin")); !os.IsNotExist(err) {
		return fmt.Errorf("qd_mcu.bin is still present or inaccessible; mainboard flash is not verified")
	}
	info, err := os.Lstat(filepath.Join(card, "qd_mcu.CUR"))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("no regular qd_mcu.CUR on %s; verify the mainboard flash before continuing", card)
	}
	actual, err := os.ReadFile(filepath.Join(card, "qd_mcu.CUR"))
	if err != nil {
		return err
	}
	expected, err := os.ReadFile(image)
	if err != nil {
		return err
	}
	if len(expected) < 8 || !bytes.Equal(actual, expected) {
		return fmt.Errorf("qd_mcu.CUR differs from the prepared firmware; do not continue")
	}
	return nil
}

func (a *app) installFirmwareConfig(p inventory.Printer) stageRunner {
	return stageRunner{Detail: []string{"config: " + p.Migration.ConfigOutput}, Run: func(ctx context.Context) error {
		if err := a.ensureTransitionIdle(ctx, p, true); err != nil {
			return err
		}
		book, err := a.journalFor(p)
		if err != nil {
			return err
		}
		book.Start("install-config")
		if err := book.Save(); err != nil {
			return err
		}
		args := append([]string{"install-config"}, migrate.ConnectionArgs(p)...)
		args = append(args, "--output", p.Migration.ConfigOutput, "--beacon-serial", p.Migration.BeaconSerial, "--confirm", "INSTALL-CONFIG")
		args = append(args, renderConfigSourceArgs(p.Profile, p.Migration.ProfileDir)...)
		err = a.migrationRunner().Run(ctx, args...)
		if err != nil {
			book.Fail("install-config", err)
		} else {
			book.Complete("install-config")
		}
		if saveErr := book.Save(); saveErr != nil {
			return fmt.Errorf("installation result could not be recorded: %v (operation error: %v)", saveErr, err)
		}
		return err
	}}
}
