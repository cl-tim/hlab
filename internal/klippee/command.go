package klippee

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// NewCommand exposes workspace sync without loading hlab's server inventory.
func NewCommand(in io.Reader, out, errOut io.Writer) *cobra.Command {
	var workspace, connection string
	var activeConnection string
	var client *Client
	root := &cobra.Command{Use: "klippee", Short: "Sync Klippee configuration using hlab's native client", SilenceUsage: true, SilenceErrors: true}
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(errOut)
	root.PersistentFlags().StringVar(&workspace, "workspace", "", "configuration workspace (default: current directory)")
	root.PersistentFlags().StringVar(&connection, "connection", "", "explicit connection file (required for non-default printer state)")
	resolve := func() (string, string, error) {
		folder := workspace
		if folder == "" {
			folder = "."
		}
		folder, err := filepath.Abs(folder)
		if err != nil {
			return "", "", err
		}
		path := connection
		if path == "" {
			path = filepath.Join(folder, ".klippee", "config.json")
			if workspace == "" {
				state := os.Getenv("KLIPPEE_STATE_DIR")
				if state == "" {
					home, _ := os.UserHomeDir()
					for candidate := folder; ; candidate = filepath.Dir(candidate) {
						if home != "" && samePath(candidate, home) {
							break
						}
						path = filepath.Join(candidate, ".klippee", "config.json")
						if _, err := os.Lstat(path); !os.IsNotExist(err) {
							return candidate, path, nil
						}
						if filepath.Dir(candidate) == candidate {
							break
						}
					}
				}
				if state == "" {
					home, err := os.UserHomeDir()
					if err != nil {
						return "", "", err
					}
					state = filepath.Join(home, ".klippee")
				}
				path = filepath.Join(state, "config.json")
				if _, err := os.Lstat(path); os.IsNotExist(err) {
					if legacy := filepath.Join(state, "config"); fileExists(legacy) {
						path = legacy
					}
				}
				folder = ""
			}
		}
		path, err = filepath.Abs(path)
		return folder, path, err
	}
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == "init" {
			return nil
		}
		folder, path, err := resolve()
		if err != nil {
			return err
		}
		bound := ""
		if folder != "" && samePath(path, filepath.Join(folder, ".klippee", "config.json")) {
			bound = folder
		}
		cfg, err := Load(cmd.Context(), path, bound)
		if err != nil {
			return err
		}
		client = &Client{Config: cfg, In: in, Out: out, ErrOut: errOut}
		activeConnection = path
		return nil
	}
	var desktop, printer, applyCurrent, captureCurrent bool
	initCmd := &cobra.Command{Use: "init", Short: "Connect this folder or printer using KLIPPEE_* environment variables", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if desktop == printer {
			return fmt.Errorf("choose exactly one of --desktop or --printer")
		}
		if desktop && connection != "" {
			return fmt.Errorf("--connection may only be supplied when initializing a printer")
		}
		if applyCurrent && captureCurrent {
			return fmt.Errorf("choose only one of --apply-current or --capture-current")
		}
		folder, err := filepath.Abs(workspace)
		if err != nil {
			return err
		}
		if printer && workspace == "" && os.Getenv("KLIPPEE_CONFIG_DIR") != "" {
			folder, err = filepath.Abs(os.Getenv("KLIPPEE_CONFIG_DIR"))
			if err != nil {
				return err
			}
		}
		mode := "desktop"
		path := connection
		if printer {
			mode = "printer"
			if workspace == "" && os.Getenv("KLIPPEE_CONFIG_DIR") == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				folder = filepath.Join(home, "printer_data", "config")
			}
			if path == "" {
				state := os.Getenv("KLIPPEE_STATE_DIR")
				if state == "" {
					home, err := os.UserHomeDir()
					if err != nil {
						return err
					}
					state = filepath.Join(home, ".klippee")
				}
				path = filepath.Join(state, "config.json")
			}
		}
		cfg := Config{BaseURL: os.Getenv("KLIPPEE_BASE_URL"), RepositoryID: os.Getenv("KLIPPEE_REPOSITORY_ID"), Token: os.Getenv("KLIPPEE_TOKEN"), ConfigDir: folder, ClientMode: mode}
		if cfg.BaseURL == "" {
			cfg.BaseURL = "https://klippee.xyz"
		}
		if err := Init(cmd.Context(), cfg, path); err != nil {
			return err
		}
		fmt.Fprintf(out, "Connected %s workspace: %s\n", mode, folder)
		c := &Client{Config: cfg, In: in, Out: out, ErrOut: errOut}
		if applyCurrent {
			return c.Apply(cmd.Context(), true, false, "")
		}
		if captureCurrent {
			return c.Capture(cmd.Context(), "Initial configuration capture")
		}
		return nil
	}}
	initCmd.Flags().BoolVar(&desktop, "desktop", false, "connect a desktop workspace")
	initCmd.Flags().BoolVar(&printer, "printer", false, "connect a physical printer")
	initCmd.Flags().BoolVar(&applyCurrent, "apply-current", false, "install current desired configuration after connecting")
	initCmd.Flags().BoolVar(&captureCurrent, "capture-current", false, "capture existing safe config files after connecting")
	target := &cobra.Command{Use: "target", Short: "Show connected printer and service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return client.Target(cmd.Context()) }}
	var diff bool
	check := &cobra.Command{Use: "check", Short: "Compare local files with desired configuration", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return client.Check(cmd.Context(), diff) }}
	check.Flags().BoolVar(&diff, "diff", false, "show configuration diff")
	var yes, clean bool
	apply := &cobra.Command{Use: "apply", Short: "Review and install desired configuration with a backup", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return client.Apply(cmd.Context(), yes, clean, "") }}
	apply.Flags().BoolVarP(&yes, "yes", "y", false, "approve replacing the listed local files")
	apply.Flags().BoolVar(&clean, "if-clean", false, "apply only with a verified unchanged local baseline")
	var note string
	capture := &cobra.Command{Use: "capture", Short: "Capture safe referenced configuration files", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return client.Capture(cmd.Context(), note) }}
	capture.Flags().StringVar(&note, "note", "", "configuration history note")
	recover := &cobra.Command{Use: "recover [cloud:NUMBER|local:NAME]", Short: "List or restore recovery points", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		selection := ""
		if len(args) > 0 {
			selection = args[0]
		}
		return client.Recover(cmd.Context(), selection)
	}}
	variables := &cobra.Command{Use: "variables", Short: "Manage private rendering variables"}
	for _, action := range []string{"list", "doctor", "set", "unset"} {
		count := 0
		if action == "set" {
			count = 2
		}
		if action == "unset" {
			count = 1
		}
		variables.AddCommand(&cobra.Command{Use: action, Args: cobra.ExactArgs(count), RunE: func(_ *cobra.Command, args []string) error {
			return client.Variables(append([]string{action}, args...)...)
		}})
	}
	root.AddCommand(initCmd, target, check, apply, capture, recover, variables)
	agent := &cobra.Command{Use: "moonraker-agent", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return client.agent(cmd.Context(), activeConnection)
	}}
	installAgent := &cobra.Command{Use: "install-moonraker", Short: "Install or migrate the Moonraker dashboard agent", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return client.installService(cmd.Context(), activeConnection, false)
	}}
	installTimer := &cobra.Command{Use: "install-timer", Short: "Install periodic read-only configuration checks", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return client.installService(cmd.Context(), activeConnection, true)
	}}
	var previous bool
	diagnostics := &cobra.Command{Use: "upload-diagnostics [LOG]", Short: "Upload a private printer diagnostic bundle", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		path := ""
		if len(args) != 0 {
			if previous {
				return fmt.Errorf("choose a log path or --previous")
			}
			path = args[0]
		}
		return client.UploadDiagnostics(cmd.Context(), path, previous)
	}}
	diagnostics.Flags().BoolVar(&previous, "previous", false, "upload the most recent rotated Klippy log")
	reset := &cobra.Command{Use: "query-box-reset-reason", Short: "Read the supported QIDI Box reset reason while Klipper is shut down", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return writeBoxReset(cmd.Context(), client)
	}}
	variableProof := &cobra.Command{Use: "set-variable-proof NAME", Hidden: true, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return client.setVariableProof(cmd.Context(), args[0])
	}}
	root.AddCommand(agent, installAgent, installTimer, diagnostics, reset, variableProof)
	return root
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return !os.IsNotExist(err)
}

