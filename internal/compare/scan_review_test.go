// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"slices"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestScanReviewTinyThresholds(t *testing.T) {
	for _, tc := range []struct {
		name, signal, direction string
		requests, input         uint64
		missing                 uint64
	}{
		{"flat", "", "", 100, 1000, 0},
		{"attempt up", "attempt_volume", "up", 101, 1010, 0},
		{"attempt down", "attempt_volume", "down", 99, 990, 0},
		{"intensity up", "tokens_per_attempt", "up", 100, 1001, 0},
		{"intensity down", "tokens_per_attempt", "down", 100, 999, 0},
		{"missing flat", "", "", 100, 1000, 25},
		{"missing up", "missing_usage_share", "up", 100, 1000, 26},
	} {
		t.Run(tc.name, func(t *testing.T) {
			docs := scanSeries(t, 25)
			if tc.missing > 0 {
				for i := range docs {
					docs[i].Counters["missing_token_usage"] = 25
				}
			}
			last := docs[len(docs)-1].Counters
			last["requests"], last["input_tokens"], last["missing_token_usage"] = tc.requests, tc.input, tc.missing
			o := scanOptions(25)
			o.Z, o.RelativeChange, o.CoverageChange, o.MinAttempts = 1e-20, 1e-20, 1e-20, 1
			r, err := Scan(docs, o)
			if err != nil || len(r.Windows) != 1 {
				t.Fatal("scan failed", err, r)
			}
			wantStatus := "evaluated"
			if tc.missing > 0 {
				wantStatus = "limited"
			}
			if r.Status != wantStatus {
				t.Fatal("unexpected coverage", r.Status)
			}
			findings := r.Windows[0].Findings
			if tc.signal == "" {
				if r.UnusualWindows != 0 || len(findings) != 0 {
					t.Fatal("unchanged observations became unusual", findings)
				}
			} else if r.UnusualWindows != 1 || len(findings) != 1 || findings[0].Signal != tc.signal || findings[0].Direction != tc.direction {
				t.Fatal("real change lost or unchanged scalar flagged", findings)
			}
			checked := 0
			for _, s := range r.Windows[0].Signals {
				if s.Status != "evaluated" || s.Measurement != "" {
					continue
				}
				if s.RequiredChange <= 0 {
					t.Fatal("positive configured change was lost", s)
				}
				if s.Median > 0 {
					// This is the floating-point plateau that previously yielded 0 >= 0.
					if s.Threshold != s.Median {
						t.Fatal("fixture no longer exercises rounded-away threshold", s)
					}
					checked++
				}
			}
			if checked != 2 {
				t.Fatal("did not exercise both positive-median scalars", checked)
			}
		})
	}
}

func TestScanReviewCachesParsedUpperBounds(t *testing.T) {
	for _, name := range []string{"empty", "exact", "uncertain", "merged producers"} {
		t.Run(name, func(t *testing.T) {
			docs := scanSeries(t, 1)
			s, err := frequentitems.New("micro", sketchhash.UserV1, sketchhash.HMACSHA25664)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "exact", "merged producers":
				if err := s.AddHash(1, 300); err != nil {
					t.Fatal(err)
				}
			case "uncertain":
				for key := uint64(1); key <= 3000; key++ {
					if err := s.AddHash(key, 1+int64(key%5)); err != nil {
						t.Fatal(err)
					}
				}
				if s.MaxError() == 0 {
					t.Fatal("fixture must have an untracked-key bound")
				}
			}
			data, err := s.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			docs[0].Sketches = map[string]summary.Payload{"top_users": {Kind: "frequent_items", Data: data}}
			expected := []string{"app"}
			if name == "merged producers" {
				other := docs[0]
				other.ProducerID = "other"
				otherSketch, err := frequentitems.New("micro", sketchhash.UserV1, sketchhash.HMACSHA25664)
				if err != nil {
					t.Fatal(err)
				}
				if err := otherSketch.AddHash(2, 500); err != nil {
					t.Fatal(err)
				}
				data, err := otherSketch.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				other.Sketches = map[string]summary.Payload{"top_users": {Kind: "frequent_items", Data: data}}
				docs = append(docs, other)
				expected = append(expected, "other")
			}
			frames, _, err := scanFrames(docs, expected)
			if err != nil || len(frames) != 1 {
				t.Fatal("cannot prepare frames", err)
			}
			merged := frames[0].sketches["top_users"]
			items, err := merged.FrequentItems(frequentitems.NoFalseNegatives)
			if err != nil {
				t.Fatal(err)
			}
			want := min(merged.TotalWeight(), merged.MaxError())
			for _, item := range items {
				want = max(want, min(merged.TotalWeight(), item.UpperBound))
			}
			got, present := frames[0].upperBounds["top_users"]
			if !present || got != want || len(frames[0].upperBounds) != 1 {
				t.Fatal("missing, incorrect, or invented cached bound", frames[0].upperBounds, want)
			}
			if name == "merged producers" && got != 500 {
				t.Fatal("cached producer bounds were summed instead of merged", got)
			}
		})
	}
}

