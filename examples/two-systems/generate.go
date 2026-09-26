// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// This development tool generates synthetic summaries and optional OTLP fixtures.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/llm-measurement/fleetdiff/examples/internal/scenario"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

func main() {
	out := flag.String("out", "", "new summary directory (must not exist)")
	otlp := flag.String("otlp-out", "", "optional new OTLP fixture directory (must not exist)")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "provide -out with a new output directory")
		os.Exit(2)
	}
	if err := generate(*out, *otlp); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(output, otlp string) error {
	// Public key for reproducible synthetic data, never for real telemetry.
	if err := os.Setenv("FLEETDIFF_FIXTURE_KEY", "fleetdiff-public-fixture-key-not-for-production-2026"); err != nil {
		return err
	}
	secret, err := sketchhash.SecretFromEnv("FLEETDIFF_FIXTURE_KEY")
	if err != nil {
		return err
	}
	s, err := scenario.Load()
	if err != nil {
		return err
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	if otlp != "" {
		if err := os.Mkdir(otlp, 0700); err != nil {
			return err
		}
	}
	for i, window := range s.Windows {
		dir := filepath.Join(output, window.Name)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		for _, producer := range []string{"owned", "partner"} {
			e, err := s.Summary(window, producer, int64(i+1)*15_000_000_000, secret)
			if err != nil {
				return err
			}
			data, err := e.MarshalBinary()
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, producer+".json"), data, 0600); err != nil {
				return err
			}
			if otlp != "" {
				data, err := json.MarshalIndent(s.OTLP(i)[producer], "", "  ")
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(otlp, window.Name+"-"+producer+".json"), append(data, '\n'), 0600); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
