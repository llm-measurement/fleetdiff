// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"bytes"
	"encoding/json"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/change"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func tokenCUSUMOptions() TokenCUSUMOptions { return TokenCUSUMOptions{ScaleFloor: 1, MinShift: 2} }

func tokenCUSUMSeries(t testing.TB, count int) []summary.Envelope {
	docs := scanSeries(t, count)
	for i := range docs {
		setTokenCUSUMQuality(docs[i].Counters, docs[i].Counters["requests"])
	}
	return docs
}

func setTokenCUSUMQuality(counters map[string]uint64, requests uint64) {
	for _, field := range []string{"input", "output"} {
		for _, state := range []string{"reported", "missing", "invalid", "conflict"} {
			counters["token_observations."+field+"."+state] = 0
		}
		counters["token_observations."+field+".reported"] = requests
	}
}

func TestTokenCUSUMTotalsAndCausality(t *testing.T) {
	for _, test := range []struct {
		name          string
		input, output uint64
		requests      uint64
		wantStatistic float64
	}{
		{"flat", 1000, 0, 100, 0},
		{"spike", 4000, 0, 100, 2999},
		{"output spike", 1000, 3000, 100, 2999},
		{"broad growth", 4000, 0, 400, 2999},
		{"zero", 0, 0, 0, 0},
		{"low traffic", 1000, 0, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			docs := tokenCUSUMSeries(t, 25)
			docs[24].Counters["input_tokens"] = test.input
			docs[24].Counters["output_tokens"] = test.output
			docs[24].Counters["requests"] = test.requests
			setTokenCUSUMQuality(docs[24].Counters, test.requests)
			before, err := json.Marshal(docs)
			if err != nil {
				t.Fatal(err)
			}
			report, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions())
			if err != nil {
				t.Fatal(err)
			}
			window := report.TokenCUSUM.Windows[0]
			if report.TokenCUSUM.Status != change.CUSUMUncalibrated || window.Status != change.CUSUMUncalibrated ||
				window.Total == nil || *window.Total != test.input+test.output || window.Samples != 24 ||
				window.Diagnostic == nil || window.Diagnostic.Median != 1000 || window.Diagnostic.MAD != 0 ||
				window.Diagnostic.Statistic != test.wantStatistic {
				t.Fatalf("unexpected diagnostic: %+v, %+v", window, window.Diagnostic)
			}
			legacy, err := Scan(docs, scanOptions(25))
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(legacy)
			got, _ := json.Marshal(report.ScanReport)
			after, _ := json.Marshal(docs)
			if !bytes.Equal(want, got) || !bytes.Equal(before, after) {
				t.Fatal("diagnostic changed legacy results or mutated its input")
			}
		})
	}
}

func TestTokenCUSUMDoesNotDoubleCountSubsetsOrUseAttemptGuard(t *testing.T) {
	docs := tokenCUSUMSeries(t, 25)
	for i := range docs {
		docs[i].Counters["requests"] = 1
		setTokenCUSUMQuality(docs[i].Counters, 1)
		docs[i].Counters["output_tokens"] = 200
		docs[i].Counters["cache_read_input_tokens"] = 100
		docs[i].Counters["cache_write_input_tokens"] = 100
		docs[i].Counters["reasoning_output_tokens"] = 100
	}
	o := scanOptions(25)
	o.MinAttempts = 1000
	report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
	if err != nil {
		t.Fatal(err)
	}
	window := report.TokenCUSUM.Windows[0]
	if window.Total == nil || *window.Total != 1200 || window.Diagnostic == nil || window.Diagnostic.Median != 1200 {
		t.Fatalf("subsets or minimum attempts changed token totals: %+v", window)
	}
}