func scanReviewDenseBaseline(t testing.TB) ([]scanFrame, scanFrame, ScanOptions) {
	t.Helper()
	docs := scanSeries(t, 2)
	for i := range docs {
		s, err := frequentitems.New("default", sketchhash.UserV1, sketchhash.HMACSHA25664)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			for key := uint64(1); key <= 1024; key++ {
				if err := s.AddHash(key, 1); err != nil {
					t.Fatal(err)
				}
			}
		} else if err := s.AddHash(9001, 1024); err != nil {
			t.Fatal(err)
		}
		data, err := s.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		docs[i].Sketches = map[string]summary.Payload{"top_users": {Kind: "frequent_items", Data: data}}
		docs[i].Counters["input_tokens"] = 1024
	}
	o := scanOptions(2)
	frames, _, err := scanFrames(docs, o.Expected)
	if err != nil || len(frames) != 2 {
		t.Fatal("cannot prepare dense baseline", err)
	}
	if frames[0].sketches["top_users"].Len() != 1024 || frames[0].upperBounds["top_users"] != 1 {
		t.Fatal("baseline must retain 1024 exact candidates")
	}
	return slices.Repeat(frames[:1], 384), frames[1], o
}

func TestScanReviewUsesCachedUpperBounds(t *testing.T) {
	base, current, o := scanReviewDenseBaseline(t)
	// A sentinel cache value distinguishes lookup from baseline re-enumeration.
	// Cache construction from real sketches is checked independently above.
	base[0].upperBounds["top_users"] = 512
	work := 0
	signals, _, err := scanSignals(base, current, o, &work)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(signals, func(s ScanSignal) bool { return s.Signal == "top_share" && s.Measurement == "top_users" })
	if index < 0 || signals[index].Median != .5 || signals[index].BaselineUpper != .5 {
		t.Fatal("scoring re-enumerated the baseline instead of using its cache", signals)
	}
}

func TestScanReviewDenseBaselineSparseCurrentBudget(t *testing.T) {
	base, current, o := scanReviewDenseBaseline(t)
	work := 0
	for range 128 {
		_, findings, err := scanSignals(base, current, o, &work)
		if err != nil || len(findings) != 1 || findings[0].Signal != "newly_prominent" || findings[0].key != 9001 {
			t.Fatal("dense baseline changed sparse-current result", findings, err)
		}
	}
	if work != 384*128 {
		t.Fatal("unexpected candidate comparison count", work)
	}
	work = maxScanComparisons - len(base)
	if _, _, err := scanSignals(base, current, o, &work); err != nil || work != maxScanComparisons {
		t.Fatal("exact budget boundary rejected", work, err)
	}
	if _, _, err := scanSignals(base, current, o, &work); err == nil {
		t.Fatal("candidate comparison budget was not enforced")
	}
}

func BenchmarkScanReviewDenseBaselineSparseCurrent(b *testing.B) {
	base, current, o := scanReviewDenseBaseline(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		work := 0
		if _, _, err := scanSignals(base, current, o, &work); err != nil {
			b.Fatal(err)
		}
	}
}
