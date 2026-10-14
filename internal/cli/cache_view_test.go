// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestCacheDropFixtureTextJSONAndPrivacy(t *testing.T) {
	for _, group := range []string{"key", "team", "session"} {
		for _, format := range []string{"text", "json"} {
			for _, hashes := range []bool{false, true} {
				args := []string{"investigate", "--litellm-spend", "../../examples/litellm-spend/cache-drop.csv", "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--group-by", group, "--format", format}
				if hashes {
					args = append(args, "--show-hashes")
				}
				var out, diagnostic bytes.Buffer
				if code := Run(args, &out, &diagnostic); code != 0 {
					t.Fatal(code, diagnostic.String())
				}
				if strings.Contains(out.String()+diagnostic.String(), "SENTINEL_") {
					t.Fatal("cache fixture leaked")
				}
				if format == "text" {
					lines := strings.Split(out.String(), "\n")
					if lines[1] != "But cache reuse fell: cache-read share 92% -> 61% (uncached input +620)." {
						t.Fatalf("cache change must be the second headline: %s", out.String())
					}
					if !strings.Contains(out.String(), cacheDiagnosticsNext) || strings.Contains(out.String(), "Excluded:") || strings.Contains(out.String(), "Uncached input tokens (read-covered requests)") || strings.Contains(out.String(), "unsupported rows: 0") || strings.Contains(out.String(), "outside periods: 0") {
						t.Fatal(out.String())
					}
					guide, err := os.ReadFile("../../docs/CACHE.md")
					if err != nil {
						t.Fatal(err)
					}
					if group == "key" && !hashes {
						excerpt := strings.SplitN(strings.SplitN(string(guide), "```text\n", 2)[1], "```", 2)[0]
						for _, line := range strings.Split(strings.TrimSuffix(excerpt, "\n"), "\n") {
							if !strings.Contains(out.String(), line+"\n") {
								t.Fatalf("guide excerpt differs: %q", line)
							}
						}
					}
					for _, want := range []string{"Cache-read share of recorded input: 92% -> 61%.", "Cache-write share of recorded input: 5% -> 7.5%.", "Uncached input +620: cache-read share fell from 92% to 61%.", "Whose uncached input grew?", group + "-1", "80 -> 700 uncached input tokens; change +620.", "No usage problems found."} {
						if !strings.Contains(out.String(), want) {
							t.Fatalf("missing %q: %s", want, out.String())
						}
					}
				} else if !strings.Contains(out.String(), `"weight_unit": "attributed-recorded-uncached-input-tokens"`) {
					t.Fatal(out.String())
				}
			}
		}
	}
}

func testCacheWindow(input, read uint64) compare.CacheWindow {
	zero, one, uncached := uint64(0), uint64(1), input-read
	w := compare.CacheWindow{Requests: 1, Read: compare.CacheDetail{CoverageKnown: true, Reported: 1, Covered: &one, Missing: &zero, Invalid: &zero}, Uncached: &uncached}
	w.Read.SetTotals(input, read)
	return w
}

func TestCacheHeadlineThresholds(t *testing.T) {
	for _, tt := range []struct {
		name string
		a, b compare.CacheWindow
		want string
	}{
		{"five points down", testCacheWindow(100, 92), testCacheWindow(100, 87), "But cache reuse fell: cache-read share 92% -> 87% (uncached input +5).\n"},
		{"five points up", testCacheWindow(100, 87), testCacheWindow(100, 92), "Cache reuse rose: cache-read share 87% -> 92% (uncached input -5).\n"},
		{"below five points", testCacheWindow(100, 92), testCacheWindow(100, 88), ""},
		{"material unchanged share", testCacheWindow(1000, 500), testCacheWindow(1200, 600), "Cache reuse held: cache-read share 50% -> 50% (uncached input +100).\n"},
		{"material decrease", testCacheWindow(1000, 500), testCacheWindow(800, 400), "Cache reuse held: cache-read share 50% -> 50% (uncached input -100).\n"},
		{"below absolute floor", testCacheWindow(100, 50), testCacheWindow(200, 100), ""},
		{"below relative floor", testCacheWindow(2000, 1000), testCacheWindow(2200, 1100), ""},
		{"from zero", testCacheWindow(10000, 10000), testCacheWindow(10000, 9900), "But cache reuse fell: cache-read share 100% -> 99% (uncached input +100).\n"},
		{"small from zero", testCacheWindow(10000, 10000), testCacheWindow(10000, 9999), ""},
		{"missing before", compare.CacheWindow{Requests: 1}, testCacheWindow(100, 50), ""},
		{"missing after", testCacheWindow(100, 50), compare.CacheWindow{Requests: 1}, ""},
		{"zero input", testCacheWindow(0, 0), testCacheWindow(100, 50), ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			renderCacheHeadline(&out, tt.a, tt.b)
			if out.String() != tt.want {
				t.Fatalf("got %q, want %q", out.String(), tt.want)
			}
		})
	}
}

