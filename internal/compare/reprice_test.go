// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
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
		source, err := ReadReprice(input, repriceSummaryPeriod, []string{doc.ProducerID}, "", func(reprice.Record) error { visits++; return nil })
		if err != nil || visits != 0 || source.Rows != 1 || source.Outside != 0 || source.Kind != "summaries" {
			t.Fatalf("source %+v; visits %d: %v", source, visits, err)
		}
		gaps := strings.Join(source.Gaps, " ")
		if len(source.Gaps) != 1 || !strings.Contains(gaps, "no per-model component split") || !strings.Contains(gaps, "model-by-key") || !strings.Contains(gaps, "model-by-team") {
			t.Fatal("missing attribution limitations")
		}
		encoded, _ := json.Marshal(source)
		if string(encoded) != `{"kind":"summaries","start":"2020-01-02T00:00:00Z","end_exclusive":"2020-01-02T00:01:00Z","observations":1,"outside_period":0,"gaps":["Current summaries have no per-model component split; top-k cannot recover model-by-key or model-by-team intersections."]}` {
			t.Fatal("legacy source bytes changed")
		}
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
	source, err := ReadReprice(dir, repriceSummaryPeriod, []string{doc.ProducerID}, "", func(reprice.Record) error {
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
			source, err := ReadReprice(writeRepriceSummaries(t, doc), period, expected, "", func(reprice.Record) error { t.Fatal("fabricated record"); return nil })
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
			source, err := ReadReprice(writeRepriceSummaries(t, doc), repriceSummaryPeriod, []string{doc.ProducerID}, "", func(reprice.Record) error {
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
	source, err := ReadReprice(writeRepriceSummaries(t, selected, selected, outside), repriceSummaryPeriod, []string{selected.ProducerID}, "", func(reprice.Record) error { return nil })
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
			source, err := ReadReprice(writeRepriceSummaries(t, a, b), period, expected, "", func(reprice.Record) error { t.Fatal("fabricated record"); return nil })
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
		if _, err := ReadReprice(dir, period, []string{doc.ProducerID}, "", func(reprice.Record) error { return nil }); err == nil || strings.Contains(err.Error(), "sentinel") {
			t.Fatal("period accepted or leaked")
		}
	}
	if _, err := ReadReprice(dir, repriceSummaryPeriod, []string{doc.ProducerID}, "", nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
	path := filepath.Join(dir, "private-invalid-sentinel.json")
	if err := os.WriteFile(path, []byte(`{"private-body-sentinel":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{dir, path, filepath.Join(dir, "private-missing-sentinel.json")} {
		source, err := ReadReprice(input, repriceSummaryPeriod, []string{doc.ProducerID}, "", func(reprice.Record) error { t.Fatal("fabricated record"); return nil })
		if err == nil || strings.Contains(err.Error(), "sentinel") || !reflect.DeepEqual(source, reprice.Source{}) {
			t.Fatal("source reader accepted or leaked invalid input")
		}
	}
}

const repriceModelContract = "model-usage-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func repriceAttempt(input uint64) modelusage.Attempt {
	return modelusage.Attempt{
		Input: modelusage.Observation{Value: new(input)}, Output: modelusage.Observation{Value: new(uint64(20))},
		CacheRead: modelusage.Observation{Value: new(uint64(30))}, CacheWrite: modelusage.Observation{Value: new(uint64(10))},
		InputOrigin: "provider_reported", OutputOrigin: "provider_reported",
	}
}

func repriceUsageSummary(t *testing.T, models map[string][]modelusage.Attempt) summary.Envelope {
	t.Helper()
	doc := repriceSummary(t)
	var err error
	doc.ModelUsage, err = modelusage.New(repriceModelContract)
	if err != nil {
		t.Fatal(err)
	}
	doc.Sketches = map[string]summary.Payload{}
	doc.Counters = map[string]uint64{"requests": 0, "input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_write_input_tokens": 0, "missing_token_usage": 0}
	for id, attempts := range models {
		var row modelusage.Model
		for _, attempt := range attempts {
			row, err = modelusage.AddAttempt(row, attempt)
			if err != nil {
				t.Fatal(err)
			}
			if attempt.Input.Value == nil || attempt.Output.Value == nil {
				doc.Counters["missing_token_usage"]++
			}
		}
		doc.ModelUsage.Models[id] = row
		doc.Counters["requests"] += row.Requests
		doc.Counters["input_tokens"] += row.Fields.Input.Tokens
		doc.Counters["output_tokens"] += row.Fields.Output.Tokens
		doc.Counters["cache_read_input_tokens"] += row.Fields.CacheRead.Tokens
		doc.Counters["cache_write_input_tokens"] += row.Fields.CacheWrite.Tokens
	}
	return doc
}

func TestReadRepriceModelUsageCohortsAndExclusions(t *testing.T) {
	missing := repriceAttempt(100)
	missing.CacheWrite.Value = nil
	invalid := missing
	invalid.Input.Invalid = true
	zero := modelusage.Attempt{Input: modelusage.Observation{Value: new(uint64)}, Output: modelusage.Observation{Value: new(uint64)}, CacheRead: modelusage.Observation{Value: new(uint64)}, CacheWrite: modelusage.Observation{Value: new(uint64)}}
	knownZero := zero
	knownZero.InputOrigin, knownZero.OutputOrigin = "provider_reported", "provider_reported"
	models := map[string][]modelusage.Attempt{"model-a": {missing, invalid, zero, knownZero}, "model-z": {repriceAttempt(50)}}
	for _, n := range []uint64{99999, 100000, 100001, 200000, 200001, 272000, 272001} {
		models["model-a"] = append(models["model-a"], repriceAttempt(n))
	}
	for _, id := range []string{"__overflow__", "__missing__", "__too_long__", "__unmapped__"} {
		models[id] = []modelusage.Attempt{repriceAttempt(100), missing, invalid, zero}
	}
	doc := repriceUsageSummary(t, models)
	dir := writeRepriceSummaries(t, doc)
	path := filepath.Join(dir, "private-file-sentinel-000.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []reprice.Record
	source, err := ReadReprice(dir, repriceSummaryPeriod, []string{doc.ProducerID}, repriceModelContract, func(r reprice.Record) error {
		records = append(records, r)
		return nil
	})
	if err != nil || source.Rows != 28 || source.Outside != 0 || len(records) != 12 || len(source.Gaps) != 1 || strings.Contains(source.Gaps[0], "no per-model") {
		t.Fatalf("model coverage changed: %+v, records %d, %v", source, len(records), err)
	}
	var covered, excluded uint64
	var bins [][7]uint64
	issues := map[string]uint64{}
	previous := ""
	for _, r := range records {
		if r.Model < previous || r.Key != "" || r.Team != "" {
			t.Fatal("model order changed or scoped identity was invented")
		}
		previous = r.Model
		if r.Issue != "" {
			excluded += r.Count
			issues[r.Issue] += r.Count
			if r.Input != nil || r.Output != nil || r.CacheRead != nil || r.CacheWrite != nil || r.InputMin != nil || r.InputMax != nil {
				t.Fatal("excluded usage contains fabricated components")
			}
			continue
		}
		covered += r.Count
		if r.Input == nil || r.Output == nil || r.CacheRead == nil || r.CacheWrite == nil || r.InputMin == nil || r.InputMax == nil {
			t.Fatal("complete cohort lost components or extrema")
		}
		if r.Model == "model-a" {
			bins = append(bins, [7]uint64{r.Count, *r.Input, *r.Output, *r.CacheRead, *r.CacheWrite, *r.InputMin, *r.InputMax})
		}
	}
	want := [][7]uint64{{3, 199999, 40, 60, 20, 0, 100000}, {2, 300001, 40, 60, 20, 100001, 200000}, {2, 472001, 40, 60, 20, 200001, 272000}, {1, 272001, 20, 30, 10, 272001, 272001}}
	if !reflect.DeepEqual(bins, want) || covered != 9 || covered+excluded != source.Rows || !reflect.DeepEqual(issues, map[string]uint64{"invalid_usage": 1, "missing_split": 1, "zero_origin_unknown": 1, "unpriced_reserved_bucket": 16}) {
		t.Fatalf("cohorts were averaged, multiplied, or double-counted: %v, %v", bins, issues)
	}
	encoded, err := json.Marshal(source)
	if err != nil || strings.Contains(string(encoded), "sentinel") || strings.Contains(string(encoded), repriceModelContract) || strings.Contains(string(encoded), "model-a") {
		t.Fatal("source metadata leaked input identity")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("source was modified")
	}
}

func TestReadRepriceModelUsageLatestAndOwners(t *testing.T) {
	old := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100)}})
	old.ObservedEnd -= minute / 2
	latest := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100), repriceAttempt(200)}})
	latest.Sequence = 2
	other := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(50)}})
	other.ProducerID = "private-second-producer-sentinel"
	outside := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(300)}})
	outside.WindowStart -= minute
	outside.ObservedStart -= minute
	outside.ObservedEnd -= minute
	outside.EmittedAt -= minute
	for _, docs := range [][]summary.Envelope{{old, latest, latest, other, outside}, {outside, other, latest, old, latest}} {
		var records []reprice.Record
		source, err := ReadReprice(writeRepriceSummaries(t, docs...), repriceSummaryPeriod, []string{old.ProducerID, other.ProducerID}, repriceModelContract, func(r reprice.Record) error { records = append(records, r); return nil })
		if err != nil || source.Rows != 3 || source.Outside != 1 || len(records) != 1 || len(source.Gaps) != 1 {
			t.Fatalf("snapshot selection changed: %+v, %v", source, err)
		}
		r := records[0]
		if r.Count != 3 || r.Input == nil || *r.Input != 350 || r.InputMin == nil || *r.InputMin != 50 || r.InputMax == nil || *r.InputMax != 200 {
			t.Fatal("latest cumulative usage or merged extrema changed")
		}
	}
}

func TestReadRepriceModelUsageContractsBeforeVisits(t *testing.T) {
	for _, tc := range []string{"required", "invalid", "uppercase", "mismatch", "missing-block", "later-mismatch", "later-missing", "later-future", "later-partial"} {
		t.Run(tc, func(t *testing.T) {
			a := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100)}})
			b := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(200)}})
			b.WindowStart += minute
			b.ObservedStart += minute
			b.ObservedEnd += minute
			b.EmittedAt += minute
			contract := repriceModelContract
			period := "2020-01-02T00:00:00Z/2020-01-02T00:02:00Z"
			switch tc {
			case "required":
				contract = ""
			case "invalid":
				contract = "private-contract-sentinel"
			case "uppercase":
				contract = strings.ToUpper(contract)
			case "mismatch":
				contract = "model-usage-v1:" + strings.Repeat("b", 64)
			case "missing-block":
				a.ModelUsage, b.ModelUsage = nil, nil
			case "later-mismatch":
				b.ModelUsage.ContractID = "model-usage-v1:" + strings.Repeat("b", 64)
			case "later-missing":
				b.ModelUsage = nil
			case "later-future":
				b.EmittedAt = time.Date(2199, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano()
			case "later-partial":
				period = "2020-01-02T00:00:00Z/2020-01-02T00:01:30Z"
			}
			visits := 0
			source, err := ReadReprice(writeRepriceSummaries(t, a, b), period, []string{a.ProducerID}, contract, func(reprice.Record) error { visits++; return nil })
			if err == nil || visits != 0 || !reflect.DeepEqual(source, reprice.Source{}) || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("validation leaked or occurred after callbacks: visits %d, source %+v, %v", visits, source, err)
			}
		})
	}
}

func TestReadRepriceModelUsageReplayAndRegression(t *testing.T) {
	for _, tc := range []string{"conflict", "histogram", "extrema", "model-removed", "overlapping-epoch"} {
		t.Run(tc, func(t *testing.T) {
			a := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(90000), repriceAttempt(110000)}})
			b := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100000), repriceAttempt(100000)}})
			b.Sequence = 2
			switch tc {
			case "conflict":
				b.Sequence = 1
			case "extrema":
				a = repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100), repriceAttempt(200)}})
				b = repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(150), repriceAttempt(150)}})
				b.Sequence = 2
			case "model-removed":
				a = repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100)}, "model-b": {repriceAttempt(200)}})
				b = repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100), repriceAttempt(200)}})
				b.Sequence = 2
			case "overlapping-epoch":
				b.Epoch = "private-other-epoch-sentinel"
			}
			visits := 0
			source, err := ReadReprice(writeRepriceSummaries(t, a, b), repriceSummaryPeriod, []string{a.ProducerID}, repriceModelContract, func(reprice.Record) error { visits++; return nil })
			if err == nil || visits != 0 || !reflect.DeepEqual(source, reprice.Source{}) || strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("invalid cumulative history accepted or leaked: %+v, %v", source, err)
			}
		})
	}
}

func TestReadRepriceModelUsageEpochsAndCoverage(t *testing.T) {
	for _, partial := range []bool{false, true} {
		a := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(100)}})
		a.ObservedEnd -= minute / 2
		b := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-a": {repriceAttempt(200)}})
		b.Epoch = "private-restarted-epoch-sentinel"
		b.ObservedStart += minute / 2
		expected := []string{a.ProducerID}
		if partial {
			b.ObservedStart++
			expected = append(expected, "private-missing-producer-sentinel")
		}
		var count uint64
		source, err := ReadReprice(writeRepriceSummaries(t, a, b), repriceSummaryPeriod, expected, repriceModelContract, func(r reprice.Record) error { count += r.Count; return nil })
		if err != nil || count != 2 || source.Rows != 2 || (len(source.Gaps) == 2) != partial || strings.Contains(strings.Join(source.Gaps, " "), "sentinel") {
			t.Fatalf("epoch coverage lost: %+v, %v", source, err)
		}
	}
}

func TestReadRepriceModelUsageEmptyAndVisitorError(t *testing.T) {
	empty := repriceUsageSummary(t, nil)
	source, err := ReadReprice(writeRepriceSummaries(t, empty), repriceSummaryPeriod, []string{empty.ProducerID}, repriceModelContract, func(reprice.Record) error { t.Fatal("empty block fabricated a record"); return nil })
	if err != nil || source.Rows != 0 || len(source.Gaps) != 1 || strings.Contains(source.Gaps[0], "no per-model") {
		t.Fatalf("enabled empty block treated as absent: %+v, %v", source, err)
	}
	for _, id := range []string{"model-a", "__unmapped__"} {
		doc := repriceUsageSummary(t, map[string][]modelusage.Attempt{id: {repriceAttempt(100), repriceAttempt(200001)}})
		stop := errors.New("bounded aggregation limit")
		visits := 0
		source, err := ReadReprice(writeRepriceSummaries(t, doc), repriceSummaryPeriod, []string{doc.ProducerID}, repriceModelContract, func(reprice.Record) error { visits++; return stop })
		if !errors.Is(err, stop) || visits != 1 || !reflect.DeepEqual(source, reprice.Source{}) {
			t.Fatal("visitor error was not preserved or did not stop emission")
		}
	}
}

func TestReadRepriceModelUsageMalformedAndBounded(t *testing.T) {
	for _, tc := range []string{"null", "unknown-version", "base-conservation", "too-many-models", "oversized"} {
		t.Run(tc, func(t *testing.T) {
			doc := repriceUsageSummary(t, map[string][]modelusage.Attempt{"model-private-sentinel": {repriceAttempt(100)}})
			switch tc {
			case "unknown-version":
				doc.ModelUsage.Version = 2
			case "base-conservation":
				doc.Counters["input_tokens"]++
			case "too-many-models":
				for i := range modelusage.MaxModels {
					doc.ModelUsage.Models[fmt.Sprintf("model-%04d", i)] = modelusage.Model{}
				}
			}
			data, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			if tc == "null" {
				var object map[string]json.RawMessage
				if err := json.Unmarshal(data, &object); err != nil {
					t.Fatal(err)
				}
				object["model_usage"] = json.RawMessage("null")
				data, err = json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
			} else if tc == "oversized" {
				data = append(data, []byte(strings.Repeat(" ", summary.MaxBytes))...)
			}
			path := filepath.Join(t.TempDir(), "private-file-sentinel.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			source, err := ReadReprice(path, repriceSummaryPeriod, []string{doc.ProducerID}, repriceModelContract, func(reprice.Record) error { t.Fatal("malformed input reached visitor"); return nil })
			if err == nil || !reflect.DeepEqual(source, reprice.Source{}) || strings.Contains(err.Error(), "sentinel") {
				t.Fatal("malformed extension accepted or leaked")
			}
		})
	}
}
