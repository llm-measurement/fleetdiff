// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSummaryCacheWriteAndUncached(t *testing.T) {
	a, b, o := cacheWindows(t)
	for _, window := range [][]map[string]uint64{{a[0].Counters}, {b[0].Counters}} {
		c := window[0]
		c["cache_write_input_tokens"] = 100
		for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
			c["token_observations.cache_write_input."+state] = 0
		}
		c["token_observations.cache_write_input.reported"] = c["requests"]
	}
	q, _ := cacheAnswer(t, a, b, o)
	v := q.CacheView
	if v == nil || *v.Before.Write.Share != .1 || *v.Before.Uncached != 400 || *v.After.Uncached != 800 || v.Rankings != nil || v.GroupingGap == "" {
		t.Fatal(v)
	}
	b[0].Counters["token_observations.cache_write_input.reported"] = 9
	q, _ = cacheAnswer(t, a, b, o)
	if q.CacheView.After.Write.Share != nil || q.CacheView.After.Read.Share == nil {
		t.Fatal("write gap spoiled read or became zero")
	}
	b[0].Counters["cache_read_input_tokens"] = 1001
	q, _ = cacheAnswer(t, a, b, o)
	if q.CacheView.After.Read.Share != nil || q.CacheView.After.Uncached != nil || q.CacheView.Before.Read.Share == nil {
		t.Fatal("subset violation masked")
	}
}

func TestSummaryCacheQualityEvidenceAndUnknownCounts(t *testing.T) {
	for _, field := range []string{"cache_read_input", "cache_write_input"} {
		for _, problem := range []string{"malformed", "subset", "overlap", "partial", "missing quality"} {
			t.Run(field+"/"+problem, func(t *testing.T) {
				a, b, o := cacheWindows(t)
				for _, counters := range []map[string]uint64{a[0].Counters, b[0].Counters} {
					counters[field+"_tokens"] = 200
					for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
						counters["token_observations."+field+"."+state] = 0
					}
					counters["token_observations."+field+".reported"] = counters["requests"]
					counters["token_observations."+field+".PRIVATE_SENTINEL"] = 1
				}
				c := b[0].Counters
				switch problem {
				case "malformed":
					c[field+"_tokens"] = 0
					c["token_observations."+field+".reported"] = 0
					c["token_observations."+field+".invalid"] = 10
				case "subset":
					c[field+"_tokens"] = 1001
					c["token_observations."+field+".subset_violation"] = 1
				case "overlap":
					c["token_observations.input.invalid"] = 6
					c["token_observations."+field+".invalid"] = 6
					c["token_observations."+field+".conflict"] = 6
				case "partial":
					c["token_observations."+field+".reported"] = 9
				case "missing quality":
					for _, counters := range []map[string]uint64{a[0].Counters, b[0].Counters} {
						delete(counters, "token_observations."+field+".invalid")
					}
				}
				q, _ := cacheAnswer(t, a, b, o)
				d := q.CacheView.After.Read
				if field == "cache_write_input" {
					d = q.CacheView.After.Write
				}
				if d.Covered != nil || d.Missing != nil || d.Invalid != nil || d.ZeroOriginUnknown != nil {
					t.Fatalf("unknown request counts became measured counts: %+v", d)
				}
				if d.Input != nil || d.Tokens != nil || d.Share != nil || d.Issue == "" || !d.HasDetail() {
					t.Fatalf("invalid or incomplete detail became a valid measurement or disappeared: %+v", d)
				}
				if d.RecordedTokens == nil || *d.RecordedTokens != c[field+"_tokens"] {
					t.Fatal("raw summary totals were not preserved")
				}
				for _, name := range []string{"input", field} {
					for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
						key := "token_observations." + name + "." + state
						got, present := d.IssueCounts[key]
						want, expected := c[key]
						if present != expected || got != want {
							t.Fatalf("raw evidence %s: got %d/%t, want %d/%t", key, got, present, want, expected)
						}
					}
				}
				data, err := json.Marshal(d)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(data), "PRIVATE_SENTINEL") || len(d.IssueCounts) > 10 {
					t.Fatal("raw quality evidence escaped its bounded allowlist")
				}
				var wire map[string]json.RawMessage
				if err := json.Unmarshal(data, &wire); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"covered_requests", "missing_detail_requests", "invalid_detail_or_input_requests", "zero_origin_unknown_requests"} {
					if string(wire[key]) != "null" {
						t.Fatalf("%s must serialize as unknown, got %s", key, wire[key])
					}
				}
			})
		}
	}
}

