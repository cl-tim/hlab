package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/buildinfo"
	"github.com/cl-tim/homelab-cli/internal/output"
)

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the hlab version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := buildinfo.Current()

			if a.format.Structured() {
				return output.Write(a.out, a.format, info, output.Table{})
			}
			_, err := fmt.Fprintln(a.out, buildinfo.String())
			return err
		},
	}
}
