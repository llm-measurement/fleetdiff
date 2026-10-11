// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func validCacheField(obs accounting.Observation, field string) bool {
	return obs.Quality[field+"/reported"] == 1 && obs.Quality[field+"/invalid"] == 0 &&
		obs.Quality[field+"/conflict"] == 0 && obs.Quality[field+"/subset_violation"] == 0
}

func addCache(w *compare.CacheWindow, row Row, obs accounting.Observation, q Quality) error {
	w.Requests++
	for _, f := range []struct {
		name, column, metric string
		detail               *compare.CacheDetail
	}{
		{"cache_read_input", "cache_read_input_tokens", "cache_read_input_tokens_total", &w.Read},
		{"cache_write_input", "cache_write_input_tokens", "cache_write_input_tokens_total", &w.Write},
	} {
		d := f.detail
		d.CoverageKnown = true
		if d.ZeroOriginUnknown == nil {
			d.ZeroOriginUnknown = new(uint64)
			d.Covered, d.Missing, d.Invalid = new(uint64), new(uint64), new(uint64)
		}
		d.Reported += obs.Quality[f.name+"/reported"]
		if row.Values[f.column] == "" {
			*d.Missing++
			continue
		}
		if !validCacheField(obs, "input") || !validCacheField(obs, f.name) {
			*d.Invalid++
			continue
		}
		input, tokens := obs.Metrics[accounting.Prefix+"input_tokens_total"], obs.Metrics[accounting.Prefix+f.metric]
		if tokens == 0 {
			*d.ZeroOriginUnknown++
		}
		if d.Input != nil {
			var err error
			input, err = accounting.AddBounded(*d.Input, input)
			if err != nil {
				return err
			}
			tokens, err = accounting.AddBounded(*d.Tokens, tokens)
			if err != nil {
				return err
			}
		}
		*d.Covered++
		d.SetTotals(input, tokens)
	}
	if w.Read.Input != nil {
		uncached := *w.Read.Input - *w.Read.Tokens
		w.Uncached = &uncached
	}
	if validCacheField(obs, "input") && validCacheField(obs, "cache_read_input") {
		w.MissingIdentity += q.MissingIdentity
	}
	return nil
}
