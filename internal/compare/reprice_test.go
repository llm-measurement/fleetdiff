// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const repriceSummaryPeriod = "2020-01-02T00:00:00Z/2020-01-02T00:01:00Z"

func repriceSummary(t *testing.T) summary.Envelope {
	t.Helper()
	start := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC).UnixNano()
	doc := fixture(t, start, fixtureSource{Producer: "private-producer-sentinel", Users: []uint64{1}, Prompts: []uint64{2}, Weights: []int64{100}})
	doc.Counters["output_tokens"] = 20
	doc.Counters["cache_read_input_tokens"] = 30
	doc.Counters["cache_write_input_tokens"] = 10
	for _, field := range []string{"input", "output", "cache_read_input", "cache_write_input", "reasoning_output"} {
		for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
			doc.Counters["token_observations."+field+"."+state] = 0
		}
		doc.Counters["token_observations."+field+".reported"] = 1
	}
	return doc
}

func writeRepriceSummaries(t *testing.T, docs ...summary.Envelope) string {
	t.Helper()
	dir := t.TempDir()
	for i, doc := range docs {
		data, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("private-file-sentinel-%03d.json", i)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestReadRepriceSummaryAttributionGap(t *testing.T) {
	doc := repriceSummary(t)
	dir := writeRepriceSummaries(t, doc)
	path := filepath.Join(dir, "private-file-sentinel-000.json")
	before, _ := os.ReadFile(path)
	for _, input := range []string{dir, path} {
		visits := 0
		source, err := ReadReprice(input, repriceSummaryPeriod, []string{doc.ProducerID}, func(reprice.Record) error { visits++; return nil })
		if err != nil || visits != 0 || source.Rows != 1 || source.Outside != 0 || source.Kind != "summaries" {
			t.Fatalf("source %+v; visits %d: %v", source, visits, err)
		}
		gaps := strings.Join(source.Gaps, " ")
		if len(source.Gaps) != 1 || !strings.Contains(gaps, "no per-model component split") || !strings.Contains(gaps, "model-by-key") || !strings.Contains(gaps, "model-by-team") {
			t.Fatal("missing attribution limitations")
		}
		encoded, _ := json.Marshal(source)
		if strings.Contains(string(encoded), "sentinel") || strings.Contains(string(encoded), dir) {
			t.Fatal("private source metadata leaked")
		}
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("source was modified")
	}
}

func TestReadRepriceSummaryDedupAndSelection(t *testing.T) {
	doc := repriceSummary(t)
	old := repriceSummary(t)
	old.ObservedEnd -= minute / 2
	latest := repriceSummary(t)
	latest.Sequence = 2
	latest.Counters["requests"] = 3
	outside := repriceSummary(t)
	outside.WindowStart -= minute
	outside.ObservedStart -= minute
	outside.ObservedEnd -= minute
	outside.EmittedAt -= minute
	dir := writeRepriceSummaries(t, old, latest, latest, outside, outside)
	source, err := ReadReprice(dir, repriceSummaryPeriod, []string{doc.ProducerID}, func(reprice.Record) error {
		t.Fatal("summary fabricated pricing record")
		return nil
	})
	if err != nil || source.Rows != 3 || source.Outside != 1 || len(source.Gaps) != 1 {
		t.Fatalf("latest snapshot/dedup selection: %+v, %v", source, err)
	}
}

func TestReadRepriceSummaryCoverageGaps(t *testing.T) {
	for _, tc := range []string{"partial", "missing-producer", "missing-windows", "missing-counts"} {
		t.Run(tc, func(t *testing.T) {
			doc := repriceSummary(t)
			expected := []string{doc.ProducerID}
			period := repriceSummaryPeriod
			gap := "missing windows, missing producers, or partial observation intervals"
			switch tc {
			case "partial":
				doc.ObservedStart += minute / 2
			case "missing-producer":
				expected = append(expected, "private-missing-sentinel")
			case "missing-windows":
				period = "2020-01-02"
			case "missing-counts":
				delete(doc.Counters, "requests")
				gap = "lack request counts"
			}
			source, err := ReadReprice(writeRepriceSummaries(t, doc), period, expected, func(reprice.Record) error { t.Fatal("fabricated record"); return nil })
			if err != nil || !strings.Contains(strings.Join(source.Gaps, " "), gap) || strings.Contains(strings.Join(source.Gaps, " "), "sentinel") {
				t.Fatalf("coverage: %+v, %v", source, err)
			}
		})
	}
}

func TestReadRepriceSummaryTokenUsageCoverage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing *uint64
		gap     string
	}{
		{"complete", new(uint64(0)), ""},
		{"missing-usage", new(uint64(1)), "requests with missing input/output token usage"},
		{"unknown-coverage", nil, "token-usage completeness is unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := repriceSummary(t)
			if tc.missing == nil {
				delete(doc.Counters, "missing_token_usage")
			} else {
				doc.Counters["missing_token_usage"] = *tc.missing
			}
			source, err := ReadReprice(writeRepriceSummaries(t, doc), repriceSummaryPeriod, []string{doc.ProducerID}, func(reprice.Record) error {
				t.Fatal("summary fabricated a pricing record")
				return nil
			})
			if err != nil || source.Rows != 1 {
				t.Fatalf("recorded request count changed: %+v, %v", source, err)
			}
			if tc.gap == "" {
				if len(source.Gaps) != 1 {
					t.Fatalf("complete usage acquired a coverage gap: %+v", source)
				}
			} else if len(source.Gaps) != 2 || !strings.Contains(strings.Join(source.Gaps, " "), tc.gap) {
				t.Fatalf("token-usage coverage was lost: %+v", source)
			}
			encoded, err := json.Marshal(source)
			if err != nil || strings.Contains(string(encoded), "sentinel") {
				t.Fatal("coverage report leaked source metadata")
			}
		})
	}
}

