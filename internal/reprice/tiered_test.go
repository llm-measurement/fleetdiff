// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tieredFixture() (Prices, Factors, Scenario) {
	p, f, s := fixture()
	p.Version = 2
	p.Models[0].Tiers = []PriceTier{{Input: "1", Output: "2", CacheRead: "0.1", CacheWrite: map[string]string{"5m": "1.25"}}}
	p.Models[0].Input, p.Models[0].Output, p.Models[0].CacheRead, p.Models[0].CacheWrite = "", "", "", ""
	p.Models[0].CacheWriteTTL = "5m"
	p.Models[1].Tiers = []PriceTier{
		{MaxPromptTokens: 100000, Input: "0.1", Output: "0.5", CacheRead: "0.01", CacheWrite: map[string]string{"5m": "0.125", "1h": "0.2"}},
		{Input: "0.5", Output: "2.5", CacheRead: "0.05", CacheWrite: map[string]string{"5m": "0.625", "1h": "1"}},
	}
	p.Models[1].Input, p.Models[1].Output, p.Models[1].CacheRead, p.Models[1].CacheWrite = "", "", "", ""
	p.Models[1].CacheWriteTTL = "5m"
	s.Moves[0].Fraction = "1"
	return p, f, s
}

func tierRecord(input uint64) Record {
	r := record()
	*r.Input = input
	*r.Output = 100
	*r.CacheRead = 20000
	*r.CacheWrite = 10000
	return r
}

func TestTieredRequestsPriceWholeRequestAtBoundary(t *testing.T) {
	p, f, s := tieredFixture()
	f.InputPairs[0].Factor = "1"
	c := calculator(t, p, f, s, WorkloadMap{})
	for _, input := range []uint64{100000, 100001} {
		if err := c.Add(tierRecord(input)); err != nil {
			t.Fatal(err)
		}
	}
	got := c.Result(Source{})
	move := got.Moves[0]
	if move.Level1 == nil || *move.Level1 != "0.051000500" {
		t.Fatalf("tiered cached input/output cost: %+v", move)
	}
	if len(move.TargetTiers) != 2 || move.TargetTiers[0].Tier != 1 ||
		move.TargetTiers[0].MaxPromptTokens == nil || *move.TargetTiers[0].MaxPromptTokens != 100000 ||
		move.TargetTiers[0].CoveredRequests != 1 || move.TargetTiers[1].Tier != 2 ||
		move.TargetTiers[1].MaxPromptTokens != nil || move.TargetTiers[1].CoveredRequests != 1 {
		t.Fatalf("per-request tier counts: %+v", move.TargetTiers)
	}
	encoded, err := json.Marshal(move)
	if err != nil || !strings.Contains(string(encoded), `"target_tiers":[{"tier":1,"max_prompt_tokens":100000,"covered_requests":1},{"tier":2,"covered_requests":1}]`) {
		t.Fatalf("tier evidence absent: %s %v", encoded, err)
	}
}

func TestTokenizerFactorSelectsTierBeforePricing(t *testing.T) {
	p, f, s := tieredFixture()
	c := calculator(t, p, f, s, WorkloadMap{}) // factor 2
	short := tierRecord(50000)
	*short.CacheRead, *short.CacheWrite = 0, 0
	long := tierRecord(50001)
	*long.CacheRead, *long.CacheWrite = 0, 0
	if err := c.Add(short); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(long); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if len(got.Moves[0].TargetTiers) != 2 || got.Moves[0].TargetTiers[0].CoveredRequests != 1 || got.Moves[0].TargetTiers[1].CoveredRequests != 1 {
		t.Fatalf("factor did not select one tier per call: %+v", got.Moves[0].TargetTiers)
	}
}

func TestTieredCalculatorKeepsItsPriceSnapshot(t *testing.T) {
	p, f, s := tieredFixture()
	f.InputPairs[0].Factor = "1"
	c := calculator(t, p, f, s, WorkloadMap{})
	p.Models[1].Tiers[1].Input = "999"
	p.Models[1].Tiers[1].CacheWrite["5m"] = "999"
	if err := c.Add(tierRecord(100001)); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if got.Moves[0].Level1 == nil || *got.Moves[0].Level1 != "0.042500500" {
		t.Fatalf("caller mutation changed an active calculator: %+v", got.Moves[0])
	}
}

