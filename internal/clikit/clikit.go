// Package clikit holds the pieces of command plumbing that the host command
// tree and the printer command tree must agree on, so that main has one
// contract to honour whichever half a command came from.
package clikit

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/buildinfo"
	"github.com/cl-tim/hlab/internal/output"
)

// ExitError carries a specific process exit code up to main. Commands return
// it rather than calling os.Exit, so deferred cleanup still runs.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

func (e *ExitError) Unwrap() error { return e.Err }

// NewVersionCmd builds a `version` command. format is read when the command
// runs, after the caller's pre-run hook has parsed --output.
func NewVersionCmd(out io.Writer, format func() output.Format) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the hlab version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if f := format(); f.Structured() {
				return output.Write(out, f, buildinfo.Current(), output.Table{})
			}
			_, err := fmt.Fprintln(out, buildinfo.String())
			return err
		},
	}
}
