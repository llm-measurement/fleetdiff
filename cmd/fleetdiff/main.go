// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/llm-measurement/fleetdiff/internal/cli"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/postgres"
)

func main() { os.Exit(run()) }

func run() int {
	return runWithInput(os.Stdin)
}

func runWithInput(in io.Reader) int {
	return cli.RunWithExporter(context.Background(), os.Args[1:], in, os.Stdout, os.Stderr, withExportSignals(postgres.Export))
}

func withExportSignals(exporter exportdata.Exporter) exportdata.Exporter {
	return func(ctx context.Context, opts exportdata.Options) (exportdata.Result, error) {
		ctx, cancel := context.WithCancel(ctx)
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
		stopped := make(chan struct{})
		go func() {
			// The first signal requests cleanup. A repeated signal may terminate
			// an unresponsive exporter and leave owner-only temporary data.
			select {
			case <-interrupts:
				signal.Stop(interrupts)
				cancel()
			case <-ctx.Done():
				signal.Stop(interrupts)
			}
			close(stopped)
		}()
		defer func() { cancel(); <-stopped }()
		return exporter(ctx, opts)
	}
}