func TestTokenCUSUMIncompleteWindowsResetHistory(t *testing.T) {
	for _, name := range []string{"missing usage", "partial", "missing producer", "too large", "no attempts"} {
		t.Run(name, func(t *testing.T) {
			docs := tokenCUSUMSeries(t, 15)
			o := scanOptions(15)
			o.Baseline, o.Recent = 6, 9
			docs[6].Counters["input_tokens"] = 4000
			switch name {
			case "missing usage":
				docs[7].Counters["missing_token_usage"] = 1
			case "partial":
				docs[7].ObservedEnd--
			case "missing producer":
				o.Expected = []string{"app", "other"}
				for i := 0; i < 15; i++ {
					if i == 7 {
						continue
					}
					other := docs[i]
					other.ProducerID = "other"
					docs = append(docs, other)
				}
			case "too large":
				docs[7].Counters["input_tokens"] = maxExactCUSUMTokens + 1
			case "no attempts":
				docs[7].Counters["requests"] = 0
			}
			report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
			if err != nil {
				t.Fatal(err)
			}
			windows := report.TokenCUSUM.Windows
			if report.TokenCUSUM.Status != change.CUSUMInsufficient || len(windows) != 9 ||
				windows[0].Status != change.CUSUMUncalibrated || windows[0].Diagnostic.Statistic <= 0 ||
				windows[1].Reason == "" || windows[1].Samples != 0 {
				t.Fatalf("incomplete position not represented: %+v", report.TokenCUSUM)
			}
			for _, window := range windows[1:8] {
				if window.Status != change.CUSUMInsufficient || window.Diagnostic != nil {
					t.Fatalf("state bridged ineligible bucket: %+v", window)
				}
			}
			if windows[8].Status != change.CUSUMUncalibrated || windows[8].Diagnostic.Statistic != 0 {
				t.Fatal("did not rewarm with clean state", windows[8])
			}
		})
	}
}

func TestTokenCUSUMMissingIsNotZeroAndPrecisionIsExplicit(t *testing.T) {
	for _, name := range []string{"input_tokens", "output_tokens", "missing_token_usage", "requests", "precision", "max sum"} {
		t.Run(name, func(t *testing.T) {
			docs := tokenCUSUMSeries(t, 25)
			for i := range docs {
				if name == "precision" {
					docs[i].Counters["input_tokens"] = maxExactCUSUMTokens + 1
				} else if name == "max sum" {
					docs[i].Counters["input_tokens"], docs[i].Counters["output_tokens"] = math.MaxInt64, math.MaxInt64
				} else {
					delete(docs[i].Counters, name)
				}
			}
			report, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions())
			if err != nil {
				t.Fatal(err)
			}
			window := report.TokenCUSUM.Windows[0]
			if window.Status != change.CUSUMInsufficient || window.Diagnostic != nil || window.Reason == "" {
				t.Fatalf("unavailable became scored: %+v", window)
			}
			if name == "max sum" {
				if window.Total == nil || *window.Total != math.MaxUint64-1 {
					t.Fatal("sum overflowed signed range", window.Total)
				}
			} else if name == "precision" {
				if window.Total == nil || *window.Total != maxExactCUSUMTokens+1 {
					t.Fatal("integer total rounded", window.Total)
				}
			} else if window.Total != nil {
				t.Fatal("missing tokens became zero", window.Total)
			}
		})
	}
	frame := scanFrame{complete: true, counters: map[string]uint64{
		"requests": 1, "missing_token_usage": 0, "input_tokens": math.MaxUint64, "output_tokens": 1,
	}}
	if total, reason := tokenCUSUMTotal(frame); total != nil || reason == "" {
		t.Fatal("unsigned sum overflow accepted")
	}
}

