// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func assertFailureContribution(t *testing.T, got *PricedFailures, want Report) {
	t.Helper()
	if got == nil || got.Records != want.Covered || !reflect.DeepEqual(got.Before, want.Before) ||
		!reflect.DeepEqual(got.Level1, want.Level1) || !reflect.DeepEqual(got.Level2, want.Level2) ||
		!reflect.DeepEqual(got.Envelope, want.Envelope) {
		t.Fatalf("failure contribution differs from calculator composition: %+v, want %+v", got, want)
	}
	if got.UsageBasis != "gateway-recorded" || got.Finality != "unknown" {
		t.Fatalf("missing recorded-usage provenance: %+v", got)
	}
}

func assertOriginalReport(t *testing.T, got, want Report) {
	t.Helper()
	got.PricedFailures = nil
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("failure reporting changed existing JSON values:\n%s\nwant:\n%s", a, b)
	}
}

func TestPricedFailuresAfterExclusions(t *testing.T) {
	for _, tc := range []struct {
		name, exclusion string
		change          func(*Prices, *Scenario, *Record)
	}{
		{"unknown-model", "unknown_model_price", func(_ *Prices, _ *Scenario, r *Record) { r.Model = "private-unknown-sentinel" }},
		{"missing-cache-read", "missing_split", func(_ *Prices, _ *Scenario, r *Record) { r.CacheRead = nil }},
		{"missing-cache-write", "missing_split", func(_ *Prices, _ *Scenario, r *Record) { r.CacheWrite = nil }},
		{"missing-scope", "missing_scope_identity", func(_ *Prices, s *Scenario, r *Record) {
			s.Moves[0].Scope = &Selector{"team", "selected-team"}
			r.Team = ""
		}},
		{"source-price-tier", "outside_price_tier", func(p *Prices, _ *Scenario, _ *Record) { p.Models[0].InputTierMax = 99 }},
		{"invalid-cache", "invalid_cache_partition", func(_ *Prices, _ *Scenario, r *Record) { *r.CacheRead = 100 }},
		{"unsupported", "unsupported_call_type", func(_ *Prices, _ *Scenario, r *Record) { r.Issue = "unsupported_call_type" }},
		{"zero-origin", "zero_origin_unknown", func(_ *Prices, _ *Scenario, r *Record) { r.Issue = "zero_origin_unknown" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, f, s := fixture()
			r := record()
			r.Failed = true
			tc.change(&p, &s, &r)
			c := calculator(t, p, f, s, WorkloadMap{})
			control := calculator(t, p, f, s, WorkloadMap{})
			if err := c.Add(r); err != nil {
				t.Fatal(err)
			}
			r.Failed = false
			if err := control.Add(r); err != nil {
				t.Fatal(err)
			}
			got := c.Result(Source{})
			if got.Covered != 0 || got.Excluded[tc.exclusion] != 1 || got.PricedFailures != nil || c.failures != nil {
				t.Fatalf("excluded failure entered the priced subset: %+v", got)
			}
			assertOriginalReport(t, got, control.Result(Source{}))
		})
	}
}

func TestPricedFailuresTargetTierRejection(t *testing.T) {
	p, f, s := fixture()
	p.Models[1].InputTierMax = 199
	s.Moves[0].Fraction = "0.01"
	c := calculator(t, p, f, s, WorkloadMap{})
	control := calculator(t, p, f, s, WorkloadMap{})
	r := record()
	r.Failed = true
	err := c.Add(r)
	r.Failed = false
	want := control.Add(r)
	if err == nil || want == nil || err.Error() != want.Error() || !strings.Contains(err.Error(), "target's priced input tier") || c.failures != nil {
		t.Fatalf("target tier rejection changed: %v, want %v", err, want)
	}
	assertOriginalReport(t, c.Result(Source{}), control.Result(Source{}))
}

