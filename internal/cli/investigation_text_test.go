// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestInvestigationTextRoundsContributionsOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		sign float64
		want string
	}{
		{"increase", 1, "+1592 tokens from attempt count; +1308 tokens from tokens per attempt."},
		{"decrease", -1, "-1592 tokens from attempt count; -1308 tokens from tokens per attempt."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := compare.Investigation{Version: 1, Questions: []compare.Question{
				{ID: "volume", Status: "observed", Volume: &compare.VolumeChange{
					RequestContribution: tc.sign * 20700 / 13, TokensPerRequestContribution: tc.sign * 17000 / 13,
					BeforeAverage: 100, AfterAverage: 3300.0 / 13,
				}},
			}}
			before, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			renderInvestigation(&out, r, .25)
			if !strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "Tokens per attempt: 100.00 -> 253.85.") {
				t.Fatal(out.String())
			}
			after, err := json.Marshal(r)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("display rounding changed JSON values")
			}
		})
	}
}

func TestInvestigationTextPreservesJSONAndUncertainty(t *testing.T) {
	r := compare.Investigation{Version: 1, Questions: []compare.Question{
		{ID: "sessions", Question: "Which sessions need investigation?", Status: "limited", Contributors: []compare.Contributor{
			{Item: "item-1", Measurement: "top_sessions", Before: &compare.Interval{Lower: 1, Upper: 1}, After: &compare.Interval{Lower: 2, Upper: 4}, AfterShare: &compare.Share{Lower: .2, Upper: .4}},
			{Item: "item-1", Measurement: "top_sessions_requests", Before: &compare.Interval{Lower: 0, Upper: 0}, After: &compare.Interval{Lower: 3, Upper: 3}},
		}},
		{ID: "users", Status: "cannot_determine", Answer: "Add user-weight sketches."},
		{ID: "usage_source", Status: "cannot_determine", Answer: "Provider origin is unknown."},
	}}
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	renderInvestigation(&out, r, .4)
	after, err := json.Marshal(r)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("text rendering changed the JSON contract")
	}
	for _, want := range []string{"(partial coverage)", "1 -> [2, 4]", "[20.00%, 40.00%]", "0 -> 3", "unknown", "top_sessions_requests", "top_sessions", "Turn on user rankings (topk_keys) for more answers."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "flagged for review") || strings.Contains(out.String(), "cannot_determine") || strings.Contains(out.String(), "More answers with more data:") || strings.Count(out.String(), "\nNotes: ") != 1 {
		t.Fatal("unexpected flag or repeated unknown answers", out.String())
	}
}

func TestCacheInvestigationTextAndJSON(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var out, diagnostic bytes.Buffer
		args := []string{"investigate", "--before", "../../examples/cache/data/before",
			"--after", "../../examples/cache/data/after", "--expected", "app", "--format", format}
		if code := Run(args, &out, &diagnostic); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		if format == "text" {
			if !strings.HasPrefix(out.String(), "Reported tokens: 1200 -> 1200 (+0); model attempts: 10 -> 10.\n") {
				t.Fatal("cache replaced the existing headline", out.String())
			}
			for _, want := range []string{
				"Cached-token share: 60.00% -> 20.00% (-40.00 percentage points; decreased).",
				"Recorded cache-read/input tokens: 600/1000 -> 200/1000.",
				"Before: 2026-10-04T00:00:00Z for 1m0s.", "After: 2026-10-04T00:01:00Z for 1m0s.",
				"Share of recorded input tokens served from cache.",
				"Turn on user and session rankings (topk_keys) for more answers.",
			} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in %s", want, out.String())
				}
			}
			continue
		}
		var r compare.Investigation
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if len(r.Questions) != 7 || r.Questions[6].ID != "cache" || r.Questions[6].Cache == nil ||
			r.Questions[6].Cache.DeltaPercentagePoints != -40 || r.Questions[5].Status != "cannot_determine" {
			t.Fatal("missing cache result or invented provider origin", r.Questions)
		}
		if !strings.Contains(r.Questions[6].Answer, "Missing output usage and tool failures do not block it") || !strings.Contains(r.Questions[6].Answer, "not a request hit rate, cost, savings, or provider billing claim") {
			t.Fatal("detailed cache contract missing from JSON")
		}
	}
}

func TestInvestigationRankingHintTargetsMissingConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, userStatus, sessionStatus, want string
		configured, dropped                   []string
	}{
		{"both missing", "cannot_determine", "cannot_determine", "Turn on user and session rankings (topk_keys) for more answers.", nil, nil},
		{"users available", "observed", "cannot_determine", "Turn on session rankings (topk_keys) for more answers.", nil, nil},
		{"sessions limited", "cannot_determine", "limited", "Turn on user rankings (topk_keys) for more answers.", nil, nil},
		{"configured but empty", "cannot_determine", "cannot_determine", "", []string{"top_users_requests", "top_sessions"}, nil},
		{"partly supplied", "cannot_determine", "cannot_determine", "", nil, []string{"top_users", "top_sessions_requests"}},
		{"all available", "observed", "observed", "", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := compare.Investigation{Questions: []compare.Question{
				{ID: "users", Status: tc.userStatus}, {ID: "sessions", Status: tc.sessionStatus},
				{ID: "usage_source", Status: "cannot_determine", Answer: "Provider origin is unknown."},
			}}
			for _, name := range tc.configured {
				r.Evidence.Concentration = append(r.Evidence.Concentration, compare.Concentration{Name: name})
			}
			r.Evidence.DroppedMeasurements = tc.dropped
			before, _ := json.Marshal(r)
			var out bytes.Buffer
			renderInvestigation(&out, r, .25)
			after, _ := json.Marshal(r)
			if !bytes.Equal(before, after) {
				t.Fatal("targeted hint changed JSON details")
			}
			if tc.want == "" && strings.Contains(out.String(), "Turn on ") || tc.want != "" && !strings.Contains(out.String(), tc.want) {
				t.Fatal("hint recommends configured data or misses absent rankings", out.String())
			}
			if strings.Contains(out.String(), "More answers with more data") || strings.Count(out.String(), "\nNotes: ") != 1 {
				t.Fatal("verbose or repeated closing notes", out.String())
			}
		})
	}
}

func TestCacheTextPreservesPrecisionAndUnknown(t *testing.T) {
	for _, tc := range []struct {
		cache *compare.CacheChange
		want  string
	}{
		{&compare.CacheChange{BeforeShare: 1.0 / 3, AfterShare: 2.0 / 3, DeltaPercentagePoints: 100.0 / 3, Direction: "increased"}, "Cached-token share: 33.33% -> 66.67% (+33.33 percentage points; increased)."},
		{nil, "Cached-token share unavailable: Supply complete quality counters."},
	} {
		q := compare.Question{ID: "cache", Status: "observed", Cache: tc.cache}
		if tc.cache == nil {
			q.Status, q.Answer = "cannot_determine", "Supply complete quality counters."
		}
		r := compare.Investigation{Questions: []compare.Question{q}}
		before, _ := json.Marshal(r)
		var out bytes.Buffer
		renderInvestigation(&out, r, .25)
		after, _ := json.Marshal(r)
		if !bytes.Equal(before, after) || !strings.Contains(out.String(), tc.want) {
			t.Fatal("cache rendering changed values or lost its explanation", out.String())
		}
		if tc.cache == nil && strings.Contains(out.String(), "0.00%") {
			t.Fatal("unknown became zero", out.String())
		}
	}
}