func TestMissingTierAndMixedSummaryRefuse(t *testing.T) {
	p, f, s := tieredFixture()
	f.InputPairs[0].Factor = "1"
	p.Models[1].Tiers = p.Models[1].Tiers[:1]
	c := calculator(t, p, f, s, WorkloadMap{})
	if err := c.Add(tierRecord(100001)); err == nil || !strings.Contains(err.Error(), "a move exceeds the target's priced input tier") {
		t.Fatalf("unpriced higher tier: %v", err)
	}
	p.Models[1].Tiers = append(p.Models[1].Tiers, PriceTier{Input: "0.5", Output: "2.5", CacheRead: "0.05", CacheWrite: map[string]string{"5m": "0.625"}})
	c = calculator(t, p, f, s, WorkloadMap{})
	r := tierRecord(200000)
	lo, hi := uint64(99999), uint64(100001)
	r.Count, r.InputMin, r.InputMax = 2, &lo, &hi
	if err := c.Add(r); err == nil || !strings.Contains(err.Error(), "cohort crosses target price tiers") {
		t.Fatalf("mixed summary guessed a tier: %v", err)
	}
}

func TestVersionOneFlatPriceJSONRemainsUntiered(t *testing.T) {
	p, f, s := fixture()
	c := calculator(t, p, f, s, WorkloadMap{})
	if err := c.Add(record()); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	encoded, err := json.Marshal(got)
	if err != nil || got.PriceVersion != 1 || len(got.Moves[0].TargetTiers) != 0 || strings.Contains(string(encoded), "target_tiers") {
		t.Fatalf("v1 output changed: %s %v", encoded, err)
	}
}

func TestVersionOneOverrideKeepsPricedRange(t *testing.T) {
	bundled, factors, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	legacy := Prices{Version: 1, Date: "2026-10-14", Models: []Price{
		{ID: "claude-sonnet-5-5", Provider: "anthropic", Input: "2", Output: "10", CacheRead: "0.10", CacheWrite: "2.50"},
		{ID: "claude-haiku-5-5", Provider: "anthropic", Input: "0.10", Output: "0.50", CacheRead: "0.01", CacheWrite: "0.125", InputTierMax: 100000},
	}}
	p, err := OverridePrices(bundled, legacy)
	if err != nil {
		t.Fatal(err)
	}
	c := calculator(t, p, factors, Scenario{Version: 1, Moves: []Move{{From: "claude-sonnet-5-5", To: "claude-haiku-5-5", Fraction: "1"}}}, WorkloadMap{})
	r := tierRecord(100000)
	r.Model = "claude-sonnet-5-5"
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if got.PriceVersion != 1 || len(got.Moves[0].TargetTiers) != 0 {
		t.Fatalf("legacy output changed: %+v", got.Moves[0])
	}
	long := tierRecord(100001)
	long.Model = "claude-sonnet-5-5"
	if err := c.Add(long); err == nil || !strings.Contains(err.Error(), "target's priced input tier") {
		t.Fatalf("legacy limit was expanded: %v", err)
	}
}

func TestTierConfigurationRejectsMissingOrDisorderedRates(t *testing.T) {
	p, _, _ := tieredFixture()
	for _, change := range []func(*Prices){
		func(p *Prices) { p.Models[1].Tiers[1].MaxPromptTokens = 100000 },
		func(p *Prices) { delete(p.Models[1].Tiers[1].CacheWrite, "5m") },
		func(p *Prices) { p.Models[1].CacheWriteTTL = "unknown" },
		func(p *Prices) { p.Models[1].Tiers[0].Input = "-1" },
	} {
		copy := p
		copy.Models = append([]Price(nil), p.Models...)
		copy.Models[1].Tiers = append([]PriceTier(nil), p.Models[1].Tiers...)
		copy.Models[1].Tiers[1].CacheWrite = map[string]string{"5m": "0.625", "1h": "1"}
		change(&copy)
		if err := validatePrices(copy); err == nil {
			t.Fatal("invalid tiered prices accepted")
		}
	}
}

func TestTierConfigurationRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	data := []byte(`{"version":2,"date":"2026-11-19","models":[{"id":"a","provider":"anthropic","cache_write_ttl":"5m","tiers":[{"max_prompt_tokens":0,"input":"1","output":"1","cache_read":"0.1","cache_write":{"5m":"1.25"},"unknown_rate":"9"}]}]}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var p Prices
	if err := ReadConfig(path, &p); err == nil {
		t.Fatal("unknown tier field was accepted")
	}
}