func TestTokenCUSUMReusesReadinessAndCompatibility(t *testing.T) {
	for _, name := range []string{"gap", "stale", "future snapshot", "short", "open", "key", "scope", "accounting", "width"} {
		t.Run(name, func(t *testing.T) {
			docs := tokenCUSUMSeries(t, 25)
			o := scanOptions(25)
			incompatible := false
			switch name {
			case "gap":
				docs = slices.Delete(docs, 20, 21)
			case "stale":
				o.Now += 10 * minute
			case "future snapshot":
				docs[24].EmittedAt = o.Now + minute
			case "short":
				docs = docs[20:]
			case "open":
				o.Now--
			case "key":
				docs[12].KeyID, incompatible = "PRIVATE_SENTINEL", true
			case "scope":
				docs[12].ScopeID, incompatible = "PRIVATE_SENTINEL", true
			case "accounting":
				docs[12].AccountingID, incompatible = "PRIVATE_SENTINEL", true
			case "width":
				docs[12].WindowDuration, incompatible = 2*minute, true
			}
			report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
			if incompatible {
				if err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
					t.Fatal("incompatible series accepted or leaked", err)
				}
				return
			}
			if err != nil || report.TokenCUSUM.Status != change.CUSUMInsufficient {
				t.Fatalf("unsupported series scored: %+v, %v", report.TokenCUSUM, err)
			}
			for _, window := range report.TokenCUSUM.Windows {
				if window.Diagnostic != nil || name == "open" && window.Start == docs[24].WindowStart {
					t.Fatal("readiness or closed-window gate bypassed", window)
				}
			}
		})
	}
}

func TestTokenCUSUMSnapshotReplacementDedupAndOrder(t *testing.T) {
	docs := tokenCUSUMSeries(t, 25)
	latest := docs[24]
	latest.Sequence++
	latest.Counters = maps.Clone(latest.Counters)
	latest.Counters["input_tokens"] = 4000
	docs = append(docs, latest, latest)
	first, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions())
	if err != nil || *first.TokenCUSUM.Windows[0].Total != 4000 {
		t.Fatal("snapshots were added instead of replaced/deduplicated", err)
	}
	slices.Reverse(docs)
	second, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions())
	want, _ := json.Marshal(first)
	got, _ := json.Marshal(second)
	if err != nil || !bytes.Equal(want, got) {
		t.Fatal("arrival order changed report", err)
	}
	docs[0].Counters = maps.Clone(docs[0].Counters)
	docs[0].Counters["input_tokens"]++
	if _, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions()); err == nil {
		t.Fatal("conflicting duplicate accepted")
	}
}

func TestTokenCUSUMQualityUsesLatestCumulativeSnapshot(t *testing.T) {
	for _, name := range []string{"repaired latest", "bad latest"} {
		for _, reversed := range []bool{false, true} {
			order := "older first"
			if reversed {
				order = "latest first"
			}
			t.Run(name+"/"+order, func(t *testing.T) {
				docs := tokenCUSUMSeries(t, 25)
				older := docs[24]
				latest := older
				latest.Sequence++
				latest.Counters = maps.Clone(older.Counters)
				want := change.CUSUMUncalibrated
				if name == "repaired latest" {
					older.Counters["token_observations.input.reported"] = 99
				} else {
					latest.Counters["requests"] = 101
					latest.Counters["token_observations.output.reported"] = 101
					want = change.CUSUMInsufficient
				}
				docs[24] = older
				docs = append(docs, latest)
				if reversed {
					slices.Reverse(docs)
				}
				combined, err := summary.Combine(docsForTokenCUSUMWindow(docs, older.WindowStart), []string{"app"})
				if err != nil || len(combined.Sources) != 1 || combined.Sources[0].Sequence != latest.Sequence {
					t.Fatalf("fixture must pass the existing combiner and select the latest snapshot: %v", err)
				}
				report, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions())
				if err != nil {
					t.Fatal(err)
				}
				window := report.TokenCUSUM.Windows[0]
				if window.Status != want || report.TokenCUSUM.Status != want {
					t.Fatalf("quality did not follow latest selection: %+v, want %s", window, want)
				}
				if want == change.CUSUMUncalibrated {
					if window.Total == nil || *window.Total != 1000 || window.Diagnostic == nil || window.Samples != 24 {
						t.Fatalf("repaired window not scored from latest counters: %+v", window)
					}
				} else if window.Total != nil || window.Diagnostic != nil || window.Reason == "" {
					t.Fatalf("bad latest snapshot became usable: %+v", window)
				}
			})
		}
	}
}

func docsForTokenCUSUMWindow(input []summary.Envelope, start int64) []summary.Envelope {
	var selected []summary.Envelope
	for _, doc := range input {
		if doc.WindowStart == start {
			selected = append(selected, doc)
		}
	}
	return selected
}

