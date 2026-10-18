// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func TestStructuredModelDisplayNamesStayDistinct(t *testing.T) {
	for _, names := range [][]string{{"gpt-6.1-sol", "openai/gpt-6.1-sol"}, {"openai/gpt-6.1-sol", "gpt-6.1-sol"}} {
		for _, top := range []int{1, 20} {
			var rows []map[string]string
			for i, name := range names {
				r := routingSample(fmt.Sprint(i), fmt.Sprintf("2026-09-%02d", i+1), 100, 20)
				r["model"], r["custom_llm_provider"] = name, "openai"
				rows = append(rows, r)
			}
			o := routingOptions(t)
			o.Top = top
			r := routingReport(t, rows, o)
			if len(r.Models) != 2 || r.Models[0].Item == r.Models[1].Item || r.Before.Tokens != 120 || r.After.Tokens != 120 {
				t.Fatal("public model spelling merged identities or changed totals")
			}
			for _, model := range r.Models {
				if !strings.HasPrefix(model.Item, "gpt-6.1-sol [model-") {
					t.Fatal("ordinary model lost discriminator")
				}
			}
			seen := map[string]bool{}
			for _, model := range r.RoutingAudit.Cohorts[0].Models {
				if seen[model.Item] || !strings.HasPrefix(model.Item, "gpt-6.1-sol [selected-model-") {
					t.Fatal("routing model lost discriminator")
				}
				seen[model.Item] = true
			}
			wire, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var decoded Report
			if err := json.Unmarshal(wire, &decoded); err != nil || decoded.Models[0].Item == decoded.Models[1].Item {
				t.Fatal("JSON identities collided", err)
			}
		}
	}
}

func TestStructuredModelLabelsAndReaders(t *testing.T) {
	for _, tc := range []struct{ name, model, provider, want string }{
		{"bare", "gpt-6.1-sol", "", "gpt-6.1-sol"},
		{"qualified", "openai/gpt-6.1-sol", "openai", "gpt-6.1-sol"},
		{"missing", "openai/gpt-6.1-sol", "", ""},
		{"wrong", "openai/gpt-6.1-sol", "anthropic", ""},
		{"catalog-mismatch", "anthropic/gpt-6.1-sol", "anthropic", ""},
		{"padded", "openai/gpt-6.1-sol ", "openai", ""},
		{"nested", "openai/openai/gpt-6.1-sol", "openai", ""},
		{"private", "openai/PRIVATE_MODEL", "openai", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []map[string]string{routingSample("a", "2026-09-01", 100, 20), routingSample("b", "2026-09-02", 100, 20)}
			for _, row := range rows {
				row["model"], row["custom_llm_provider"] = tc.model, tc.provider
			}
			o := routingOptions(t)
			o.ShowHashes = true
			r := routingReport(t, rows, o)
			wantTotal, wantRouting := "model-1", "selected-model-1"
			if tc.want != "" {
				wantTotal, wantRouting = tc.want, tc.want
			}
			if r.Models[0].Item != wantTotal || r.RoutingAudit.Cohorts[0].Models[0].Item != wantRouting {
				t.Fatal("labels disagree with structured match")
			}
			for _, ext := range []string{".csv", ".json", ".jsonl"} {
				row := routingRepriceRow("synthetic")
				row["model"], row["custom_llm_provider"] = tc.model, tc.provider
				path := routingRepriceFile(t, ext, []map[string]any{row})
				for _, read := range []func(string, string, func(reprice.Record) error) (reprice.Source, error){ReadReprice, ReadRoutingReprice} {
					var records []reprice.Record
					_, err := read(path, repriceDay, func(r reprice.Record) error { records = append(records, r); return nil })
					want := tc.want
					if want == "" {
						want = tc.model
					}
					if err != nil || len(records) != 1 || records[0].Model != want {
						t.Fatal("reader identity mismatch", err)
					}
				}
			}
		})
	}
}

func TestStructuredModelProviderConflictStaysAliased(t *testing.T) {
	for _, providers := range [][]string{{"openai", "anthropic", "openai"}, {"", "openai", "openai"}} {
		var rows []map[string]string
		for i, provider := range providers {
			r := routingSample(fmt.Sprint(i), "2026-09-01", 100, 20)
			r["model"], r["custom_llm_provider"] = "openai/gpt-6.1-sol", provider
			rows = append(rows, r)
		}
		r := routingReport(t, rows, routingOptions(t))
		if len(r.Models) != 1 || r.Models[0].Item != "model-1" ||
			r.RoutingAudit.Cohorts[0].Models[0].Item != "selected-model-1" || r.Before.Tokens != 360 {
			t.Fatal("provider conflict changed accounting or restored public labels")
		}
	}
}