func TestCachePartialCoverageAndExclusions(t *testing.T) {
	a, b := testCacheWindow(100, 92), testCacheWindow(100, 61)
	a.Requests, b.Requests = 2, 3
	missing := uint64(1)
	a.Read.Missing = &missing
	var out bytes.Buffer
	renderCacheHeadline(&out, a, b)
	if out.Len() != 0 {
		t.Fatal("partial population must not imply a whole-period cache change")
	}
	renderCachePair(&out, a, b, 100, 100)
	for _, want := range []string{"before: 1 of 2 requests report cache detail; after: 1 of 3", "Excluded: missing detail 1 -> 0 requests."} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	if strings.Contains(out.String(), "Excluded: invalid") || strings.Contains(out.String(), "Uncached input +") {
		t.Fatal(out.String())
	}
	// Unknown counts remain in JSON; text prints only known nonzero exclusions.
	out.Reset()
	renderCacheExclusions(&out, compare.CacheDetail{}, compare.CacheDetail{})
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
}

func TestIncompleteSpendKeepsCoverageFirst(t *testing.T) {
	r := spend.Report{Incomplete: true}
	r.Before.Cache, r.After.Cache = testCacheWindow(100, 92), testCacheWindow(100, 61)
	var out bytes.Buffer
	renderSpend(&out, r)
	if !strings.HasPrefix(out.String(), "Comparison incomplete:") || strings.Contains(out.String(), "But cache reuse fell:") || strings.Contains(out.String(), cacheDiagnosticsNext) {
		t.Fatal(out.String())
	}
}

func TestNoCacheDetailOneLine(t *testing.T) {
	var out, diagnostic bytes.Buffer
	code := Run([]string{"investigate", "--litellm-spend", "../../examples/litellm-spend/synthetic.csv", "--before-period", "2026-10-07", "--after-period", "2026-10-08"}, &out, &diagnostic)
	if code != 0 || strings.Count(out.String(), "Cache detail not recorded in this export.") != 1 || strings.Contains(out.String(), "Whose uncached input grew?") {
		t.Fatal(code, out.String(), diagnostic.String())
	}
}

func TestCacheInvalidAndUnknownCoverageRemainVisible(t *testing.T) {
	invalid, zero, raw := uint64(10), uint64(0), uint64(600)
	for _, detail := range []compare.CacheDetail{
		{CoverageKnown: true, Invalid: &invalid, Covered: &zero, Missing: &zero},
		{RecordedTokens: &raw, Issue: "unknown coverage"},
		{Issue: "invalid observations", IssueCounts: map[string]uint64{"token_observations.cache_read_input.invalid": 10}},
	} {
		var out bytes.Buffer
		w := compare.CacheWindow{Requests: 10, Read: detail}
		renderCachePair(&out, w, w, 1000, 1000)
		if !strings.Contains(out.String(), "Cache-read share of recorded input: unknown -> unknown.") || strings.Contains(out.String(), "Cache-read detail not recorded.") {
			t.Fatal(out.String())
		}
	}
}

func TestCacheZeroOriginAndImprovedShareWording(t *testing.T) {
	zero, one := uint64(0), uint64(1)
	a := compare.CacheWindow{Requests: 1, Read: compare.CacheDetail{CoverageKnown: true, Reported: 1, Covered: &one, Missing: &zero, Invalid: &zero, ZeroOriginUnknown: &one}, Uncached: &zero}
	a.Read.SetTotals(1000, 0)
	var out bytes.Buffer
	renderCachePair(&out, a, a, 1000, 1000)
	if !strings.Contains(out.String(), "Recorded zero detail, origin unknown: 1 -> 1 requests.") {
		t.Fatal(out.String())
	}
	b := a
	x, y := uint64(100), uint64(150)
	a.Uncached, b.Uncached = &x, &y
	a.Read.SetTotals(1000, 900)
	b.Read.SetTotals(3000, 2850)
	out.Reset()
	renderCachePair(&out, a, b, 1000, 3000)
	renderCacheRankings(&out, compare.CacheView{Before: a, After: b, Rankings: &compare.Concentration{}})
	if strings.Contains(out.String(), "share fell") || strings.Contains(out.String(), "Who lost cache reuse") || !strings.Contains(out.String(), "Whose uncached input grew?") {
		t.Fatal(out.String())
	}
}
