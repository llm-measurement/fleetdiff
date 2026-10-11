// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import "github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"

func summaryCacheView(r Report, before, after []summary.Envelope) *CacheView {
	v := &CacheView{GroupingGap: "These summaries retain aggregate cache totals, not cache totals by model or key/session. Use request-level records for those breakdowns."}
	for side, w := range []*CacheWindow{&v.Before, &v.After} {
		c := map[string]uint64{}
		for _, counter := range r.Counters {
			c[counter.Name] = counter.Before
			if side == 1 {
				c[counter.Name] = counter.After
			}
		}
		w.Requests = c["requests"]
		for _, f := range []struct {
			name   string
			detail *CacheDetail
		}{{"cache_read_input", &w.Read}, {"cache_write_input", &w.Write}} {
			d := f.detail
			d.Reported = c["token_observations."+f.name+".reported"]
			_, d.CoverageKnown = c["token_observations."+f.name+".reported"]
			d.IssueCounts = make(map[string]uint64)
			for _, name := range []string{"input", f.name} {
				for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
					key := "token_observations." + name + "." + state
					if count, present := c[key]; present {
						d.IssueCounts[key] = count
					}
				}
			}
			docs := before
			if side == 1 {
				docs = after
			}
			complete := r.Complete
			for _, doc := range docs {
				if !summaryCacheFieldComplete(doc.Counters, f.name) {
					complete = false
				}
			}
			input, hasInput := c["input_tokens"]
			tokens, hasTokens := c[f.name+"_tokens"]
			if hasTokens {
				d.RecordedTokens = &tokens
			}
			if complete && hasInput && hasTokens && tokens <= input {
				covered, missing, invalid := w.Requests, uint64(0), uint64(0)
				d.Covered, d.Missing, d.Invalid = &covered, &missing, &invalid
				d.SetTotals(input, tokens)
			} else {
				d.Issue = "Complete input and cache-detail observations are needed; summaries cannot recover the covered-row denominator."
			}
		}
		if w.Read.Input != nil {
			n := *w.Read.Input - *w.Read.Tokens
			w.Uncached = &n
		}
	}
	return v
}

func summaryCacheFieldComplete(c map[string]uint64, field string) bool {
	requests, ok := c["requests"]
	if !ok {
		return false
	}
	input, inputOK := c["input_tokens"]
	tokens, tokensOK := c[field+"_tokens"]
	if !inputOK || !tokensOK || tokens > input || requests == 0 && input != 0 {
		return false
	}
	for _, name := range []string{"input", field} {
		for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
			n, present := c["token_observations."+name+"."+state]
			if !present || (state == "reported" && n != requests) || (state != "reported" && n != 0) {
				return false
			}
		}
	}
	return true
}