func TestTokenCUSUMReplacementPreservesCumulativeQualityRegressions(t *testing.T) {
	for _, counter := range []string{"token_observations.input.invalid", "token_observations.output.conflict"} {
		for _, reversed := range []bool{false, true} {
			docs := tokenCUSUMSeries(t, 25)
			latest := docs[24]
			latest.Sequence++
			latest.Counters = maps.Clone(latest.Counters)
			docs[24].Counters[counter] = 1
			docs = append(docs, latest)
			if reversed {
				slices.Reverse(docs)
			}
			_, err := summary.Combine(docsForTokenCUSUMWindow(docs, latest.WindowStart), []string{"app"})
			if err == nil || err.Error() != "summary counter regressed" {
				t.Fatalf("combiner did not reject cumulative quality regression: %v", err)
			}
			if _, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions()); err == nil {
				t.Fatal("quality selection bypassed cumulative regression validation")
			}
		}
	}
}

func TestTokenCUSUMQualitySelectionKeepsEveryProducerAndEpoch(t *testing.T) {
	for _, name := range []string{"producer", "epoch"} {
		t.Run(name, func(t *testing.T) {
			docs := tokenCUSUMSeries(t, 25)
			o := scanOptions(25)
			bad := docs[24]
			bad.Counters = maps.Clone(bad.Counters)
			bad.Counters["token_observations.input.reported"] = 99
			docs[24].Sequence++
			if name == "producer" {
				o.Expected = []string{"app", "other"}
				for i := 0; i < 24; i++ {
					other := docs[i]
					other.ProducerID = "other"
					docs = append(docs, other)
				}
				bad.ProducerID = "other"
			} else {
				bad.ObservedEnd -= minute / 2
				docs[24].ObservedStart = bad.ObservedEnd
				docs[24].Epoch = "restart"
			}
			docs = append(docs, bad)
			for range 2 {
				report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
				if err != nil {
					t.Fatal(err)
				}
				window := report.TokenCUSUM.Windows[0]
				if window.Status != change.CUSUMInsufficient || window.Total != nil || window.Diagnostic != nil {
					t.Fatalf("higher sequence from another source hid bad quality: %+v", window)
				}
				slices.Reverse(docs)
			}
		})
	}
}

func TestTokenCUSUMRestartCoverageAndProducerOverflow(t *testing.T) {
	docs := tokenCUSUMSeries(t, 25)
	first := docs[24]
	first.ObservedEnd -= minute / 2
	first.Counters = maps.Clone(first.Counters)
	first.Counters["input_tokens"] = 400
	second := first
	second.Epoch = "restart"
	second.ObservedStart = first.ObservedEnd
	second.ObservedEnd = docs[24].ObservedEnd
	second.Counters = maps.Clone(first.Counters)
	second.Counters["input_tokens"] = 600
	docs[24] = first
	docs = append(docs, second)
	report, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions())
	if err != nil || *report.TokenCUSUM.Windows[0].Total != 1000 {
		t.Fatal("disjoint restart intervals not combined", err)
	}
	docs[25].ObservedStart--
	if _, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions()); err == nil {
		t.Fatal("overlapping restart intervals accepted")
	}
	docs = tokenCUSUMSeries(t, 25)
	other := docs[24]
	other.ProducerID = "other"
	other.Counters = maps.Clone(other.Counters)
	other.Counters["input_tokens"] = math.MaxInt64
	docs = append(docs, other)
	o := scanOptions(25)
	o.Expected = []string{"app", "other"}
	if _, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions()); err == nil {
		t.Fatal("combined counter overflow accepted")
	}
}

func TestTokenCUSUMOptionsAndDurationReference(t *testing.T) {
	docs := tokenCUSUMSeries(t, 25)
	o := scanOptions(25)
	o.BaselineDuration = 6 * minute
	report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
	if err != nil || report.TokenCUSUM.Reference != 6 || report.TokenCUSUM.Windows[0].Samples != 6 {
		t.Fatal("duration baseline not resolved", err, report.TokenCUSUM)
	}
	for _, options := range []TokenCUSUMOptions{{}, {ScaleFloor: math.NaN(), MinShift: 2}, {ScaleFloor: 1, MinShift: math.Inf(1)}} {
		if _, err := ScanTokenCUSUM(docs, o, options); err == nil {
			t.Fatal("invalid diagnostic config accepted")
		}
	}
}

