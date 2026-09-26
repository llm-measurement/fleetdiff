// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/llm-measurement/fleetdiff/examples/internal/scenario"
	"github.com/llm-measurement/fleetdiff/internal/cli"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func invoke(before, after string, extra ...string) (int, []byte, []byte) {
	var out, errout bytes.Buffer
	args := []string{"compare", "--before", before, "--after", after, "--expected", "owned,partner", "--format", "json"}
	code := cli.Run(append(args, extra...), &out, &errout)
	return code, out.Bytes(), errout.Bytes()
}

func saveJSON(path string, value any) error {
	var data bytes.Buffer
	if err := writeJSON(&data, value); err != nil {
		return err
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		return errors.New("cannot write private report")
	}
	return nil
}

func checkHandoff(out, image string, start time.Time, secret sketchhash.Secret) error {
	before, after := filepath.Join(out, "handoff", "before"), filepath.Join(out, "handoff", "after")
	code, data, diagnostic := invoke(before, after)
	if code != 0 || len(diagnostic) != 0 {
		return errors.New("fleetdiff could not compare real collector exports")
	}
	var r compare.Report
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if !r.Complete || r.Before.SelectedSnapshots != 2 || r.After.SelectedSnapshots != 2 {
		return errors.New("incomplete combined observation intervals")
	}
	recipe, err := scenario.Load()
	if err != nil {
		return err
	}
	if err := recipe.CheckReport(r); err != nil {
		return err
	}
	for _, c := range r.Concentration {
		for _, m := range c.Movers {
			if m.Hash != "" {
				return errors.New("default comparison exposed a candidate hash")
			}
		}
	}
	code, hashData, hashDiagnostic := invoke(before, after, "--show-hashes")
	var hashed compare.Report
	if code != 0 || len(hashDiagnostic) != 0 || json.Unmarshal(hashData, &hashed) != nil {
		return errors.New("cannot verify synthetic signature identities")
	}
	if err := recipe.CheckSignatures(hashed, secret); err != nil {
		return err
	}
	if err := noPrivateContent(hashData); err != nil {
		return err
	}
	if err := noPrivateContent(data); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "comparison.json"), data, 0600); err != nil {
		return err
	}
	code, text, _ := invoke(before, after, "--format", "text")
	if code != 0 {
		return errors.New("text comparison failed")
	}
	if err := noPrivateContent(text); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "comparison.txt"), text, 0600); err != nil {
		return err
	}
	code, localData, _ := invoke(filepath.Join(before, "owned.json"), filepath.Join(after, "owned.json"), "--expected", "owned")
	var local compare.Report
	if code != 0 || json.Unmarshal(localData, &local) != nil || !local.Complete || local.After.Usage == nil {
		return errors.New("owned-only comparison failed")
	}
	var localBefore, localAfter uint64
	for _, c := range local.Counters {
		if c.Name == "input_tokens" || c.Name == "output_tokens" {
			localBefore += c.Before
			localAfter += c.After
		}
	}
	if localBefore != recipe.Windows[0].Expected.OwnedTokens || localAfter != recipe.Windows[1].Expected.OwnedTokens {
		return errors.New("owned-only comparison did not show the planted local decrease")
	}
	if err := noPrivateContent(localData); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "owned-only.json"), localData, 0600); err != nil {
		return err
	}

	docs, err := compare.ReadWindow(after, nil)
	if err != nil {
		return err
	}
	// Counterexamples are deliberately edited copies, never the handoff originals.
	for _, kind := range []string{"key", "scope", "accounting", "partial", "missing"} {
		dir := filepath.Join(out, "counterexamples", kind)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		for _, doc := range docs {
			if doc.ProducerID == "partner" {
				switch kind {
				case "key":
					doc.KeyID = "FC_PRIVATE_DIAGNOSTIC"
				case "scope":
					doc.ScopeID = "FC_PRIVATE_DIAGNOSTIC"
				case "accounting":
					doc.AccountingID = "FC_PRIVATE_DIAGNOSTIC"
				case "partial":
					doc.ObservedStart++
				case "missing":
					continue
				}
			}
			encoded, err := doc.MarshalBinary()
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, doc.ProducerID+".json"), encoded, 0600); err != nil {
				return err
			}
		}
		code, output, diagnostics := invoke(before, dir)
		if code == 0 || len(output) != 0 || len(diagnostics) == 0 {
			return errors.New("incompatible or incomplete input was not rejected atomically")
		}
		if err := noPrivateContent(diagnostics); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(out, kind+"-rejection.txt"), diagnostics, 0600); err != nil {
			return err
		}
		if kind == "missing" || kind == "partial" {
			code, output, _ = invoke(before, dir, "--allow-partial")
			var partial compare.Report
			if code != 0 || json.Unmarshal(output, &partial) != nil || partial.Complete {
				return errors.New("explicitly partial comparison was not marked partial")
			}
			if kind == "missing" && (len(partial.After.MissingProducers) != 1 || partial.After.Usage == nil || partial.After.Usage.Requests != local.After.Usage.Requests) {
				return errors.New("missing producer became zero or disappeared")
			}
			if kind == "partial" && len(partial.After.PartialProducers) != 1 {
				return errors.New("partial producer disappeared")
			}
			name := "partial-interval.json"
			if err := noPrivateContent(output); err != nil {
				return err
			}
			if kind == "missing" {
				name = "missing-operator.json"
			}
			if err := os.WriteFile(filepath.Join(out, name), output, 0600); err != nil {
				return err
			}
		} else {
			code, output, _ = invoke(before, dir, "--allow-partial")
			if code == 0 || len(output) != 0 {
				return errors.New("allow-partial bypassed compatibility")
			}
		}
	}
	prior, err := compare.ReadWindow(before, nil)
	if err != nil {
		return err
	}
	replay := filepath.Join(out, "counterexamples", "replay")
	if err := os.Mkdir(replay, 0700); err != nil {
		return err
	}
	for i, doc := range append(prior, prior[0]) {
		encoded, err := doc.MarshalBinary()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(replay, fmt.Sprintf("%d.json", i)), encoded, 0600); err != nil {
			return err
		}
	}
	code, replayData, _ := invoke(replay, after)
	if code != 0 || !bytes.Equal(data, replayData) {
		return errors.New("replayed snapshot changed the comparison")
	}
	return saveJSON(filepath.Join(out, "checks.json"), struct {
		Collector string   `json:"collector_image"`
		Before    string   `json:"before_window"`
		After     string   `json:"after_window"`
		Synthetic bool     `json:"synthetic_otlp_not_provider_certification"`
		Passed    []string `json:"passed"`
	}{image, start.Format(time.RFC3339Nano), start.Add(windowDuration).Format(time.RFC3339Nano), true, []string{
		"two released collectors; no raw traces in handoff", "owned-only usage falls but the combined scope rises", "model requests rise from six to ten; non-model spans excluded",
		"two supervisor roots per window; cross-operator specialists do not add runs", "shared MCP resources merge once; distinct resources rise from one to four",
		"MCP sessions rise from four to eight", "search timeout signature is the top tool-error mover; other signature bounds reconcile",
		"reported token and subset accounting", "missing token coverage remains explicit", "shared identities merge across operators",
		"tracked change intervals match exact fixture", "replay leaves the report byte-identical", "key/scope/accounting mismatches reject even with allow-partial",
		"missing and partial operators require explicit opt-in", "raw sentinels absent from metrics, logs, summaries, decoded state, reports, and diagnostics",
		"top-k log snapshot present; candidate hashes absent from metrics", "label overflow exercised without losing scope totals",
	}})
}

func checkMetricHashes(dir string, metrics []byte) error {
	seen := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		doc, err := summary.Parse(data)
		if err != nil {
			return err
		}
		if err := noPrivateContent(data); err != nil {
			return err
		}
		for name, payload := range doc.Sketches {
			if err := noPrivateContent(payload.Data); err != nil {
				return err
			}
			if payload.Kind != "frequent_items" {
				continue
			}
			sketch, err := frequentitems.Parse(payload.Data)
			if err != nil {
				return err
			}
			items, err := sketch.FrequentItems(frequentitems.NoFalseNegatives)
			if err != nil {
				return err
			}
			for _, item := range items {
				seen[name] = true
				if bytes.Contains(metrics, []byte(fmt.Sprintf("%016x", item.Hash))) || bytes.Contains(metrics, []byte(fmt.Sprintf("%d", item.Hash))) {
					return errors.New("tracked hash leaked to metrics or labels")
				}
			}
		}
	}
	if !seen["top_prompts"] || !seen["top_tool_errors"] {
		return errors.New("metric privacy scan did not exercise both frequent-item surfaces")
	}
	return nil
}
