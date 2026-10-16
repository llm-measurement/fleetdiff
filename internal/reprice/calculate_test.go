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

func fixture() (Prices, Factors, Scenario) {
	p := Prices{Version: 1, Date: "2026-10-14", Models: []Price{{ID: "a", Provider: "provider-a", Input: "1", Output: "2", CacheRead: "0.1", CacheWrite: "1.25"}, {ID: "b", Provider: "provider-b", Input: "2", Output: "4", CacheRead: "0.2", CacheWrite: "2.5"}, {ID: "c", Provider: "provider-a", Input: "2", Output: "4", CacheRead: "0.2", CacheWrite: "2.5"}}}
	f := Factors{Version: 1, Date: "2026-10-14", InputPairs: []InputFactor{{"a", "b", "2", 39, "record-a-b"}}}
	for _, w := range Workloads {
		f.Pairs = append(f.Pairs, Factor{"a", "b", w, "2", "1.5", "2.5", 39, "record-" + w})
	}
	return p, f, Scenario{1, []Move{{From: "a", To: "b", Fraction: "0.5"}}}
}
func record() Record {
	i, o, r, w := uint64(100), uint64(50), uint64(20), uint64(10)
	return Record{Model: "a", Key: "private-key-sentinel", Team: "private-team-sentinel", Input: &i, Output: &o, CacheRead: &r, CacheWrite: &w, Count: 1}
}
func calculator(t *testing.T, p Prices, f Factors, s Scenario, m WorkloadMap) *Calculator {
	t.Helper()
	c, err := New(p, f, s, m)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestInputPriceTierBoundaries(t *testing.T) {
	for _, target := range []bool{false, true} {
		for _, outside := range []bool{false, true} {
			p, f, s := fixture()
			i, limit := 0, uint64(100)
			if target {
				i, limit = 1, 200
			}
			p.Models[i].InputTierMax = limit
			c := calculator(t, p, f, s, WorkloadMap{})
			r := record()
			if outside {
				*r.Input++
			}
			err := c.Add(r)
			if target && outside {
				if err == nil {
					t.Fatal("target tier overflow accepted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			got := c.Result(Source{})
			if outside {
				if got.Before != nil || got.Excluded["outside_price_tier"] != 1 {
					t.Fatalf("source tier: %+v", got)
				}
			} else if got.Covered != 1 {
				t.Fatal("inclusive input tier boundary rejected")
			}
		}
	}
}

func TestPartialMappingPreservesThreeUnmappedAlternatives(t *testing.T) {
	p, f, s := fixture()
	f.Pairs[1].Output, f.Pairs[1].High = "3", "4"
	c := calculator(t, p, f, s, WorkloadMap{1, []Mapping{{Selector{"key", "private-key-sentinel"}, "extraction"}}})
	if err := c.Add(record()); err != nil {
		t.Fatal(err)
	}
	r := record()
	r.Key = "unmapped"
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if len(got.Level2) != 3 || got.Unmapped != 1 || got.Level2[0].Mean != "0.001022500" || got.Level2[1].Mean != "0.001122500" {
		t.Fatalf("partial mapping: %+v", got)
	}
}

func TestAcrossProviderLevels(t *testing.T) {
	p, f, s := fixture()
	c := calculator(t, p, f, s, WorkloadMap{})
	if err := c.Add(record()); err != nil {
		t.Fatal(err)
	}
	r := c.Result(Source{})
	if *r.Before != "0.000184500" || *r.Level1 != "0.000361250" || len(r.Level2) != 3 || r.Level2[0].Mean != "0.000461250" || r.Envelope.Low != "0.000411250" || r.Envelope.High != "0.000511250" {
		t.Fatalf("unexpected report: %+v", r)
	}
	if r.Moves[0].InputFactor != "2.000000000" || r.Moves[0].Before == nil {
		t.Fatalf("move: %+v", r.Moves[0])
	}
}

func TestWithinProviderUsesOneAndNoExtrapolation(t *testing.T) {
	p, f, s := fixture()
	s.Moves[0].To = "c"
	c := calculator(t, p, f, s, WorkloadMap{})
	if err := c.Add(record()); err != nil {
		t.Fatal(err)
	}
	r := c.Result(Source{})
	if *r.Level1 != "0.000276750" || r.Envelope != nil || r.Moves[0].Gap != "no measured verbosity factor" || r.Moves[0].InputFactor != "1.000000000" {
		t.Fatalf("report: %+v", r)
	}
}

func TestWorkloadMappingAndScope(t *testing.T) {
	p, f, s := fixture()
	s.Moves[0].Scope = &Selector{"team", "private-team-sentinel"}
	f.Pairs[1].Output = "3"
	f.Pairs[1].High = "4"
	m := WorkloadMap{1, []Mapping{{Selector{"key", "private-key-sentinel"}, "extraction"}}}
	c := calculator(t, p, f, s, m)
	if err := c.Add(record()); err != nil {
		t.Fatal(err)
	}
	r := c.Result(Source{})
	if len(r.Level2) != 1 || r.Level2[0].Workload != "mapped" || r.Level2[0].Mean != "0.000561250" || r.Unmapped != 0 {
		t.Fatalf("report: %+v", r)
	}
	b, _ := json.Marshal(r)
	for _, sentinel := range []string{"private-key-sentinel", "private-team-sentinel"} {
		if strings.Contains(string(b), sentinel) {
			t.Fatal("identity leaked")
		}
	}
	m.Mappings = append(m.Mappings, Mapping{Selector{"team", "private-team-sentinel"}, "qa"})
	c = calculator(t, p, f, s, m)
	if c.Add(record()) == nil {
		t.Fatal("conflicting mappings accepted")
	}
}

func TestMissingAndInvalidSplitsStayUnknown(t *testing.T) {
	for _, kind := range []string{"input", "output", "cache-read", "cache-write", "invalid", "sum", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			p, f, s := fixture()
			c := calculator(t, p, f, s, WorkloadMap{})
			v := record()
			switch kind {
			case "input":
				v.Input = nil
			case "output":
				v.Output = nil
			case "cache-read":
				v.CacheRead = nil
			case "cache-write":
				v.CacheWrite = nil
			case "invalid":
				v.Issue = "invalid"
			case "sum":
				*v.CacheRead = 95
			case "unknown":
				v.Model = "private-unknown-model"
			}
			if err := c.Add(v); err != nil {
				t.Fatal(err)
			}
			r := c.Result(Source{})
			if r.Before != nil || r.Level1 != nil || r.Covered != 0 || len(r.Excluded) != 1 {
				t.Fatalf("unknown became cost: %+v", r)
			}
		})
	}
}

func TestMovesUseOriginalTrafficAndCheckOverlaps(t *testing.T) {
	p, f, s := fixture()
	s.Moves = append(s.Moves, Move{From: "b", To: "a", Fraction: "1"})
	f.InputPairs = append(f.InputPairs, InputFactor{"b", "a", "0.5", 39, "reverse"})
	c := calculator(t, p, f, s, WorkloadMap{})
	if err := c.Add(record()); err != nil {
		t.Fatal(err)
	}
	r := c.Result(Source{})
	if r.Moves[1].Covered != 0 || *r.Level1 != "0.000361250" {
		t.Fatal("moves cascaded")
	}
	s.Moves[1] = Move{From: "a", To: "c", Fraction: "0.6", Scope: &Selector{"key", "private-key-sentinel"}}
	c = calculator(t, p, f, s, WorkloadMap{})
	if c.Add(record()) == nil {
		t.Fatal("overlapping moves accepted")
	}
}

func TestUnknownModelsPricesAndUnmeasuredInputsRefused(t *testing.T) {
	p, f, s := fixture()
	for _, target := range []string{"unknown", ""} {
		s.Moves[0].To = target
		if _, err := New(p, f, s, WorkloadMap{}); err == nil {
			t.Fatal("unknown target accepted")
		}
	}
	s.Moves[0].To = "b"
	f.InputPairs = nil
	if _, err := New(p, f, s, WorkloadMap{}); err == nil {
		t.Fatal("unmeasured factor accepted")
	}
	p.Models[0].CacheWrite = ""
	if _, err := New(p, f, s, WorkloadMap{}); err == nil {
		t.Fatal("unknown cache price accepted")
	}
}

func TestConfigRejectsAmbiguousOrUnsafeInputs(t *testing.T) {
	for _, body := range []string{`{"version":1,"version":1}`, `{"version":1,"prompt":"secret-sentinel"}`, `{"version":1,"moves":"secret-sentinel"}`, `{} {}`} {
		path := filepath.Join(t.TempDir(), "scenario.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		var s Scenario
		err := ReadConfig(path, &s)
		if err == nil || strings.Contains(err.Error(), "secret-sentinel") {
			t.Fatalf("config: %v", err)
		}
	}
	p, _, _ := fixture()
	override := p
	override.Models = append([]Price(nil), p.Models...)
	override.Models[0].ID = "secret-sentinel"
	if _, err := OverridePrices(p, override); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("unsafe override")
	}
}

func TestInvalidNumericFactorsAndFractions(t *testing.T) {
	for _, v := range []string{"0", "-1", "1.01", "NaN", "1/2", ""} {
		p, f, s := fixture()
		s.Moves[0].Fraction = v
		if _, err := New(p, f, s, WorkloadMap{}); err == nil {
			t.Fatalf("fraction %q accepted", v)
		}
	}
	p, f, s := fixture()
	f.Pairs[0].Low = "3"
	if _, err := New(p, f, s, WorkloadMap{}); err == nil {
		t.Fatal("reversed mean interval accepted")
	}
}

func TestCostDoesNotMutateInputs(t *testing.T) {
	p, f, s := fixture()
	c := calculator(t, p, f, s, WorkloadMap{})
	v := record()
	if err := c.Add(v); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(c.Result(Source{}))
	b, _ := json.Marshal(c.Result(Source{}))
	if string(a) != string(b) || *v.Input != 100 {
		t.Fatal("calculation mutated inputs")
	}
}

func TestMissingScopeIdentityIsUnknownNotUnchanged(t *testing.T) {
	for _, kind := range []string{"key", "team"} {
		for _, empty := range []string{"", " \t "} {
			p, f, s := fixture()
			s.Moves[0].Scope = &Selector{kind, "private-key-sentinel"}
			c := calculator(t, p, f, s, WorkloadMap{})
			r := record()
			if kind == "key" {
				r.Key = empty
			} else {
				r.Team = empty
			}
			if err := c.Add(r); err != nil {
				t.Fatal(err)
			}
			got := c.Result(Source{})
			if got.Before != nil || got.Excluded["missing_scope_identity"] != 1 || got.Moves[0].Excluded != 1 {
				t.Fatalf("unknown scope: %+v", got)
			}
		}
	}
}

func TestOverridePreservesOmittedPriceTier(t *testing.T) {
	p, _, _ := fixture()
	p.Models[0].InputTierMax, p.Models[0].CacheWriteTTL = 100, "5m"
	override := p
	override.Models = append([]Price(nil), p.Models...)
	override.Models[0].InputTierMax, override.Models[0].CacheWriteTTL = 0, ""
	got, err := OverridePrices(p, override)
	if err != nil {
		t.Fatal(err)
	}
	if got.Models[0].InputTierMax != 100 || got.Models[0].CacheWriteTTL != "5m" || override.Models[0].InputTierMax != 0 {
		t.Fatal("override lost metadata or mutated input")
	}
	override.Models[0].InputTierMax = 200
	got, err = OverridePrices(p, override)
	if err != nil || got.Models[0].InputTierMax != 200 {
		t.Fatal("explicit tier replacement ignored")
	}
}

func TestUnknownScopeExcludesSelectedAndUnknownMovesTogether(t *testing.T) {
	p, f, s := fixture()
	s.Moves = append(s.Moves, Move{From: "a", To: "c", Fraction: "0.25", Scope: &Selector{"team", "selected-team"}})
	c := calculator(t, p, f, s, WorkloadMap{})
	r := record()
	r.Team = ""
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if got.Before != nil || got.Excluded["missing_scope_identity"] != 1 || got.Moves[0].Excluded != 1 || got.Moves[1].Excluded != 1 {
		t.Fatalf("partial eligibility: %+v", got)
	}
}