func TestReadRepriceSummaryTokenUsageSelection(t *testing.T) {
	selected := repriceSummary(t)
	outside := repriceSummary(t)
	outside.WindowStart -= minute
	outside.ObservedStart -= minute
	outside.ObservedEnd -= minute
	outside.EmittedAt -= minute
	outside.Counters["missing_token_usage"] = 1
	source, err := ReadReprice(writeRepriceSummaries(t, selected, selected, outside), repriceSummaryPeriod, []string{selected.ProducerID}, func(reprice.Record) error { return nil })
	if err != nil || source.Rows != 1 || source.Outside != 1 || len(source.Gaps) != 1 {
		t.Fatalf("unselected usage affected selected coverage: %+v, %v", source, err)
	}
}

func TestReadRepriceSummaryValidation(t *testing.T) {
	for _, tc := range []string{"conflicting-sequence", "counter-regression", "observation-regression", "overlapping-epochs", "incompatible", "missing-expected", "duplicate-expected", "unexpected-producer", "partial-window", "outside-period", "future-emission", "missing-usage", "count-overflow"} {
		t.Run(tc, func(t *testing.T) {
			a, b := repriceSummary(t), repriceSummary(t)
			expected := []string{a.ProducerID}
			period := repriceSummaryPeriod
			switch tc {
			case "conflicting-sequence":
				b.Counters["requests"]++
			case "counter-regression":
				b.Sequence++
				b.Counters["requests"] = 0
			case "observation-regression":
				b.Sequence++
				b.ObservedEnd--
			case "overlapping-epochs":
				b.Epoch = "another-epoch"
			case "incompatible":
				b.ScopeID = "private-scope-sentinel"
			case "missing-expected":
				expected = nil
			case "duplicate-expected":
				expected = append(expected, a.ProducerID)
			case "unexpected-producer":
				expected = []string{"private-other-producer-sentinel"}
			case "partial-window":
				period = "2020-01-02T00:00:30Z/2020-01-02T00:01:00Z"
			case "outside-period":
				period = "2020-01-03"
			case "future-emission":
				a.EmittedAt = time.Date(2199, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
				b.EmittedAt = a.EmittedAt
			case "missing-usage":
				a.Counters["missing_token_usage"] = 2
			case "count-overflow":
				a.Counters["requests"] = math.MaxInt64
				b.WindowStart += minute
				b.ObservedStart += minute
				b.ObservedEnd += minute
				b.EmittedAt += minute
				period = "2020-01-02T00:00:00Z/2020-01-02T00:02:00Z"
			}
			source, err := ReadReprice(writeRepriceSummaries(t, a, b), period, expected, func(reprice.Record) error { t.Fatal("fabricated record"); return nil })
			if err == nil || strings.Contains(err.Error(), "sentinel") || !reflect.DeepEqual(source, reprice.Source{}) {
				t.Fatalf("wanted safe failure with empty source: %+v, %v", source, err)
			}
		})
	}
}

func TestReadRepriceSummaryErrorsDoNotLeak(t *testing.T) {
	doc := repriceSummary(t)
	dir := writeRepriceSummaries(t, doc)
	for _, period := range []string{"", "private-period-sentinel", "2199-01-01", "2020-01-02T00:01:00Z/2020-01-02T00:00:00Z"} {
		if _, err := ReadReprice(dir, period, []string{doc.ProducerID}, func(reprice.Record) error { return nil }); err == nil || strings.Contains(err.Error(), "sentinel") {
			t.Fatal("period accepted or leaked")
		}
	}
	if _, err := ReadReprice(dir, repriceSummaryPeriod, []string{doc.ProducerID}, nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
	path := filepath.Join(dir, "private-invalid-sentinel.json")
	if err := os.WriteFile(path, []byte(`{"private-body-sentinel":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{dir, path, filepath.Join(dir, "private-missing-sentinel.json")} {
		source, err := ReadReprice(input, repriceSummaryPeriod, []string{doc.ProducerID}, func(reprice.Record) error { t.Fatal("fabricated record"); return nil })
		if err == nil || strings.Contains(err.Error(), "sentinel") || !reflect.DeepEqual(source, reprice.Source{}) {
			t.Fatal("source reader accepted or leaked invalid input")
		}
	}
}