func TestSummaryCacheLegacyTotalsRemainVisible(t *testing.T) {
	for _, tokens := range []uint64{0, 600} {
		a, b, o := cacheWindows(t)
		for _, counters := range []map[string]uint64{a[0].Counters, b[0].Counters} {
			counters["cache_read_input_tokens"] = tokens
			for key := range counters {
				if strings.HasPrefix(key, "token_observations.") {
					delete(counters, key)
				}
			}
		}
		q, _ := cacheAnswer(t, a, b, o)
		for _, w := range []CacheWindow{q.CacheView.Before, q.CacheView.After} {
			d := w.Read
			if !w.HasDetail() || !d.HasDetail() || d.RecordedTokens == nil || *d.RecordedTokens != tokens {
				t.Fatal("old summary totals were mistaken for absent detail")
			}
			if d.CoverageKnown || d.Covered != nil || d.Missing != nil || d.Invalid != nil || d.Share != nil || d.ZeroOriginUnknown != nil || len(d.IssueCounts) != 0 {
				t.Fatalf("old summary supplied false coverage or zero-origin evidence: %+v", d)
			}
		}
	}
}

func TestSummaryCacheCompleteCountsAndAbsentDetail(t *testing.T) {
	a, b, o := cacheWindows(t)
	for _, counters := range []map[string]uint64{a[0].Counters, b[0].Counters} {
		counters["cache_read_input_tokens"] = 0
	}
	q, _ := cacheAnswer(t, a, b, o)
	for _, w := range []CacheWindow{q.CacheView.Before, q.CacheView.After} {
		d := w.Read
		if !w.CompleteRead() || !d.HasDetail() || d.Covered == nil || *d.Covered != 10 || d.Missing == nil || *d.Missing != 0 || d.Invalid == nil || *d.Invalid != 0 {
			t.Fatalf("complete summary lost exact counts: %+v", d)
		}
		if d.Tokens == nil || *d.Tokens != 0 || d.ZeroOriginUnknown != nil {
			t.Fatal("recorded zero was lost or its origin invented")
		}
		if w.Write.HasDetail() {
			t.Fatal("absent write counters became recorded detail")
		}
	}
	for _, counters := range []map[string]uint64{a[0].Counters, b[0].Counters} {
		counters["token_observations.cache_read_input.reported"] = 0
	}
	q, _ = cacheAnswer(t, a, b, o)
	if q.CacheView.Before.HasDetail() || q.CacheView.After.HasDetail() {
		t.Fatal("initialized zero counter with known absent observations became recorded detail")
	}
}

func TestCacheDetailExactCountsAndPresence(t *testing.T) {
	covered, missing, invalid, zeroUnknown := uint64(0), uint64(0), uint64(2), uint64(0)
	d := CacheDetail{CoverageKnown: true, Covered: &covered, Missing: &missing, Invalid: &invalid, ZeroOriginUnknown: &zeroUnknown}
	if !d.HasDetail() || !(CacheWindow{Read: d}).HasDetail() {
		t.Fatal("invalid spend detail was mistaken for absent detail")
	}
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"covered_requests": "0", "missing_detail_requests": "0", "invalid_detail_or_input_requests": "2", "zero_origin_unknown_requests": "0"} {
		if string(wire[key]) != want {
			t.Fatalf("exact %s: got %s, want %s", key, wire[key], want)
		}
	}
	invalid, missing = 0, 2
	if d.HasDetail() {
		t.Fatal("missing-only spend detail became recorded detail")
	}
}
