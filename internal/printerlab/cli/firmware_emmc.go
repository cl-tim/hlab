package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/printerlab/config"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/stages"
)

func newFirmwareWriteEMMCCmd(a *app) *cobra.Command {
	var image, device string
	cmd := &cobra.Command{
		Use: "write-emmc", Short: "Write verified Armbian to a spare USB eMMC and verify read-back",
		Long: "Write a downloaded Armbian image to a spare USB eMMC and verify the written image.\n" +
			"Select the image and disk, then approve the write at [y/N].\n" +
			"Requires Administrator PowerShell and a writable USB disk with 512-byte sectors,\n" +
			"a serial number, and a unique disk ID. Boot and system disks are excluded.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.format != "table" {
				return fmt.Errorf("eMMC writing requires interactive table output")
			}
			if a.cfg == nil {
				a.cfg = &config.Config{}
			}
			return a.writeFirmwareEMMC(cmd.Context(), image, device)
		},
	}
	cmd.Flags().StringVar(&image, "image", "", "verified .img.xz image (default: select from downloaded Armbian images)")
	cmd.Flags().StringVar(&device, "device", "", `whole USB disk, such as \\.\PhysicalDrive3 (default: select from attached USB disks)`)
	return cmd
}

func (a *app) writeFirmwareEMMC(ctx context.Context, image, device string) error {
	if image == "" {
		images, err := filepath.Glob(filepath.Join(config.Home(), "artifacts", "armbian", "*.img.xz"))
		if err != nil {
			return err
		}
		switch len(images) {
		case 0:
			return fmt.Errorf("no downloaded Armbian image; run firmware prepare first or supply --image with its verified .sha and .json receipt")
		case 1:
			image = images[0]
		default:
			for i, path := range images {
				fmt.Fprintf(a.errOut, "%d: %s\n", i+1, filepath.Base(path))
			}
			answer, err := a.ask("Armbian image number", "")
			if err != nil {
				return err
			}
			index, err := strconv.Atoi(answer)
			if err != nil || index < 1 || index > len(images) {
				return fmt.Errorf("invalid image selection")
			}
			image = images[index-1]
		}
	}
	if device == "" {
		raw, err := a.migrationRunner().Output(ctx, "emmc-disks")
		if err != nil {
			return err
		}
		var disks []migrate.EMMCDisk
		if err := json.Unmarshal(raw, &disks); err != nil {
			return err
		}
		if len(disks) == 0 {
			return fmt.Errorf("no eligible USB disks; attach the SPARE eMMC reader")
		}
		for _, disk := range disks {
			fmt.Fprintf(a.errOut, "Disk %d: %s, %.1f GiB, serial %s, read-only=%t\n", disk.Number, migrate.SafeDiagnosticSummary([]byte(disk.FriendlyName)), float64(disk.Size)/(1024*1024*1024), migrate.SafeDiagnosticSummary([]byte(disk.SerialNumber)), disk.IsReadOnly)
		}
		answer, err := a.ask("Disk NUMBER for the SPARE eMMC (all its contents will be replaced)", "")
		if err != nil {
			return err
		}
		number, err := strconv.Atoi(answer)
		if err != nil {
			return fmt.Errorf("invalid disk number")
		}
		for _, disk := range disks {
			if disk.Number == number {
				device = disk.Device
				break
			}
		}
		if device == "" {
			return fmt.Errorf("selected disk is not in the USB disk list")
		}
	}
	raw, err := a.migrationRunner().Output(ctx, "emmc-plan", "--image", image, "--device", device)
	if err != nil {
		return err
	}
	var plan migrate.EMMCWritePlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return err
	}
	if plan.Schema != "printerlab/emmc-write/v1" || plan.Manifest == "" || plan.Disk.Device == "" || plan.Bytes < 512 || plan.RawSHA256 == "" {
		return fmt.Errorf("invalid eMMC write plan")
	}
	fmt.Fprintf(a.errOut, "\nImage: %s\nVerified image SHA-256: %s\nWrite: %d bytes\nTarget: %s — %s\nCapacity: %.1f GiB; serial: %s\n", plan.Image, plan.ImageSHA256, plan.Bytes, plan.Disk.Device, migrate.SafeDiagnosticSummary([]byte(plan.Disk.FriendlyName)), float64(plan.Disk.Size)/(1024*1024*1024), migrate.SafeDiagnosticSummary([]byte(plan.Disk.SerialNumber)))
	if err := a.acknowledge("Replace ALL contents of this SPARE USB eMMC with Armbian? Keep the factory module disconnected."); err != nil {
		if cleanupErr := a.migrationRunner().Run(ctx, "cleanup-emmc-plan", "--plan", plan.Manifest); cleanupErr != nil {
			fmt.Fprintln(a.errOut, "Prepared image retained:", plan.Manifest)
		}
		return fmt.Errorf("eMMC write cancelled")
	}
	return a.migrationRunner().Run(ctx, "write-emmc", "--plan", plan.Manifest, "--confirm", "WRITE-EMMC")
}

// ensureFirmwareEMMC writes the spare eMMC or records an existing verified image.
func (a *app) ensureFirmwareEMMC(ctx context.Context, p inventory.Printer) error {
	book, err := a.journalFor(p)
	if err != nil {
		return err
	}
	if entry, ok := book.Entry("write-emmc"); ok && entry.Attempt == stages.AttemptCompleted {
		return nil
	}
	fmt.Fprint(a.errOut, "Has verified Armbian already been written to the SPARE eMMC (by plab or an external writer)? [y/N]: ")
	answer, err := readLine(a.in)
	if err != nil {
		return fmt.Errorf("eMMC preparation was not acknowledged")
	}
	if !affirmative(answer) {
		if err := a.writeFirmwareEMMC(ctx, "", ""); err != nil {
			return err
		}
	}
	book.Start("write-emmc")
	book.Complete("write-emmc")
	return book.Save()
}
