// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"
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
		fmt.Fprintf(out, "      Covered input / %s tokens: %s / %s -> %s / %s; requests %s/%s -> %s/%s.\n", f.label, cacheCount(f.a.Input), cacheCount(f.a.Tokens), cacheCount(f.b.Input), cacheCount(f.b.Tokens), cacheCount(f.a.Covered), spendNumber(a.Requests), cacheCount(f.b.Covered), spendNumber(b.Requests))
		if slices.ContainsFunc([]*uint64{f.a.Missing, f.b.Missing, f.a.Invalid, f.b.Invalid}, func(n *uint64) bool { return n == nil || *n != 0 }) {
			fmt.Fprintf(out, "      Excluded: missing detail %s -> %s; invalid detail or input %s -> %s.\n", cacheCount(f.a.Missing), cacheCount(f.b.Missing), cacheCount(f.a.Invalid), cacheCount(f.b.Invalid))
		}
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
	fmt.Fprintf(out, "    Uncached input tokens (read-covered requests): %s -> %s.\n", cacheCount(a.Uncached), cacheCount(b.Uncached))
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
	fmt.Fprintf(out, "  Read-covered requests: %s/%s -> %s/%s; missing detail excluded: %s -> %s; invalid detail or input excluded: %s -> %s.\n", cacheCount(v.Before.Read.Covered), spendNumber(v.Before.Requests), cacheCount(v.After.Read.Covered), spendNumber(v.After.Requests), cacheCount(v.Before.Read.Missing), cacheCount(v.After.Read.Missing), cacheCount(v.Before.Read.Invalid), cacheCount(v.After.Read.Invalid))
	if v.Before.MissingIdentity+v.After.MissingIdentity != 0 {
		fmt.Fprintf(out, "  Read-covered requests without group identity: %s -> %s.\n", spendNumber(v.Before.MissingIdentity), spendNumber(v.After.MissingIdentity))
	}
}
