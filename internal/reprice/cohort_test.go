// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import "testing"

func TestSummaryCohortUsesRequestExtremaNotTokenSum(t *testing.T) {
	p, f, s := fixture()
	p.Models[0].InputTierMax = 100
	p.Models[1].InputTierMax = 200
	c := calculator(t, p, f, s, WorkloadMap{})
	r := record()
	r.Count = 20
	*r.Input = 2000
	lo, hi := uint64(100), uint64(100)
	r.InputMin, r.InputMax = &lo, &hi
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	if got.Covered != 20 || len(got.Excluded) != 0 {
		t.Fatalf("aggregate mistaken for request size: %+v", got)
	}
}

func TestSummaryCohortCannotAverageAcrossPriceTier(t *testing.T) {
	for _, target := range []bool{false, true} {
		p, f, s := fixture()
		if target {
			p.Models[1].InputTierMax = 200
		} else {
			p.Models[0].InputTierMax = 100
		}
		// Fractional selection does not shrink an individual request's tier.
		s.Moves[0].Fraction = "0.01"
		c := calculator(t, p, f, s, WorkloadMap{})
		r := record()
		r.Count = 2
		*r.Input = 200
		lo, hi := uint64(50), uint64(150)
		r.InputMin, r.InputMax = &lo, &hi
		err := c.Add(r)
		if target {
			if err == nil {
				t.Fatal("target cohort crossed a pricing tier")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			got := c.Result(Source{})
			if got.Covered != 0 || got.Excluded["outside_price_tier"] != 2 {
				t.Fatalf("source cohort averaged across a tier: %+v", got)
			}
		}
	}
}

func TestSummaryCohortExclusionsAndMalformedExtrema(t *testing.T) {
	for _, reason := range []string{"invalid_usage", "missing_split", "zero_origin_unknown", "unpriced_reserved_bucket"} {
		p, f, s := fixture()
		c := calculator(t, p, f, s, WorkloadMap{})
		r := Record{Model: "a", Count: 3, Issue: reason}
		if reason == "unpriced_reserved_bucket" {
			r.Model = "__overflow__"
		}
		if err := c.Add(r); err != nil {
			t.Fatal(err)
		}
		got := c.Result(Source{})
		if got.Before != nil || got.Covered != 0 || got.Excluded[reason] != 3 {
			t.Fatalf("exclusion lost: %+v", got)
		}
	}
	for _, kind := range []string{"one-sided", "reversed", "under", "over", "missing"} {
		p, f, s := fixture()
		c := calculator(t, p, f, s, WorkloadMap{})
		r := record()
		lo, hi := uint64(50), uint64(150)
		r.InputMin, r.InputMax = &lo, &hi
		switch kind {
		case "one-sided":
			r.InputMax = nil
		case "reversed":
			lo, hi = 150, 50
		case "under":
			*r.Input = 49
		case "over":
			*r.Input = 151
		case "missing":
			r.Input = nil
		}
		if c.Add(r) == nil {
			t.Fatalf("accepted %s extrema", kind)
		}
	}
}
