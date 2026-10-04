// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Generate synthetic closed windows, without a collector or provider calls.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func main() {
	dir := flag.String("out", "", "new output directory")
	quiet := flag.Bool("quiet", false, "quiet series")
	flag.Parse()
	if *dir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "supply --out with a new directory")
		os.Exit(2)
	}
	if err := generate(*dir, *quiet); err != nil {
		fmt.Fprintln(os.Stderr, "cannot generate synthetic windows")
		os.Exit(1)
	}
}

func generate(dir string, quiet bool) error {
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC).UnixNano()
	for i := range 30 {
		input, missing := uint64(10000), uint64(0)
		spike := !quiet && i >= 28
		if spike {
			input = 31000
		}
		if !quiet && i == 29 {
			missing = 40
			input = 6000
		}
		counters := map[string]uint64{"requests": 100, "input_tokens": input, "output_tokens": 0, "missing_token_usage": missing}
		payloads := map[string]summary.Payload{}
		for _, entry := range []struct {
			name   string
			domain sketchhash.Domain
		}{{"top_sessions", sketchhash.SessionV1}, {"top_sessions_requests", sketchhash.SessionV1}, {"top_tool_errors", sketchhash.ToolErrorV1}} {
			s, err := frequentitems.New("micro", entry.domain, sketchhash.HMACSHA25664)
			if err != nil {
				return err
			}
			switch entry.name {
			case "top_tool_errors":
				if spike {
					if err := s.AddHash(42, 40); err != nil {
						return err
					}
				}
			default:
				total, head := int64(input), int64(0)
				if entry.name == "top_sessions_requests" {
					total = 100
				}
				if spike {
					head = total * 62 / 100
					if err := s.AddHash(999, head); err != nil {
						return err
					}
				}
				remaining := total - head
				for k := range 10 {
					weight := remaining / 10
					if k == 9 {
						weight += remaining % 10
					}
					if err := s.AddHash(uint64(k+1), weight); err != nil {
						return err
					}
				}
				counters["topk_contract.v1."+entry.name+".00000000000000000000000000000000"] = 0
			}
			data, err := s.MarshalBinary()
			if err != nil {
				return err
			}
			payloads[entry.name] = summary.Payload{Kind: "frequent_items", Data: data}
		}
		window := start + int64(i)*int64(time.Minute)
		e := summary.Envelope{Version: 1, ProducerID: "app", Epoch: "synthetic", ScopeID: "demo", AccountingID: "synthetic-scan-v1", KeyID: "synthetic-key", Sequence: 1,
			WindowStart: window, WindowDuration: int64(time.Minute), ObservedStart: window, ObservedEnd: window + int64(time.Minute), EmittedAt: window + int64(time.Minute), Counters: counters, Sketches: payloads}
		data, err := e.MarshalBinary()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("window-%02d.json", i)), data, 0600); err != nil {
			return err
		}
	}
	return nil
}
