// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/attribution"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func TestModelAliasesAcrossRankings(t *testing.T) {
	root := t.TempDir()
	var body strings.Builder
	for _, row := range []struct {
		id, day, model       string
		input, cache, output int
	}{
		{"a0", "2026-10-10", "PRIVATE_A", 100, 900, 100},
		{"b0", "2026-10-10", "PRIVATE_B", 100, 900, 100},
		{"c0", "2026-10-10", "PRIVATE_C", 100, 900, 100},
		{"a1", "2026-10-17", "PRIVATE_A", 100, 900, 9000},
		{"b1", "2026-10-17", "PRIVATE_B", 900, 100, 100},
		{"c1", "2026-10-17", "PRIVATE_C", 500, 500, 2000},
	} {
		var value map[string]any
		if err := json.Unmarshal([]byte(rowJSON(row.id, "project", row.day)), &value); err != nil {
			t.Fatal(err)
		}
		value["provided_model_name"] = row.model
		value["usage_details"] = map[string]int{"input": row.input, "input_cached_tokens": row.cache, "output": row.output, "total": row.input + row.cache + row.output}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(encoded)
		body.WriteByte('\n')
	}
	addRun(t, root, "project", "models", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body.String(), 6)
	for _, hashes := range []bool{false, true} {
		for _, top := range []int{1, 20} {
			for _, fixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("hashes-%t/top-%d/fixed-%t", hashes, top, fixed), func(t *testing.T) {
					opts := options()
					opts.ShowHashes, opts.Top = hashes, top
					if fixed {
						t.Setenv("FLEETDIFF_TEST_MODEL_ALIASES", "local-model-alias-regression-key")
						opts.SecretEnv = "FLEETDIFF_TEST_MODEL_ALIASES"
					}
					r, err := Investigate(root, "provided_model_name", "", opts)
					if err != nil {
						t.Fatal(err)
					}
					if r.Before.Tokens != 3300 || r.After.Tokens != 14100 || len(r.Models) != 3 || r.Cache.Rankings == nil {
						t.Fatal("alias selection changed totals or cache coverage")
					}
					for i, model := range r.Models {
						if model.Item != fmt.Sprintf("model-%d", i+1) {
							t.Fatal("headline aliases changed")
						}
					}
					if r.Rankings.Movers[0].Item != "model-1" || r.Cache.Rankings.Movers[0].Item != "model-3" {
						t.Fatalf("aliases follow ranking positions instead of model identities: %s / %s", r.Rankings.Movers[0].Item, r.Cache.Rankings.Movers[0].Item)
					}
					encoded, err := json.Marshal(r)
					if err != nil || strings.Contains(string(encoded), "PRIVATE_") {
						t.Fatal("private label escaped", err)
					}
				})
			}
		}
	}
}

func TestModelAliasesPreserveProjectScope(t *testing.T) {
	root := t.TempDir()
	for _, project := range []string{"project-a", "project-b"} {
		addRun(t, root, project, "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", rowJSON("one", project, "2026-10-10")+rowJSON("two", project, "2026-10-17"), 2)
	}
	const secret = "local-model-alias-regression-key"
	t.Setenv("FLEETDIFF_TEST_MODEL_ALIASES", secret)
	opts := options()
	opts.ShowHashes, opts.SecretEnv = true, "FLEETDIFF_TEST_MODEL_ALIASES"
	r, err := Investigate(root, "provided_model_name", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Models) != 1 || len(r.Rankings.Movers) != 2 || r.Before.Tokens != 280 || r.After.Tokens != 280 {
		t.Fatal("global model totals or project-scoped contributors changed")
	}
	modelHash, err := attribution.Identity([]byte(secret), "litellm-spend/v1", "model", "PRIVATE_MODEL")
	if err != nil || r.Models[0].Hash != fmt.Sprintf("%x", modelHash) {
		t.Fatal("model hash scope changed", err)
	}
	seen := map[string]bool{}
	for _, mover := range r.Rankings.Movers {
		if !strings.HasPrefix(mover.Item, "model-1 [group-") || seen[mover.Item] || mover.Before.Lower != 140 || mover.After.Lower != 140 {
			t.Fatal("separate project groups lost their model alias or disambiguation", mover)
		}
		seen[mover.Item] = true
	}
	for _, project := range []string{"project-a", "project-b"} {
		hash, err := attribution.Identity([]byte(secret), "litellm-spend/v1", "key", framed("langfuse", project, "PRIVATE_MODEL"))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, mover := range r.Rankings.Movers {
			found = found || mover.Hash == fmt.Sprintf("%016x", binary.BigEndian.Uint64(hash[:8]))
		}
		if !found {
			t.Fatal("project-scoped keyed identity changed")
		}
	}
}

func TestModelLabelsPreservePublicNames(t *testing.T) {
	prices, _, err := reprice.Bundled()
	if err != nil || len(prices.Models) == 0 {
		t.Fatal("need a bundled model", err)
	}
	model := prices.Models[0].ID
	body := strings.ReplaceAll(rowJSON("one", "project", "2026-10-10")+rowJSON("two", "project", "2026-10-17"), "PRIVATE_MODEL", model)
	root := t.TempDir()
	addRun(t, root, "project", "known-model", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 2)
	r, err := Investigate(root, "provided_model_name", "", options())
	if err != nil || len(r.Models) != 1 || len(r.Rankings.Movers) != 1 || r.Models[0].Item != model || r.Rankings.Movers[0].Item != model {
		t.Fatal("headline and contributor did not preserve the same public model label", err)
	}
	encoded, err := json.Marshal(r)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("private label escaped alongside the public model label", err)
	}
}
