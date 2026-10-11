// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"
	"math/big"
	"slices"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func cacheCount(v *uint64) string {
	if v == nil {
		return "unknown"
	}
	return spendNumber(*v)
}

func cacheShare(v *float64) string {
	if v == nil {
		return "unknown"
	}
	return spendPercent(*v * 100)
}

func cacheReadChange(a, b compare.CacheWindow) *big.Rat {
	if !a.CompleteRead() || !b.CompleteRead() || a.Read.Input == nil || b.Read.Input == nil || a.Read.Tokens == nil || b.Read.Tokens == nil || *a.Read.Input == 0 || *b.Read.Input == 0 {
		return nil
	}
	share := func(d compare.CacheDetail) *big.Rat {
		return new(big.Rat).SetFrac(new(big.Int).SetUint64(*d.Tokens), new(big.Int).SetUint64(*d.Input))
	}
	return new(big.Rat).Sub(share(b.Read), share(a.Read))
}

func renderCacheHeadline(out io.Writer, a, b compare.CacheWindow) {
	change := cacheReadChange(a, b)
	if change == nil {
		return
	}
	delta, sign := *b.Uncached, "+"
	if delta >= *a.Uncached {
		delta -= *a.Uncached
	} else {
		delta, sign = *a.Uncached-delta, "-"
	}
	// A five-point share change or a 20% change of at least 100 tokens is material.
	minimumChange := *a.Uncached / 5
	if *a.Uncached%5 != 0 {
		minimumChange++
	}
	material := delta >= 100 && delta >= minimumChange
	if new(big.Rat).Abs(change).Cmp(big.NewRat(1, 20)) < 0 && !material {
		return
	}
	lead := "Cache reuse held"
	if change.Sign() < 0 {
		lead = "But cache reuse fell"
	} else if change.Sign() > 0 {
		lead = "Cache reuse rose"
	}
	if delta == 0 {
		sign = ""
	}
	fmt.Fprintf(out, "%s: cache-read share %s -> %s (uncached input %s%s).\n", lead, cacheShare(a.Read.Share), cacheShare(b.Read.Share), sign, spendNumber(delta))
}

func cacheCoverage(a, b compare.CacheWindow, ad, bd compare.CacheDetail) string {
	if ad.Covered != nil && bd.Covered != nil && *ad.Covered == a.Requests && *bd.Covered == b.Requests {
		return "all requests report cache detail"
	}
	return fmt.Sprintf("before: %s of %s requests report cache detail; after: %s of %s", cacheCount(ad.Covered), spendNumber(a.Requests), cacheCount(bd.Covered), spendNumber(b.Requests))
}

func renderCacheExclusions(out io.Writer, a, b compare.CacheDetail) {
	for _, f := range []struct {
		label string
		a, b  *uint64
	}{{"missing detail", a.Missing, b.Missing}, {"invalid detail or input", a.Invalid, b.Invalid}} {
		if (f.a != nil && *f.a != 0) || (f.b != nil && *f.b != 0) {
			fmt.Fprintf(out, "      Excluded: %s %s -> %s requests.\n", f.label, cacheCount(f.a), cacheCount(f.b))
		}
	}
}

const cacheDiagnosticsNext = "Next: Use your provider's cache diagnostics (Anthropic, OpenAI) to find why, and check that your gateway route preserves cache markers."

func renderCachePair(out io.Writer, a, b compare.CacheWindow, beforeTokens, afterTokens uint64) {
	for _, f := range []struct {
		label string
		a, b  compare.CacheDetail
	}{{"Cache-read", a.Read, b.Read}, {"Cache-write", a.Write, b.Write}} {
		if !f.a.HasDetail() && !f.b.HasDetail() {
			fmt.Fprintf(out, "    %s detail not recorded.\n", f.label)
			continue
		}
		fmt.Fprintf(out, "    %s share of recorded input: %s -> %s.\n", f.label, cacheShare(f.a.Share), cacheShare(f.b.Share))
		verb := "reads"
		if f.label == "Cache-write" {
			verb = "writes"
		}
		fmt.Fprintf(out, "      Cache %s: %s of %s input tokens -> %s of %s (%s).\n", verb, cacheCount(f.a.Tokens), cacheCount(f.a.Input), cacheCount(f.b.Tokens), cacheCount(f.b.Input), cacheCoverage(a, b, f.a, f.b))
		renderCacheExclusions(out, f.a, f.b)
		if f.a.Issue != "" || f.b.Issue != "" {
			fmt.Fprintln(out, "      Complete input and cache-detail observations are needed for this summary share.")
			for _, side := range []struct {
				name   string
				detail compare.CacheDetail
			}{{"Before", f.a}, {"After", f.b}} {
				var issues []string
				for name, count := range side.detail.IssueCounts {
					if count != 0 && !strings.HasSuffix(name, ".reported") {
						issues = append(issues, strings.TrimPrefix(name, "token_observations.")+": "+spendNumber(count))
					}
				}
				slices.Sort(issues)
				if len(issues) != 0 {
					fmt.Fprintf(out, "      %s quality evidence (categories may overlap): %s.\n", side.name, strings.Join(issues, "; "))
				}
			}
		}
		if (f.a.ZeroOriginUnknown != nil && *f.a.ZeroOriginUnknown != 0) || (f.b.ZeroOriginUnknown != nil && *f.b.ZeroOriginUnknown != 0) {
			fmt.Fprintf(out, "      Recorded zero detail, origin unknown: %s -> %s requests.\n", cacheCount(f.a.ZeroOriginUnknown), cacheCount(f.b.ZeroOriginUnknown))
		}
	}
	if a.CompleteRead() && b.CompleteRead() && *b.Uncached > *a.Uncached && *b.Read.Share < *a.Read.Share {
		growth := *b.Uncached - *a.Uncached
		if afterTokens <= beforeTokens || growth >= (afterTokens-beforeTokens)/2+(afterTokens-beforeTokens)%2 {
			fmt.Fprintf(out, "    Uncached input +%s: cache-read share fell from %s to %s.\n", spendNumber(growth), cacheShare(a.Read.Share), cacheShare(b.Read.Share))
		}
	}
}

func renderCacheRankings(out io.Writer, v compare.CacheView) {
	fmt.Fprintln(out, "\nWhose uncached input grew?")
	if v.Rankings == nil {
		fmt.Fprintln(out, "  Cache-read detail is needed in both periods to compare uncached growth.")
		return
	}
	fmt.Fprintln(out, "  Ranked by recorded uncached-input growth on read-covered requests; volume and workload mix can also change it.")
	for _, m := range v.Rankings.Movers {
		label := m.Item
		if m.Hash != "" {
			label += " (" + m.Hash + ")"
		}
		fmt.Fprintf(out, "  %s: %s -> %s uncached input tokens; change %s.\n", label, spendInterval(m.Before), spendInterval(m.After), signedInterval(m.Delta))
	}
	if len(v.Rankings.Movers) == 0 {
		fmt.Fprintln(out, "  No tracked candidates with possible uncached-input growth.")
	}
	fmt.Fprintf(out, "  Cache-read coverage: %s.\n", cacheCoverage(v.Before, v.After, v.Before.Read, v.After.Read))
	renderCacheExclusions(out, v.Before.Read, v.After.Read)
	if v.Before.MissingIdentity+v.After.MissingIdentity != 0 {
		fmt.Fprintf(out, "  Read-covered requests without group identity: %s -> %s.\n", spendNumber(v.Before.MissingIdentity), spendNumber(v.After.MissingIdentity))
	}
}
