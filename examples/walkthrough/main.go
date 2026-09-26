// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// This example composes the real CLI into a short, reproducible investigation.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/llm-measurement/fleetdiff/examples/internal/scenario"
	"github.com/llm-measurement/fleetdiff/internal/cli"
	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func main() {
	out := flag.String("out", "", "new directory for example reports")
	live := flag.String("live", "", "output directory from the two-operator collector example")
	flag.Parse()
	if *out == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "run sh examples/demo.sh [--live]")
		os.Exit(2)
	}
	if err := run(*out, *live, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "walkthrough:", err)
		os.Exit(1)
	}
}

func invoke(before, after, expected string, extra ...string) (int, []byte, []byte) {
	var out, diagnostic bytes.Buffer
	args := []string{"compare", "--before", before, "--after", after, "--expected", expected, "--format", "json"}
	code := cli.Run(append(args, extra...), &out, &diagnostic)
	return code, out.Bytes(), diagnostic.Bytes()
}

func run(out, live string, output io.Writer) error {
	base, own, expected := "examples/two-systems/data", "owned", "owned,partner"
	if live != "" {
		base, own, expected = filepath.Join(live, "handoff"), "owned", "owned,partner"
	}
	before, after := filepath.Join(base, "before"), filepath.Join(base, "after")
	code, combinedJSON, _ := invoke(before, after, expected)
	if code != 0 {
		return errors.New("cannot compare example inputs")
	}
	code, ownedJSON, _ := invoke(filepath.Join(before, own+".json"), filepath.Join(after, own+".json"), own)
	if code != 0 {
		return errors.New("cannot compare the single-system example")
	}
	var combined, owned compare.Report
	if json.Unmarshal(combinedJSON, &combined) != nil || json.Unmarshal(ownedJSON, &owned) != nil {
		return errors.New("invalid example report")
	}
	if !combined.Complete || !owned.Complete || combined.Before.Usage == nil || combined.After.Usage == nil {
		return errors.New("example coverage is incomplete")
	}
	recipe, err := scenario.Load()
	if err != nil {
		return err
	}
	if err := recipe.CheckReport(combined); err != nil {
		return err
	}

	// Keep the full expected inventory while omitting the other system's export.
	code, rejected, diagnostic := invoke(before, filepath.Join(after, own+".json"), expected)
	if code != 1 || len(rejected) != 0 || !bytes.Contains(diagnostic, []byte("missing producers or partial observation intervals")) {
		return errors.New("missing-export example did not fail as expected")
	}
	code, partialJSON, _ := invoke(before, filepath.Join(after, own+".json"), expected, "--allow-partial")
	var partial compare.Report
	if code != 0 || json.Unmarshal(partialJSON, &partial) != nil || partial.Complete || len(partial.After.MissingProducers) != 1 {
		return errors.New("missing export was not marked partial")
	}
	code, fullText, _ := invoke(before, after, expected, "--format", "text")
	if code != 0 {
		return errors.New("cannot render full example report")
	}
	var transcript bytes.Buffer
	if err := render(&transcript, owned, combined); err != nil {
		return err
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return errors.New("reports need a new directory with an existing parent")
	}
	for name, data := range map[string][]byte{
		"comparison.json": combinedJSON, "comparison.txt": fullText,
		"owned-only.json": ownedJSON, "missing-operator.json": partialJSON,
		"walkthrough.txt": transcript.Bytes(),
	} {
		if err := os.WriteFile(filepath.Join(out, name), data, 0600); err != nil {
			return errors.New("cannot write example report")
		}
	}
	_, err = io.Copy(output, &transcript)
	return err
}

func tokens(report compare.Report) (uint64, uint64, error) {
	var before, after uint64
	fields := 0
	for _, c := range report.Counters {
		if c.Name == "input_tokens" || c.Name == "output_tokens" {
			// Each counter is bounded by MaxInt64 by the comparison contract.
			before += c.Before
			after += c.After
			fields++
		}
	}
	if fields != 2 {
		return 0, 0, errors.New("example is missing token counters")
	}
	return before, after, nil
}

func render(out io.Writer, owned, combined compare.Report) error {
	ob, oa, err := tokens(owned)
	if err != nil {
		return err
	}
	cb, ca, err := tokens(combined)
	if err != nil {
		return err
	}
	if oa >= ob || ca <= cb {
		return errors.New("example no longer demonstrates a local decrease and fleet increase")
	}
	fmt.Fprintln(out, "fleetdiff: did our research-agent change reduce work?")
	fmt.Fprintln(out, "A supervisor delegates to company- and partner-operated specialists.")
	fmt.Fprintln(out, "Synthetic batch jobs. No model calls or raw-trace upload.")
	fmt.Fprintln(out, "\n1. Did total reported usage fall?")
	fmt.Fprintf(out, "   Our reported tokens: %d -> %d (down %d)\n", ob, oa, ob-oa)
	fmt.Fprintf(out, "   Fleet reported tokens: %d -> %d (up %d)\n", cb, ca, ca-cb)
	fmt.Fprintln(out, "\n2. Did model activity rise while runs stayed flat?")
	fmt.Fprintf(out, "   Model requests: %d -> %d\n", combined.Before.Usage.Requests, combined.After.Usage.Requests)
	for _, c := range combined.Counters {
		if c.Name == "agent_runs" {
			fmt.Fprintf(out, "   Observed root-agent runs: %d -> %d\n", c.Before, c.After)
		}
	}
	fmt.Fprintf(out, "   Requests missing token usage: %d -> %d\n", combined.Before.Usage.Missing, combined.After.Usage.Missing)
	fmt.Fprintln(out, "\n3. Did the workload touch more documents?")
	for _, item := range []struct{ name, label string }{{"distinct_mcp_resources", "Distinct MCP resources"}, {"distinct_mcp_sessions", "Distinct MCP sessions"}, {"distinct_users", "Distinct users"}} {
		for _, d := range combined.Distinct {
			if d.Name == item.name {
				fmt.Fprintf(out, "   %s, estimated: %.0f -> %.0f\n", item.label, d.Before.Estimate, d.After.Estimate)
			}
		}
	}
	fmt.Fprintln(out, "\n4. Did particular tool-error signatures increase?")
	for _, c := range combined.Concentration {
		if c.Name != "top_tool_errors" {
			continue
		}
		for _, m := range c.Movers {
			fmt.Fprintf(out, "   %s: [%d, %d] -> [%d, %d] occurrences; %s\n", m.Item, m.Before.Lower, m.Before.Upper, m.After.Lower, m.After.Upper, m.Direction)
		}
	}
	fmt.Fprintln(out, "   Bounds are exact in this small fixture; tool identities stay hidden.")
	fmt.Fprintln(out, "\n5. Is the comparison missing a system?")
	fmt.Fprintln(out, "   Remove the partner's after-window export:")
	fmt.Fprintln(out, "   Normal comparison: refused because an expected system is missing.")
	fmt.Fprintln(out, "   With --allow-partial: marked incomplete, not treated as savings.")
	fmt.Fprintln(out, "\nReported work moved and grew; answer quality is outside what fleetdiff measures.")
	fmt.Fprintln(out, "This does not diagnose retries or prove savings. Missing tokens stay unknown.")
	return nil
}
