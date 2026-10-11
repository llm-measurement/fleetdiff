// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
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
