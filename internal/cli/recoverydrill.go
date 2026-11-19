package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/health"
	"github.com/cl-tim/hlab/internal/inventory"
)

func newRecoveryDrillCmd(a *app) *cobra.Command {
	var from string
	var apply bool
	cmd := &cobra.Command{
		Use:   "drill <host>",
		Short: "Restore an archive on a disposable recovery host and check its services",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, ok := a.cfg.Inventory.Host(args[0])
			if !ok || !host.Managed {
				return fmt.Errorf("%q is not a managed inventory host", args[0])
			}
			if !host.HasTag("recovery-drill") {
				return fmt.Errorf("%s must have the recovery-drill inventory tag", host.Name)
			}
			if len(a.cfg.Endpoints[host.Name]) == 0 {
				return fmt.Errorf("%s needs at least one configured service endpoint", host.Name)
			}
			data, err := os.ReadFile(from)
			if err != nil {
				return err
			}
			sum, err := verifyStoredArchive(from, data)
			if err != nil {
				return err
			}
			if err := checkArchiveReadable(data); err != nil {
				return fmt.Errorf("read archive: %w", err)
			}
			fmt.Fprintf(a.errOut, "Recovery drill: %s -> %s\n", from, host.Name)
			if !apply {
				return nil
			}
			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			if err := a.restoreHost(cmd.Context(), dialer, host, data, sum); err != nil {
				return err
			}
			reports, err := a.collectHealth(cmd.Context(), []inventory.Host{host}, health.DefaultThresholds())
			if err != nil {
				return err
			}
			if err := a.reportHealth(reports, false); err != nil {
				return err
			}
			if reports[0].Partial {
				return fmt.Errorf("recovery health check returned an incomplete report")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "backup archive to exercise")
	cmd.Flags().BoolVar(&apply, "apply", false, "restore and start services on the disposable host")
	_ = cmd.MarkFlagRequired("from")
	return cmd
}