func TestTokenCUSUMHonorsExportedObservationQuality(t *testing.T) {
	for _, issue := range []string{"reported", "missing", "invalid", "conflict", "subset_violation", "partial schema", "absent quality", "dedup bypass"} {
		t.Run(issue, func(t *testing.T) {
			docs := tokenCUSUMSeries(t, 25)
			for i := range docs {
				for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
					docs[i].Counters["token_observations.input."+state] = 0
				}
				docs[i].Counters["token_observations.input.reported"] = 100
			}
			if issue == "absent quality" {
				for i := range docs {
					for name := range docs[i].Counters {
						if strings.HasPrefix(name, "token_observations.") {
							delete(docs[i].Counters, name)
						}
					}
				}
			} else if issue == "dedup bypass" {
				for i := range docs {
					docs[i].Counters["dedup_key_missing"] = 0
				}
				docs[24].Counters["dedup_key_missing"] = 1
			} else if issue == "partial schema" {
				for i := range docs {
					delete(docs[i].Counters, "token_observations.input.conflict")
				}
			} else if issue == "reported" {
				docs[24].Counters["token_observations.input.reported"] = 99
			} else {
				docs[24].Counters["token_observations.input."+issue] = 1
			}
			report, err := ScanTokenCUSUM(docs, scanOptions(25), tokenCUSUMOptions())
			if err != nil {
				t.Fatal(err)
			}
			window := report.TokenCUSUM.Windows[0]
			if window.Status != change.CUSUMInsufficient || window.Total != nil || window.Diagnostic != nil {
				t.Fatalf("exported quality issue ignored: %+v", window)
			}
		})
	}
}

func TestTokenCUSUMQualityCannotCancelAcrossProducers(t *testing.T) {
	docs := tokenCUSUMSeries(t, 25)
	for i := 0; i < 25; i++ {
		for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
			docs[i].Counters["token_observations.input."+state] = 0
		}
		docs[i].Counters["token_observations.input.reported"] = 100
		other := docs[i]
		other.ProducerID = "other"
		other.Counters = maps.Clone(other.Counters)
		if i == 24 {
			docs[i].Counters["token_observations.input.reported"] = 99
			other.Counters["token_observations.input.reported"] = 101
		}
		docs = append(docs, other)
	}
	o := scanOptions(25)
	o.Expected = []string{"app", "other"}
	report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
	if err != nil || report.TokenCUSUM.Windows[0].Status != change.CUSUMInsufficient {
		t.Fatal("opposing producer quality errors disappeared in combined counters", err)
	}
}

func TestTokenCUSUMNoAttemptTokensCannotHideAcrossSources(t *testing.T) {
	for _, name := range []string{"producers", "restart epochs"} {
		t.Run(name, func(t *testing.T) {
			docs := tokenCUSUMSeries(t, 25)
			o := scanOptions(25)
			if name == "producers" {
				o.Expected = []string{"app", "other"}
				for i := 0; i < 25; i++ {
					other := docs[i]
					other.ProducerID = "other"
					other.Counters = maps.Clone(other.Counters)
					docs = append(docs, other)
				}
			} else {
				other := docs[24]
				other.Epoch = "restart"
				other.ObservedStart += minute / 2
				other.Counters = maps.Clone(other.Counters)
				docs[24].ObservedEnd = other.ObservedStart
				docs = append(docs, other)
			}
			docs[24].Counters["requests"] = 0
			docs[24].Counters["input_tokens"] = 10
			setTokenCUSUMQuality(docs[24].Counters, 0)
			report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
			if err != nil {
				t.Fatal(err)
			}
			window := report.TokenCUSUM.Windows[0]
			if window.Status != change.CUSUMInsufficient || window.Total != nil || window.Diagnostic != nil {
				t.Fatalf("source attempts masked malformed source tokens: %+v", window)
			}
		})
	}
}
