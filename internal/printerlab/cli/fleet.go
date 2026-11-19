package cli

import (
	"context"
	"sync"

	"github.com/cl-tim/hlab/internal/printerlab/inventory"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

// readFleet preserves inventory order and lets each selected printer report its
// own result, including when the command is cancelled while waiting to run.
func readFleet[T any](ctx context.Context, a *app, printers []inventory.Printer, read func(inventory.Printer, *moonraker.Client, error) T) []T {
	results := make([]T, len(printers))
	slots := make(chan struct{}, a.parallelism())
	var wg sync.WaitGroup
	for i, printer := range printers {
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
			}
			var client *moonraker.Client
			err := ctx.Err()
			if err == nil {
				client, err = moonraker.New(printer, a.timeout())
			}
			results[i] = read(printer, client, err)
		})
	}
	wg.Wait()
	return results
}
