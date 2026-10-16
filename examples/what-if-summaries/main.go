// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Generate synthetic canonical model-usage summaries without a collector or provider calls.
package main

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func main() {
	dir := flag.String("out", "", "new output directory")
	flag.Parse()
	if *dir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "supply --out with a new directory")
		os.Exit(2)
	}
	contract, err := generate(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot generate synthetic summaries")
		os.Exit(1)
	}
	fmt.Println(contract)
}

type event struct {
	model string
	usage modelusage.Attempt
}

const demoAccountingID = "synthetic-model-usage-v1"

func usage(input, output, read, write uint64) modelusage.Attempt {
	return modelusage.Attempt{
		Input: modelusage.Observation{Value: &input}, Output: modelusage.Observation{Value: &output},
		CacheRead: modelusage.Observation{Value: &read}, CacheWrite: modelusage.Observation{Value: &write},
		InputOrigin: "provider_reported", OutputOrigin: "provider_reported",
	}
}

func fixtures() (map[string]summary.Envelope, string, error) {
	mapping := map[string]string{"demo-sonnet": "claude-sonnet-5-5", "demo-sol": "gpt-6.1-sol"}
	routing := modelusage.Routing{Algorithm: "inactive-lru-v1", MaxLabelBytes: 256, MaxSlices: 16, SelectedSlice: "model", Slices: []modelusage.Slice{
		{Name: "model", Keys: []string{"gen_ai.request.model"}, FromResourceAttributes: []string{"gen_ai.request.model"}},
	}}
	contract, err := modelusage.ContractID(demoAccountingID, mapping, routing)
	if err != nil {
		return nil, "", err
	}
	start := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC).UnixNano()
	snapshot := func(producer string, sequence uint64, observedEnd int64, events []event) (summary.Envelope, error) {
		block, err := modelusage.New(contract)
		if err != nil {
			return summary.Envelope{}, err
		}
		counters := map[string]uint64{"requests": 0, "input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_write_input_tokens": 0, "missing_token_usage": 0}
		for _, event := range events {
			id, err := modelusage.ClassifyModel(event.model, mapping, "")
			if err != nil {
				return summary.Envelope{}, err
			}
			row, err := modelusage.AddAttempt(block.Models[id], event.usage)
			if err != nil {
				return summary.Envelope{}, err
			}
			block.Models[id] = row
			if event.usage.Input.Value == nil || event.usage.Output.Value == nil {
				counters["missing_token_usage"]++
			}
		}
		for _, row := range block.Models {
			for name, count := range map[string]uint64{"requests": row.Requests, "input_tokens": row.Fields.Input.Tokens, "output_tokens": row.Fields.Output.Tokens, "cache_read_input_tokens": row.Fields.CacheRead.Tokens, "cache_write_input_tokens": row.Fields.CacheWrite.Tokens} {
				counters[name], err = accounting.AddBounded(counters[name], count)
				if err != nil {
					return summary.Envelope{}, err
				}
			}
		}
		return summary.Envelope{Version: 1, AccountingID: demoAccountingID, KeyID: "synthetic-key", ScopeID: "what-if-summaries-demo", ProducerID: producer, Epoch: "synthetic-epoch", Sequence: sequence,
			WindowStart: start, WindowDuration: int64(time.Minute), ObservedStart: start, ObservedEnd: observedEnd, EmittedAt: observedEnd,
			Counters: counters, Sketches: map[string]summary.Payload{}, ModelUsage: block}, nil
	}
	first := event{"demo-sonnet", usage(90000, 900, 30000, 10000)}
	missing := usage(1000, 10, 0, 0)
	missing.CacheWrite.Value = nil
	zero := usage(0, 0, 0, 0)
	zero.InputOrigin, zero.OutputOrigin = "unknown", "unknown"
	old, err := snapshot("demo-a", 1, start+int64(30*time.Second), []event{first})
	if err != nil {
		return nil, "", err
	}
	latest, err := snapshot("demo-a", 2, start+int64(time.Minute), []event{
		first, {"demo-sonnet", usage(100000, 1000, 30000, 10000)}, {"demo-sonnet", usage(150000, 1500, 50000, 10000)},
		{"demo-sonnet", missing}, {"demo-sonnet", usage(100, 10, 80, 30)}, {"demo-sonnet", zero}, {"demo-unmapped", usage(250, 10, 0, 0)},
	})
	if err != nil {
		return nil, "", err
	}
	other, err := snapshot("demo-b", 1, start+int64(time.Minute), []event{{"demo-sonnet", usage(160000, 1600, 40000, 20000)}, {"demo-sol", usage(2000, 400, 0, 0)}})
	if err != nil {
		return nil, "", err
	}
	return map[string]summary.Envelope{"a-old.json": old, "a-latest.json": latest, "a-replay.json": latest, "b.json": other}, contract, nil
}

func generate(dir string) (string, error) {
	docs, contract, err := fixtures()
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	for _, name := range slices.Sorted(maps.Keys(docs)) {
		data, err := docs[name].MarshalBinary()
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "contract.txt"), []byte(contract+"\n"), 0600); err != nil {
		return "", err
	}
	return contract, nil
}
