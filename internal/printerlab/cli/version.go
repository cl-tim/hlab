package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/buildinfo"
)

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(a.out, buildinfo.String())
			return err
		},
	}
}
