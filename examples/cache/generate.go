// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Generate counter-only synthetic summaries, not captured provider traffic.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func main() {
	out := flag.String("out", "", "new summary directory (must not exist)")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "provide -out with a new output directory")
		os.Exit(2)
	}
	if err := generate(*out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func generate(output string) error {
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	for side, name := range []string{"before", "after"} {
		const duration = int64(time.Minute)
		start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC).UnixNano() + int64(side)*duration
		doc := summary.Envelope{
			Version: 1, Sequence: 1, ProducerID: "app", Epoch: "synthetic-cache",
			ScopeID: "cache-demo", KeyID: "public-synthetic-key", AccountingID: "synthetic-cache-v1",
			WindowStart: start, WindowDuration: duration, ObservedStart: start,
			ObservedEnd: start + duration, EmittedAt: start + duration,
			Counters: map[string]uint64{
				"requests": 10, "input_tokens": 1000, "output_tokens": 200,
				"missing_token_usage": 0, "cache_read_input_tokens": 600,
			},
			Sketches: map[string]summary.Payload{},
		}
		if side == 1 {
			doc.Counters["cache_read_input_tokens"] = 200
		}
		for _, field := range []string{"input", "cache_read_input"} {
			for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
				var count uint64
				if state == "reported" {
					count = doc.Counters["requests"]
				}
				doc.Counters["token_observations."+field+"."+state] = count
			}
		}
		data, err := doc.MarshalBinary()
		if err != nil {
			return err
		}
		dir := filepath.Join(output, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "app.json"), data, 0600); err != nil {
			return err
		}
	}
	return nil
}
