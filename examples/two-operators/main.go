// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// This example owns the Docker/HTTP effects; the fleetdiff command remains file-only.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

//go:embed fixtures/*.json collector.yaml image.txt
var assets embed.FS

const windowDuration = 15 * time.Second

func main() {
	out := flag.String("out", "two-operator-run", "new private output directory; existing paths are refused")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected arguments")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := runDemo(ctx, *out, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "two-operator example:", err)
		os.Exit(1)
	}
}
