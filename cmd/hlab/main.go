// Command hlab is a CLI for managing a homelab fleet over SSH.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cl-tim/hlab/internal/cli"
	"github.com/cl-tim/hlab/internal/printerlab/migrate"
)

func main() {
	if handled, code := migrate.MaybeRunAskpass(os.Args[1:], os.Stdout); handled {
		os.Exit(code)
	}

	// Ctrl-C cancels in-flight SSH work rather than leaving sessions dangling.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if handled, code := migrate.MaybeRunBackend(ctx, os.Args[1:], os.Stdout, os.Stderr); handled {
		os.Exit(code)
	}

	if err := cli.Execute(ctx); err != nil {
		// Host and printer commands return the same ExitError type.
		var exitErr *cli.ExitError
		if errors.As(err, &exitErr) {
			if exitErr.Err != nil {
				fmt.Fprintln(os.Stderr, "hlab:", exitErr.Err)
			}
			os.Exit(exitErr.Code)
		}
		fmt.Fprintln(os.Stderr, "hlab:", err)
		os.Exit(1)
	}
}