func writeBoxReset(ctx context.Context, client *Client) error {
	result, err := client.queryBoxReset(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(client.output()).Encode(result)
}

// Run executes the common workspace operations used by hlab printer klippee.
func (c *Client) Run(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("Klippee command required")
	}
	switch args[0] {
	case "target":
		if len(args) != 1 {
			break
		}
		return c.Target(ctx)
	case "check":
		if len(args) == 1 {
			return c.Check(ctx, false)
		}
		if len(args) == 2 && args[1] == "--diff" {
			return c.Check(ctx, true)
		}
	case "apply":
		yes, clean := false, false
		for _, arg := range args[1:] {
			switch arg {
			case "--yes":
				yes = true
			case "--if-clean":
				clean = true
			default:
				return fmt.Errorf("unknown apply option: %s", arg)
			}
		}
		return c.Apply(ctx, yes, clean, "")
	case "capture":
		if len(args) == 1 {
			return c.Capture(ctx, "")
		}
		if len(args) == 3 && args[1] == "--note" {
			return c.Capture(ctx, args[2])
		}
	case "variables":
		return c.Variables(args[1:]...)
	case "recover":
		if len(args) == 1 {
			return c.Recover(ctx, "")
		}
		if len(args) == 2 {
			return c.Recover(ctx, args[1])
		}
	}
	return fmt.Errorf("unsupported Klippee command or arguments: %s", args[0])
}
