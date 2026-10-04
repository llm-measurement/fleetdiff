// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"math/big"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func cacheQuestion(r Report, before, after []summary.Envelope) Question {
	q := Question{ID: "cache", Question: "Did caching get worse?", Status: "cannot_determine"}
	if !r.Complete {
		q.Answer = "An expected producer or observation interval is incomplete. Supply complete intervals for every expected producer."
		return q
	}
	// Validate each original snapshot: aggregate ratios and coverage can hide
	// opposing producer errors, and missing detail is not an explicit zero.
	for _, window := range [][]summary.Envelope{before, after} {
		for _, doc := range window {
			if reason := cacheQuality(doc.Counters); reason != "" {
				q.Answer = reason
				return q
			}
		}
	}
	var c CacheChange
	for _, counter := range r.Counters {
		switch counter.Name {
		case "input_tokens":
			c.BeforeInputTokens, c.AfterInputTokens = counter.Before, counter.After
		case "cache_read_input_tokens":
			c.BeforeCacheReadInputTokens, c.AfterCacheReadInputTokens = counter.Before, counter.After
		}
	}
	if c.BeforeInputTokens == 0 || c.AfterInputTokens == 0 {
		q.Answer = "At least one window has zero recorded input tokens; cached-token share is undefined. Compare windows with positive input totals."
		return q
	}
	// Compare exact integer ratios before converting for display. A one-token
	// difference near MaxInt64 must not become an unchanged result.
	a := new(big.Rat).SetFrac64(int64(c.BeforeCacheReadInputTokens), int64(c.BeforeInputTokens))
	b := new(big.Rat).SetFrac64(int64(c.AfterCacheReadInputTokens), int64(c.AfterInputTokens))
	c.BeforeShare, _ = a.Float64()
	c.AfterShare, _ = b.Float64()
	delta := new(big.Rat).Sub(b, a)
	c.DeltaPercentagePoints, _ = delta.Mul(delta, big.NewRat(100, 1)).Float64()
	c.Direction = "unchanged"
	switch b.Cmp(a) {
	case -1:
		c.Direction = "decreased"
	case 1:
		c.Direction = "increased"
	}
	q.Status, q.Cache = "observed", &c
	q.Answer = "The recorded cached-token share " + c.Direction + ". This is cache-read input tokens / input tokens, not a request hit rate, cost, savings, or provider billing claim. Unknown input provenance still permits a recorded share. Missing output usage and tool failures do not block it when input and cache-read coverage are complete."
	return q
}

func cacheQuality(counters map[string]uint64) string {
	requests, hasRequests := counters["requests"]
	input, hasInput := counters["input_tokens"]
	cache, hasCache := counters["cache_read_input_tokens"]
	if !hasRequests || !hasInput || !hasCache {
		return "Required request, input-token, or cache-read-token counters are unavailable. Supply these counters; absence is not zero."
	}
	for _, field := range []string{"input", "cache_read_input"} {
		for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
			value, present := counters["token_observations."+field+"."+state]
			if !present {
				return "Input or cache-read observation quality counters are unavailable. Supply complete token_observations counters; older snapshots do not establish zero errors."
			}
			if state == "reported" {
				if value != requests {
					return "Input and cache-read fields must each be reported for every model attempt in each snapshot. Optional detail with no missing count can still be absent."
				}
			} else if value != 0 {
				return "Input or cache-read observations include missing, invalid, conflicting, or subset-violating values. Resolve accounting quality before comparing cached-token shares."
			}
		}
	}
	if cache > input || requests == 0 && input != 0 {
		return "Per-snapshot token totals disagree with input subsets or observed attempts. Resolve accounting quality before comparing cached-token shares."
	}
	return ""
}
