package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/shellword"
	"github.com/cl-tim/homelab-cli/internal/stack"
)

func newStackDiffCmd(a *app) *cobra.Command {
	var root, file string
	cmd := &cobra.Command{
		Use:   "diff <host> [project]",
		Short: "Compare deployed Compose files with local copies",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if file != "" && len(args) != 2 {
				return fmt.Errorf("--file requires an explicit project")
			}
			host, err := a.oneManagedHost(args)
			if err != nil {
				return err
			}

			var names []string
			if len(args) == 2 {
				names = []string{args[1]}
			} else {
				projects, err := a.listProjects(cmd.Context(), host)
				if err != nil {
					return err
				}
				for _, p := range projects {
					if p.Adopted() {
						names = append(names, p.Name)
					}
				}
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			type result struct {
				Project string `json:"project" yaml:"project"`
				Local   string `json:"local" yaml:"local"`
				Status  string `json:"status" yaml:"status"`
			}
			results := make([]result, 0, len(names))
			tbl := output.Table{Headers: []string{"PROJECT", "LOCAL FILE", "STATUS"}}
			different := false
			for _, name := range names {
				if err := stack.ValidateName(name); err != nil {
					return err
				}
				local := file
				if local == "" {
					local = filepath.Join(root, name, "compose.yaml")
				}
				status := "identical"
				data, err := os.ReadFile(local)
				if err != nil {
					status = fmt.Sprintf("local file unavailable: %v", err)
				} else {
					remote := stack.RemoteDir + "/" + name + "/compose.yaml"
					script := "sha256sum < " + shellword.Quote(remote) + "\n"
					res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), script)
					fields := strings.Fields(res.Stdout)
					switch {
					case err != nil:
						status = err.Error()
					case !res.OK() || len(fields) == 0:
						status = "remote checksum unavailable"
					case fields[0] != sha256Hex(data):
						status = "changed"
					}
				}
				different = different || status != "identical"
				results = append(results, result{Project: name, Local: local, Status: status})
				tbl.AddRow(name, local, status)
			}
			if err := output.Write(a.out, a.format, results, tbl); err != nil {
				return err
			}
			if different {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "dir", filepath.Join(config.Home(), stack.LocalDirName), "directory containing per-project Compose files")
	cmd.Flags().StringVar(&file, "file", "", "compare against this Compose file")
	return cmd
}
