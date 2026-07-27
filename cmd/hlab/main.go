// Command hlab is a CLI for managing a homelab fleet over SSH.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cl-tim/homelab-cli/internal/cli"
)

func main() {
	// Ctrl-C cancels in-flight SSH work rather than leaving sessions dangling.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cli.Execute(ctx); err != nil {
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
