package cli

import "github.com/spf13/cobra"

func newMainlineFactoryImageCmd(a *app) *cobra.Command {
	var device, output string
	command := &cobra.Command{Use: "image-factory-emmc", Short: "Read and verify a read-only factory 32 GB QIDI eMMC", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := a.acknowledge("Read the factory eMMC at " + device + " and save its backup to " + output + "? The source disk remains read-only."); err != nil {
			return err
		}
		return a.migrationRunner().Run(cmd.Context(), "image-factory-emmc", "--device", device, "--output", output, "--confirm", "IMAGE-FACTORY-EMMC")
	}}
	command.Flags().StringVar(&device, "device", "", `raw device, for example \\.\PhysicalDrive1`)
	command.Flags().StringVar(&output, "output", "", "new evidence output directory")
	_ = command.MarkFlagRequired("device")
	_ = command.MarkFlagRequired("output")
	return command
}
