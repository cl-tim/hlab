package cli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/output"
	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status [selector]",
		Short: "Read live state from Moonraker",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printers, err := a.selectPrinters(args)
			if err != nil {
				return err
			}
			snapshots := collectSnapshots(cmd.Context(), printers, a.timeout(), a.parallelism())
			table := output.Table{
				Headers: []string{"NAME", "FIRMWARE", "KLIPPY", "PRINT", "PROGRESS", "HOTEND", "BED", "RESULT"},
			}
			failed := false
			for _, snapshot := range snapshots {
				result := "ok"
				if !snapshot.Reachable || snapshot.Error != "" {
					result = snapshot.Error
					failed = true
				} else if snapshot.KlippyState != "ready" {
					result = "Klippy state is " + snapshot.KlippyState
					if snapshot.KlippyStateMessage != "" {
						result += ": " + snapshot.KlippyStateMessage
					}
					failed = true
				}
				table.AddRow(
					snapshot.Name,
					snapshot.Firmware,
					snapshot.KlippyState,
					snapshot.PrintState,
					fmt.Sprintf("%.1f%%", snapshot.Progress*100),
					fmt.Sprintf("%.1f/%.1f", snapshot.ExtruderTemp, snapshot.ExtruderTarget),
					fmt.Sprintf("%.1f/%.1f", snapshot.BedTemp, snapshot.BedTarget),
					result,
				)
			}
			if err := output.Write(a.out, a.format, snapshots, table); err != nil {
				return err
			}
			if failed {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
}

func collectSnapshots(
	ctx context.Context,
	printers []inventory.Printer,
	timeout time.Duration,
	concurrency int,
) []moonraker.Snapshot {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]moonraker.Snapshot, len(printers))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for index, printer := range printers {
		wg.Add(1)
		go func(i int, p inventory.Printer) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = moonraker.Snapshot{
					Name: p.Name, Address: p.Address, Firmware: p.Firmware, Error: ctx.Err().Error(),
				}
				return
			}
			client, err := moonraker.New(p, timeout)
			if err != nil {
				results[i] = moonraker.Snapshot{
					Name: p.Name, Address: p.Address, Firmware: p.Firmware, Error: err.Error(),
				}
				return
			}
			results[i] = client.Snapshot(ctx, p)
		}(index, printer)
	}
	wg.Wait()
	return results
}
