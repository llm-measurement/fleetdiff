// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"math"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestInvestigationHeadlineShares(t *testing.T) {
	for _, tc := range []struct {
		name, measurement, status, flag, want string
		lower, upper                          float64
		candidates                            int
	}{
		{"exact tokens", "top_sessions", "observed", "runaway_candidate", "1 of 1 tracked sessions flagged for review: 80.00% of attributed tokens.", .8, .8, 1},
		{"bounded tokens", "top_sessions", "observed", "runaway_candidate", "1 of 1 tracked sessions flagged for review: [26.12%, 80.24%] of attributed tokens.", .261234, .802345, 1},
		{"tiny interval", "top_sessions", "observed", "runaway_candidate", "1 of 1 tracked sessions flagged for review: [80.00%, 80.01%] of attributed tokens.", .800001, .800002, 1},
		{"truncated", "top_sessions", "observed", "runaway_candidate", "1 of 1 shown sessions flagged for review: 80.00% of attributed tokens.", .8, .8, 4},
		{"attempts", "top_sessions_requests", "observed", "runaway_candidate", "1 of 1 tracked sessions flagged for review: 80.00% of attributed model attempts.", .8, .8, 1},
		{"no flags", "top_sessions", "observed", "", "Comparison ready. See observed coverage below.", .2, .8, 1},
		{"incomplete", "top_sessions", "limited", "", "Comparison ready. See observed coverage below.", .8, .8, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := compare.Investigation{
				Questions: []compare.Question{{ID: "sessions", Status: tc.status, Contributors: []compare.Contributor{{
					Measurement: tc.measurement, AfterShare: &compare.Share{Lower: tc.lower, Upper: tc.upper}, Flag: tc.flag,
				}}}},
				Evidence: compare.Report{Concentration: []compare.Concentration{{Name: tc.measurement, CandidateCount: tc.candidates}}},
			}
			if got := investigationHeadline(r); got != tc.want {
				t.Fatalf("got %q; want %q", got, tc.want)
			}
		})
	}
}

func TestInvestigationHeadlineKeepsMeasurementsSeparate(t *testing.T) {
	r := compare.Investigation{Questions: []compare.Question{{ID: "sessions", Status: "observed", Contributors: []compare.Contributor{
		{Measurement: "top_sessions_requests", AfterShare: &compare.Share{Lower: .9, Upper: .9}, Flag: "runaway_candidate"},
		{Measurement: "top_sessions", AfterShare: &compare.Share{Lower: .4, Upper: .5}, Flag: "runaway_candidate"},
		{Measurement: "top_sessions", AfterShare: &compare.Share{Lower: .3, Upper: .4}, Flag: "runaway_candidate"},
	}}}}
	want := "2 of 2 tracked sessions flagged for review; one flagged session: [40.00%, 50.00%] of attributed tokens."
	if got := investigationHeadline(r); got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

func TestInvestigationHeadlineVolume(t *testing.T) {
	for _, tc := range []struct {
		before, after uint64
		delta         string
	}{
		{200, 600, "+400"}, {600, 200, "-400"}, {200, 200, "+0"},
		{math.MaxUint64 - 1, math.MaxUint64, "+1"},
		{math.MaxUint64, math.MaxUint64 - 1, "-1"},
	} {
		r := compare.Investigation{Questions: []compare.Question{{ID: "volume", Volume: &compare.VolumeChange{BeforeTokens: tc.before, AfterTokens: tc.after}}}}
		if got := investigationHeadline(r); !strings.Contains(got, "("+tc.delta+")") {
			t.Fatal("incorrect integer delta", got)
		}
	}
}

func TestInvestigationHeadlinePreservesIntegerUncertainty(t *testing.T) {
	r := compare.Investigation{Questions: []compare.Question{{ID: "sessions", Contributors: []compare.Contributor{{
		Measurement: "top_sessions", Flag: "runaway_candidate",
		After:      &compare.Interval{Lower: math.MaxInt64 - 1, Upper: math.MaxInt64},
		AfterShare: &compare.Share{Lower: 1, Upper: 1},
	}}}}}
	if got := investigationHeadline(r); got != "1 of 1 tracked sessions flagged for review: [99.99%, 100.00%] of attributed tokens." {
		t.Fatal("rounded shares hid integer uncertainty", got)
	}
}

func TestCacheDoesNotChangeHeadlinePriority(t *testing.T) {
	for _, q := range []compare.Question{
		{ID: "volume", Status: "observed", Volume: &compare.VolumeChange{BeforeTokens: 100, AfterTokens: 200}},
		{ID: "volume", Status: "cannot_determine", Answer: "Output usage is missing."},
		{ID: "sessions", Status: "observed", Contributors: []compare.Contributor{{Measurement: "top_sessions", Flag: "runaway_candidate", AfterShare: &compare.Share{Lower: .8, Upper: .8}}}},
	} {
		r := compare.Investigation{Questions: []compare.Question{q}}
		want := investigationHeadline(r)
		r.Questions = append(r.Questions, compare.Question{ID: "cache", Status: "observed", Cache: &compare.CacheChange{BeforeShare: .8, AfterShare: .1, DeltaPercentagePoints: -70, Direction: "decreased"}})
		if got := investigationHeadline(r); got != want {
			t.Fatalf("cache changed headline from %q to %q", want, got)
		}
	}
}
