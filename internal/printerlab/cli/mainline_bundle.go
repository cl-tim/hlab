package cli

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/config"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migration"
)

type mainlinePaths struct {
	Backend   string `json:"backend" yaml:"backend"`
	BundleID  string `json:"bundleId,omitempty" yaml:"bundleId,omitempty"`
	Tools     string `json:"tools" yaml:"tools"`
	Profile   string `json:"profile" yaml:"profile"`
	Artifacts string `json:"artifacts" yaml:"artifacts"`
	Config    string `json:"config" yaml:"config"`
	Backups   string `json:"backups" yaml:"backups"`
}

func newMainlinePathsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use: "paths [PRINTER]", Short: "Show migration tools, profile, output and recovery paths",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runner := a.migrationRunner()
			if err := runner.Validate(); err != nil {
				return err
			}
			root, err := runner.ToolRoot()
			if err != nil {
				return err
			}
			settings := a.cfg.Defaults.Migration
			profile := defaultConfigProfile
			if len(args) == 1 {
				printer, err := a.onePrinter(args[0])
				if err != nil {
					return err
				}
				if err := migrate.RequirePlus4(printer); err != nil {
					return err
				}
				settings.ArtifactDir = printer.Migration.ArtifactDir
				settings.ConfigOutput = printer.Migration.ConfigOutput
				settings.ProfileDir = printer.Migration.ProfileDir
				if printer.Profile != "" {
					profile = printer.Profile
				}
			}
			profilePath := settings.ProfileDir
			if profilePath == "" {
				profilePath = filepath.Join(root, "mainline-configs", profile)
			}
			report := mainlinePaths{
				Backend: "bundled", BundleID: migration.ID(), Tools: root,
				Profile: profilePath, Artifacts: settings.ArtifactDir,
				Config: settings.ConfigOutput, Backups: filepath.Join(config.Home(), "backups"),
			}
			if runner.Repo != "" {
				report.Backend, report.BundleID = "custom", ""
			}
			table := output.Table{Headers: []string{"PATH", "VALUE"}}
			table.AddRow("backend", report.Backend)
			table.AddRow("tools", report.Tools)
			table.AddRow("profile", report.Profile)
			table.AddRow("artifacts", report.Artifacts)
			table.AddRow("config", report.Config)
			table.AddRow("backups", report.Backups)
			return output.Write(a.out, a.format, report, table)
		},
	}
}

func newMainlineArmbianImageCmd(a *app) *cobra.Command {
	var listOnly bool
	var imageURL, outputDir, minimumKernel string
	command := &cobra.Command{
		Use: "armbian-image", Short: "Download and verify the official Qidi X-6 Armbian image",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if outputDir == "" {
				outputDir = filepath.Join(config.Home(), "artifacts", "armbian")
			}
			args := []string{"armbian-image", "--output", outputDir,
				"--minimum-kernel", minimumKernel}
			if listOnly {
				args = append(args, "--list-only")
			}
			if imageURL != "" {
				args = append(args, "--url", imageURL)
			}
			return a.migrationRunner().Run(cmd.Context(), args...)
		},
	}
	command.Flags().BoolVar(&listOnly, "list-only", false, "show official image information")
	command.Flags().StringVar(&imageURL, "url", "", "exact HTTPS image URL instead of the download alias")
	command.Flags().StringVar(&outputDir, "output", "", "download directory (default: Printerlab home/artifacts/armbian)")
	command.Flags().StringVar(&minimumKernel, "minimum-kernel", "6.18.54", "minimum kernel version")
	return command
}