func TestPricedFailuresComposeMultipleFractionMoves(t *testing.T) {
	p, f, s := fixture()
	s.Moves[0].Fraction = "0.25"
	s.Moves[0].Scope = &Selector{"team", "private-team-sentinel"}
	s.Moves = append(s.Moves, Move{From: "a", To: "c", Fraction: "0.5", Scope: &Selector{"key", "private-key-sentinel"}})
	for _, w := range Workloads {
		f.Pairs = append(f.Pairs, Factor{"a", "c", w, "3", "2", "4", 39, "record-a-c"})
	}
	for _, mapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmapped", true: "mapped"}[mapped], func(t *testing.T) {
			m := WorkloadMap{}
			if mapped {
				m = WorkloadMap{1, []Mapping{{Selector{"key", "private-key-sentinel"}, "extraction"}}}
			}
			c := calculator(t, p, f, s, m)
			control := calculator(t, p, f, s, m)
			onlyFailures := calculator(t, p, f, s, m)
			r := record()
			r.Failed = true
			if err := c.Add(r); err != nil {
				t.Fatal(err)
			}
			if !r.Failed || c.failures.failures != nil || c.failures.moves[0].move.Scope != nil || c.failures.moves[0].scope != c.moves[0].scope {
				t.Fatal("child retained raw scopes, recursed, or mutated the record")
			}
			r.Failed = false
			if err := onlyFailures.Add(r); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := control.Add(r); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Add(r); err != nil {
				t.Fatal(err)
			}
			got := c.Result(Source{})
			assertOriginalReport(t, got, control.Result(Source{}))
			assertFailureContribution(t, got.PricedFailures, onlyFailures.Result(Source{}))
			v := got.PricedFailures
			if v.Input.String() != "100" || v.Output.String() != "50" || v.CacheRead.String() != "20" || v.CacheWrite.String() != "10" || v.Total.String() != "150" || v.Records != 1 {
				t.Fatalf("tokens counted per move rather than per record: %+v", v)
			}
			if *v.Before != "0.000184500" || *v.Level1 != "0.000365125" || v.Level2[0].Mean != "0.000615125" {
				t.Fatalf("original-volume fractional math changed: %+v", v)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "sentinel") || !reflect.DeepEqual(got, c.Result(Source{})) {
				t.Fatal("identity leaked or result mutated calculator state")
			}
			c.Close()
			for _, b := range c.failures.secret {
				if b != 0 {
					t.Fatal("child secret survived parent close")
				}
			}
		})
	}
}

func TestPricedFailuresUseParentWorkloadVariants(t *testing.T) {
	p, f, s := fixture()
	f.Pairs[1].Output, f.Pairs[1].High = "3", "4"
	m := WorkloadMap{1, []Mapping{{Selector{"key", "private-key-sentinel"}, "extraction"}}}
	c := calculator(t, p, f, s, m)
	r := record()
	r.Failed = true
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	r.Failed, r.Key = false, "unmapped-success"
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if len(got.Level2) != 3 || len(got.PricedFailures.Level2) != 3 {
		t.Fatal("failure-only workload mapping changed the scenario variants")
	}
	for i, w := range got.PricedFailures.Level2 {
		if w.Workload != got.Level2[i].Workload || w.Mean != "0.000561250" || w.Low != "0.000411250" || w.High != "0.000661250" {
			t.Fatalf("mapped failure changed with the success-only workload assumption: %+v", w)
		}
	}
}

func TestPricedFailuresUnselectedBaselineAndEmptyMoves(t *testing.T) {
	for _, unmatched := range []string{"scope", "model", "second-move"} {
		t.Run(unmatched, func(t *testing.T) {
			p, f, s := fixture()
			r := record()
			r.Failed = true
			switch unmatched {
			case "scope":
				s.Moves[0].Scope = &Selector{"key", "private-key-sentinel"}
				r.Key = "unselected-failure"
			case "model":
				r.Model = "c"
			case "second-move":
				s.Moves = append(s.Moves, Move{From: "a", To: "c", Fraction: "0.25", Scope: &Selector{"key", "success-key"}})
				for _, w := range Workloads {
					f.Pairs = append(f.Pairs, Factor{"a", "c", w, "2", "1.5", "2.5", 39, "record-a-c"})
				}
			}
			c := calculator(t, p, f, s, WorkloadMap{})
			if err := c.Add(r); err != nil {
				t.Fatal(err)
			}
			success := record()
			if unmatched == "second-move" {
				success.Key = "success-key"
			}
			if err := c.Add(success); err != nil {
				t.Fatal(err)
			}
			got := c.Result(Source{})
			v := got.PricedFailures
			if v == nil || v.Records != 1 || len(v.Level2) != 3 || v.Envelope == nil {
				t.Fatalf("success-only move suppressed failure contribution: %+v", v)
			}
			want := "0.000184500"
			if unmatched == "model" {
				want = "0.000369000"
			}
			if *v.Before != want {
				t.Fatalf("source contribution: %s", *v.Before)
			}
			if unmatched == "second-move" {
				if *v.Level1 != "0.000361250" || v.Level2[0].Mean != "0.000461250" {
					t.Fatalf("success-only move changed failure costs: %+v", v)
				}
			} else if *v.Level1 != want || v.Level2[0].Mean != want || v.Level2[0].Low != want || v.Level2[0].High != want {
				t.Fatalf("unselected but priced failure lost its baseline: %+v", v)
			}
		})
	}
}

func TestPricedFailuresMissingParentLevel2(t *testing.T) {
	p, f, s := fixture()
	s.Moves = append(s.Moves, Move{From: "a", To: "c", Fraction: "0.25", Scope: &Selector{"key", "success-key"}})
	c := calculator(t, p, f, s, WorkloadMap{})
	r := record()
	r.Failed = true
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	r.Key, r.Failed = "success-key", false
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if len(got.Level2) != 0 || len(got.PricedFailures.Level2) != 0 || got.PricedFailures.Envelope != nil || got.PricedFailures.Level1 == nil {
		t.Fatal("subset invented an unsupported full-scenario level 2")
	}
}

func TestPricedFailuresZeroAndNonfailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		p, f, s := fixture()
		c := calculator(t, p, f, s, WorkloadMap{})
		r := record()
		r.Failed = failed
		*r.Input, *r.Output, *r.CacheRead, *r.CacheWrite = 0, 0, 0, 0
		if err := c.Add(r); err != nil {
			t.Fatal(err)
		}
		got := c.Result(Source{})
		if !failed {
			if got.PricedFailures != nil {
				t.Fatal("nonfailure entered subset")
			}
		} else if got.PricedFailures.Records != 1 || got.PricedFailures.Total.Sign() != 0 || *got.PricedFailures.Level1 != "0.000000000" {
			t.Fatal("explicit zero priced contribution changed")
		}
	}
}

func TestPricedFailuresNoNewAggregateTokenLimit(t *testing.T) {
	p, f, s := fixture()
	c := calculator(t, p, f, s, WorkloadMap{})
	control := calculator(t, p, f, s, WorkloadMap{})
	r := record()
	r.Model = "c"
	*r.Input, *r.Output, *r.CacheRead, *r.CacheWrite = math.MaxInt64, 0, 0, 0
	for range 3 {
		r.Failed = true
		if err := c.Add(r); err != nil {
			t.Fatal(err)
		}
		r.Failed = false
		if err := control.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	got := c.Result(Source{})
	assertOriginalReport(t, got, control.Result(Source{}))
	if got.PricedFailures.Total.String() != "27670116110564327421" {
		t.Fatal("aggregate tokens wrapped or saturated")
	}
	encoded, err := json.Marshal(got)
	if err != nil || !strings.Contains(string(encoded), `"recorded_tokens":27670116110564327421`) {
		t.Fatal("large token aggregate not encoded as an exact JSON integer")
	}
	snapshot := got.PricedFailures.Input.String()
	r.Failed = true
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	if got.PricedFailures.Input.String() != snapshot {
		t.Fatal("subsequent records mutated an existing report")
	}
}
