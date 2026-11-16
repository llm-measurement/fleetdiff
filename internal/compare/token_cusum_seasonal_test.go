// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/change"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const seasonalTestDay = int64(24 * time.Hour)

func seasonalTokenCUSUMSeries(t testing.TB, count int) []summary.Envelope {
	t.Helper()
	docs := tokenCUSUMSeries(t, count)
	origin := docs[0].WindowStart
	start := time.Date(2026, time.January, 4, 0, 0, 0, 0, time.UTC).UnixNano() // Sunday.
	for i := range docs {
		doc := &docs[i]
		doc.WindowStart = start + (doc.WindowStart-origin)*(seasonalTestDay/minute)
		doc.WindowDuration *= seasonalTestDay / minute
		doc.ObservedStart = start + (doc.ObservedStart-origin)*(seasonalTestDay/minute)
		doc.ObservedEnd = start + (doc.ObservedEnd-origin)*(seasonalTestDay/minute)
		doc.EmittedAt = start + (doc.EmittedAt-origin)*(seasonalTestDay/minute)
		if err := doc.Validate(); err != nil {
			t.Fatalf("invalid daily fixture %d: %v", i, err)
		}
	}
	return docs
}

func seasonalTokenCUSUMOptions() TokenCUSUMOptions {
	o := tokenCUSUMOptions()
	o.Stratum = TokenCUSUMWeekdayStratum
	return o
}

func seasonalTokenCUSUMScanOptions(docs []summary.Envelope, baseline, recent int) ScanOptions {
	o := DefaultScanOptions()
	o.Expected = []string{"app"}
	o.Baseline, o.Recent = baseline, recent
	o.Now = docs[len(docs)-1].WindowStart + seasonalTestDay
	return o
}

func seasonalTokenCUSUMJSON(t testing.TB, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func scanSeasonalTokenCUSUM(t testing.TB, docs []summary.Envelope, o ScanOptions) TokenCUSUMScanReport {
	t.Helper()
	before := seasonalTokenCUSUMJSON(t, docs)
	legacy, err := Scan(docs, o)
	if err != nil {
		t.Fatalf("legacy fixture rejected: %v", err)
	}
	report, err := ScanTokenCUSUM(docs, o, seasonalTokenCUSUMOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(seasonalTokenCUSUMJSON(t, legacy), seasonalTokenCUSUMJSON(t, report.ScanReport)) {
		t.Fatal("seasonal diagnostic changed legacy scan bytes")
	}
	if !bytes.Equal(before, seasonalTokenCUSUMJSON(t, docs)) {
		t.Fatal("seasonal diagnostic mutated input")
	}
	analysis := report.TokenCUSUM
	if analysis.Unit != "observed_tokens" || analysis.Reference != o.Baseline {
		t.Fatalf("wrong units or matching reference count: %+v", analysis)
	}
	metadata := analysis.Seasonality
	if metadata == nil || metadata.Key != "utc-day-of-week" || metadata.Timezone != "UTC" ||
		metadata.Cadence != "24h" || metadata.KeyConvention != "Sunday=0 through Saturday=6" ||
		metadata.ReferenceUnit != "preceding matching weekly windows" {
		t.Fatalf("missing or incorrect declared seasonality: %+v", metadata)
	}
	end := o.Now - o.Now%seasonalTestDay
	start := max(int64(0), end-int64(o.Recent)*seasonalTestDay)
	if len(analysis.Windows) != int((end-start)/seasonalTestDay) {
		t.Fatalf("wrong wall-clock horizon: got %d windows for [%d,%d)", len(analysis.Windows), start, end)
	}
	for i, window := range analysis.Windows {
		wantStart := start + int64(i)*seasonalTestDay
		key := int(time.Unix(0, wantStart).UTC().Weekday())
		if window.Start != wantStart || window.End != wantStart+seasonalTestDay || window.StratumKey == nil || *window.StratumKey != key {
			t.Fatalf("wrong UTC position or weekday at %d: %+v, want start %d key %d", i, window, wantStart, key)
		}
	}
	return report
}

func requireSeasonalCUSUMScore(t testing.TB, window TokenCUSUMWindow, samples int, median, mad, statistic float64) {
	t.Helper()
	if window.Status != change.CUSUMUncalibrated || window.Diagnostic == nil || window.Samples != samples || window.Total == nil || window.Reason != "" {
		t.Fatalf("expected scored matching history: %+v", window)
	}
	score := window.Diagnostic
	if math.Abs(score.Median-median) > 1e-9 || math.Abs(score.MAD-mad) > 1e-9 || math.Abs(score.Statistic-statistic) > 1e-9 {
		t.Fatalf("wrong isolated causal score: got %+v, want median %g MAD %g statistic %g", score, median, mad, statistic)
	}
}

func TestTokenCUSUMSeasonalCausalMedianMADAndFutureExclusion(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 56)
	for i := range docs {
		weekday, week := i%7, i/7
		factor := uint64(weekday + 1)
		offset := uint64(week * 10)
		if week >= 6 {
			offset = 100
		}
		docs[i].Counters["input_tokens"] = 1000*factor + offset*factor
	}
	o := seasonalTokenCUSUMScanOptions(docs, 6, 14)
	report := scanSeasonalTokenCUSUM(t, docs, o)
	if report.Status == "not_ready" || report.TokenCUSUM.Status != change.CUSUMUncalibrated {
		t.Fatal("complete matching history was not scored", report.TokenCUSUM.Status)
	}
	for key := range 7 {
		factor := float64(key + 1)
		scale := 1.4826 * 15 * factor
		first := 75*factor/scale - 1
		requireSeasonalCUSUMScore(t, report.TokenCUSUM.Windows[key], 6, 1025*factor, 15*factor, first)
		requireSeasonalCUSUMScore(t, report.TokenCUSUM.Windows[7+key], 6, 1035*factor, 15*factor, first+65*factor/scale-1)
	}
	// Changing later observations must not alter already scored positions.
	for i := 49; i < len(docs); i++ {
		docs[i].Counters["input_tokens"] = 900_000 + uint64(i)
	}
	later := scanSeasonalTokenCUSUM(t, docs, o)
	if !bytes.Equal(seasonalTokenCUSUMJSON(t, report.TokenCUSUM.Windows[:7]), seasonalTokenCUSUMJSON(t, later.TokenCUSUM.Windows[:7])) {
		t.Fatal("future observations changed earlier references or scores")
	}
	// Fully open and future snapshots are present in input but outside as-of.
	o.Now = docs[48].WindowStart + seasonalTestDay
	o.Recent = 7
	closed := scanSeasonalTokenCUSUM(t, docs[:49], o)
	withFuture := scanSeasonalTokenCUSUM(t, docs, o)
	if !bytes.Equal(seasonalTokenCUSUMJSON(t, closed), seasonalTokenCUSUMJSON(t, withFuture)) {
		t.Fatal("open or future buckets entered the reference")
	}
}

func TestTokenCUSUMSeasonalWeekdayStatisticsAreIndependent(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 56)
	for i := range docs {
		docs[i].Counters["input_tokens"] = uint64(1000 * (i%7 + 1))
	}
	docs[42].Counters["input_tokens"] = 1010
	docs[49].Counters["input_tokens"] = 1010
	o := seasonalTokenCUSUMScanOptions(docs, 6, 14)
	report := scanSeasonalTokenCUSUM(t, docs, o)
	for i, window := range report.TokenCUSUM.Windows {
		statistic := float64(0)
		if i%7 == 0 {
			statistic = float64(9 * (i/7 + 1))
		}
		requireSeasonalCUSUMScore(t, window, 6, float64(1000*(i%7+1)), 0, statistic)
	}
	// Neither the reference nor the accumulated statistic may cross weekdays.
	for i := range docs {
		if i%7 != 0 {
			docs[i].Counters["input_tokens"] = uint64(100_000 + i*i)
		}
	}
	changed := scanSeasonalTokenCUSUM(t, docs, o)
	for _, i := range []int{0, 7} {
		if !bytes.Equal(seasonalTokenCUSUMJSON(t, report.TokenCUSUM.Windows[i]), seasonalTokenCUSUMJSON(t, changed.TokenCUSUM.Windows[i])) {
			t.Fatal("another weekday changed Sunday's reference or statistic")
		}
	}
}

func TestTokenCUSUMSeasonalRequiresFullMatchingWarmup(t *testing.T) {
	for _, baseline := range []int{6, 24, 384} {
		t.Run(fmt.Sprint(baseline), func(t *testing.T) {
			count := 7*baseline + 1
			if baseline == 384 {
				// A valid large count is not a promise that enough history was read.
				count = 49
			}
			docs := seasonalTokenCUSUMSeries(t, count)
			o := seasonalTokenCUSUMScanOptions(docs, baseline, 1)
			if baseline == 384 {
				report := scanSeasonalTokenCUSUM(t, docs, o)
				window := report.TokenCUSUM.Windows[0]
				if window.Status != change.CUSUMInsufficient || window.Samples != 6 || window.Diagnostic != nil || window.Reason == "" {
					t.Fatalf("large baseline pooled daily observations: %+v", window)
				}
				return
			}
			o.Now = docs[count-2].WindowStart + seasonalTestDay
			short := scanSeasonalTokenCUSUM(t, docs, o).TokenCUSUM.Windows[0]
			if short.Status != change.CUSUMInsufficient || short.Samples != baseline-1 || short.Diagnostic != nil || short.Total == nil || short.Reason == "" {
				t.Fatalf("scored before N matching weekly references: %+v", short)
			}
			o.Now += seasonalTestDay
			full := scanSeasonalTokenCUSUM(t, docs, o)
			requireSeasonalCUSUMScore(t, full.TokenCUSUM.Windows[0], baseline, 1000, 0, 0)
		})
	}
}

func TestTokenCUSUMSeasonalOldHistoryEligibilityIsWeekdayLocal(t *testing.T) {
	for _, issue := range []string{
		"missing bucket", "partial interval", "missing producer",
		"reported mismatch", "invalid input", "conflicting output", "subset violation", "dedup bypass",
		"no attempts", "inexact total", "future emission",
	} {
		t.Run(issue, func(t *testing.T) {
			docs := seasonalTokenCUSUMSeries(t, 49)
			for i := range docs {
				docs[i].Counters["token_observations.input.subset_violation"] = 0
				docs[i].Counters["dedup_key_missing"] = 0
			}
			o := seasonalTokenCUSUMScanOptions(docs, 6, 7)
			switch issue {
			case "missing bucket":
				docs = slices.Delete(docs, 0, 1)
			case "partial interval":
				docs[0].ObservedEnd--
			case "missing producer":
				o.Expected = []string{"app", "other"}
				for i := 1; i < 49; i++ {
					other := docs[i]
					other.ProducerID = "other"
					docs = append(docs, other)
				}
			case "reported mismatch":
				docs[0].Counters["token_observations.input.reported"]--
			case "invalid input":
				docs[0].Counters["token_observations.input.invalid"] = 1
			case "conflicting output":
				docs[0].Counters["token_observations.output.conflict"] = 1
			case "subset violation":
				docs[0].Counters["token_observations.input.subset_violation"] = 1
			case "dedup bypass":
				docs[0].Counters["dedup_key_missing"] = 1
			case "no attempts":
				docs[0].Counters["requests"] = 0
				setTokenCUSUMQuality(docs[0].Counters, 0)
			case "inexact total":
				docs[0].Counters["input_tokens"] = maxExactCUSUMTokens + 1
			case "future emission":
				docs[0].EmittedAt = o.Now + 1
			}
			report := scanSeasonalTokenCUSUM(t, docs, o)
			if report.Status == "not_ready" {
				t.Fatal("old-history fixture accidentally exercised only the global veto")
			}
			window := report.TokenCUSUM.Windows[0]
			if report.TokenCUSUM.Status != change.CUSUMInsufficient || window.Status != change.CUSUMInsufficient ||
				window.Samples != 5 || window.Diagnostic != nil || window.Total == nil || window.Reason == "" {
				t.Fatalf("old ineligible Sunday was skipped, accepted, or treated as zero: %+v", window)
			}
			for _, other := range report.TokenCUSUM.Windows[1:] {
				median := float64(1000)
				if issue == "missing producer" {
					median = 2000
				}
				requireSeasonalCUSUMScore(t, other, 6, median, 0, 0)
			}
		})
	}
}

func TestTokenCUSUMSeasonalGapPositionsAndWeeklyRewarming(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 99)
	docs[42].Counters["input_tokens"] = 4000
	o := seasonalTokenCUSUMScanOptions(docs, 6, 1)
	missingStart := docs[49].WindowStart
	docs = slices.Delete(docs, 49, 50)
	for occurrence := 0; occurrence <= 7; occurrence++ {
		t.Run(fmt.Sprint(occurrence), func(t *testing.T) {
			o.Now = missingStart + int64(7*occurrence+1)*seasonalTestDay
			report := scanSeasonalTokenCUSUM(t, docs, o)
			if report.Status == "not_ready" {
				t.Fatal("weekly gap must be outside the legacy baseline or be the missing current position")
			}
			window := report.TokenCUSUM.Windows[0]
			if occurrence == 0 {
				if window.Total != nil || window.Status != change.CUSUMInsufficient || window.Samples != 0 || window.Reason == "" || window.Diagnostic != nil {
					t.Fatalf("missing wall-clock current position became zero or was omitted: %+v", window)
				}
			} else if occurrence <= 6 {
				if window.Status != change.CUSUMInsufficient || window.Samples != occurrence-1 || window.Diagnostic != nil || window.Total == nil || window.Reason == "" {
					t.Fatalf("weekly gap bridged or rewarm completed early: %+v", window)
				}
			} else {
				requireSeasonalCUSUMScore(t, window, 6, 1000, 0, 0)
			}
		})
	}
}

func TestTokenCUSUMSeasonalIneligibleOccurrenceResetsOnlyItsWeekday(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 99)
	docs[42].Counters["input_tokens"] = 4000
	docs[43].Counters["input_tokens"] = 1010
	docs[49].Counters["token_observations.input.invalid"] = 1
	docs[98].Counters["input_tokens"] = 1010
	o := seasonalTokenCUSUMScanOptions(docs, 6, 57)
	report := scanSeasonalTokenCUSUM(t, docs, o)
	if report.Status == "not_ready" {
		t.Fatal("quality-only defect must not trigger legacy readiness")
	}
	requireSeasonalCUSUMScore(t, report.TokenCUSUM.Windows[0], 6, 1000, 0, 2999)
	for i, window := range report.TokenCUSUM.Windows {
		if i%7 == 1 {
			requireSeasonalCUSUMScore(t, window, 6, 1000, 0, float64(9-i/7))
		}
		if i%7 != 0 || i == 0 {
			continue
		}
		if i == 56 {
			// The old large statistic must not survive the ineligible Sunday.
			requireSeasonalCUSUMScore(t, window, 6, 1000, 0, 9)
			continue
		}
		wantSamples := max(0, i/7-2)
		if window.Status != change.CUSUMInsufficient || window.Diagnostic != nil || window.Samples != wantSamples || window.Reason == "" {
			t.Fatalf("ineligible occurrence did not restart matching warmup: %+v", window)
		}
		if i == 7 && window.Total != nil {
			t.Fatal("ineligible occurrence exposed a usable total")
		}
	}
}

func TestTokenCUSUMSeasonalUTCBoundariesAndSundayZero(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 57)
	o := seasonalTokenCUSUMScanOptions(docs[:56], 6, 7)
	for _, offset := range []int64{-1, 0, 1, seasonalTestDay - 1} {
		t.Run(fmt.Sprint(offset), func(t *testing.T) {
			at := o
			at.Now += offset
			report := scanSeasonalTokenCUSUM(t, docs, at)
			for _, window := range report.TokenCUSUM.Windows {
				if *window.StratumKey == 0 && !bytes.Contains(seasonalTokenCUSUMJSON(t, window), []byte(`"stratum_key":0`)) {
					t.Fatal("Sunday key zero disappeared from serialized output")
				}
				if window.End > at.Now {
					t.Fatal("open UTC day was included")
				}
			}
		})
	}
	// Sunday midnight in India is still Saturday in UTC.
	at := o
	at.Now = time.Date(2026, time.March, 1, 0, 0, 0, 0, time.FixedZone("IST", 19800)).UnixNano()
	report := scanSeasonalTokenCUSUM(t, docs, at)
	last := report.TokenCUSUM.Windows[len(report.TokenCUSUM.Windows)-1]
	if *last.StratumKey != int(time.Friday) {
		t.Fatal("weekday followed a local timezone instead of UTC")
	}
}

func TestTokenCUSUMSeasonalWarmupClampsAtUnixEpoch(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 8)
	shift := docs[0].WindowStart
	for i := range docs {
		docs[i].WindowStart -= shift
		docs[i].ObservedStart -= shift
		docs[i].ObservedEnd -= shift
		docs[i].EmittedAt -= shift
	}
	o := seasonalTokenCUSUMScanOptions(docs, 6, 1)
	report := scanSeasonalTokenCUSUM(t, docs, o)
	window := report.TokenCUSUM.Windows[0]
	if window.Status != change.CUSUMInsufficient || window.Diagnostic != nil || window.Samples != 1 || *window.StratumKey != int(time.Thursday) {
		t.Fatalf("epoch warmup wrapped or used the wrong weekday: %+v", window)
	}
}

func TestTokenCUSUMSeasonalRetainsGlobalReadinessVeto(t *testing.T) {
	for _, issue := range []string{"recent gap", "baseline partial", "recent future emission", "stale", "short"} {
		t.Run(issue, func(t *testing.T) {
			docs := seasonalTokenCUSUMSeries(t, 56)
			o := seasonalTokenCUSUMScanOptions(docs, 6, 7)
			switch issue {
			case "recent gap":
				docs = slices.Delete(docs, 52, 53)
			case "baseline partial":
				docs[44].ObservedEnd--
			case "recent future emission":
				docs[53].EmittedAt = o.Now + 1
			case "stale":
				o.Now += 10 * seasonalTestDay
			case "short":
				docs = docs[len(docs)-8:]
			}
			report := scanSeasonalTokenCUSUM(t, docs, o)
			if report.Status != "not_ready" || report.TokenCUSUM.Status != change.CUSUMInsufficient {
				t.Fatal("existing readiness veto was not preserved", report.Status)
			}
			for _, window := range report.TokenCUSUM.Windows {
				if window.Status != change.CUSUMInsufficient || window.Diagnostic != nil || window.Total != nil || window.Reason == "" {
					t.Fatalf("seasonal score bypassed global readiness: %+v", window)
				}
			}
		})
	}
}

func TestTokenCUSUMSeasonalSchemaChangesPreserveLegacyRefusal(t *testing.T) {
	for _, counter := range []string{"input_tokens", "output_tokens", "missing_token_usage", "requests", "token_observations.output.conflict"} {
		t.Run(counter, func(t *testing.T) {
			docs := seasonalTokenCUSUMSeries(t, 49)
			o := seasonalTokenCUSUMScanOptions(docs, 6, 7)
			delete(docs[0].Counters, counter)
			before := seasonalTokenCUSUMJSON(t, docs)
			_, legacyErr := Scan(docs, o)
			_, seasonalErr := ScanTokenCUSUM(docs, o, seasonalTokenCUSUMOptions())
			if legacyErr == nil || seasonalErr == nil || seasonalErr.Error() != legacyErr.Error() {
				t.Fatalf("measurement contract refusal changed: legacy %v, seasonal %v", legacyErr, seasonalErr)
			}
			if !bytes.Equal(before, seasonalTokenCUSUMJSON(t, docs)) {
				t.Fatal("rejected schema change mutated input")
			}
		})
	}
}

func TestTokenCUSUMSeasonalRefusesUnsupportedOptionsAndIdentity(t *testing.T) {
	for _, issue := range []string{"unknown mode", "minute cadence", "half-day cadence", "two-day cadence", "unaligned day", "duration baseline", "baseline below minimum", "baseline above maximum", "scale", "shift", "key", "scope", "accounting"} {
		t.Run(issue, func(t *testing.T) {
			docs := seasonalTokenCUSUMSeries(t, 49)
			o := seasonalTokenCUSUMScanOptions(docs, 6, 7)
			options := seasonalTokenCUSUMOptions()
			switch issue {
			case "unknown mode":
				options.Stratum = "PRIVATE_SENTINEL"
			case "minute cadence":
				docs = tokenCUSUMSeries(t, 49)
				o.Now = 50 * minute
			case "half-day cadence":
				for i := range docs {
					docs[i].WindowStart /= 2
					docs[i].WindowDuration /= 2
					docs[i].ObservedStart /= 2
					docs[i].ObservedEnd /= 2
					docs[i].EmittedAt /= 2
				}
				o.Now /= 2
			case "two-day cadence":
				for i := range docs {
					docs[i].WindowStart *= 2
					docs[i].WindowDuration *= 2
					docs[i].ObservedStart *= 2
					docs[i].ObservedEnd *= 2
					docs[i].EmittedAt *= 2
					if docs[i].WindowStart%seasonalTestDay != 0 {
						t.Fatal("cadence-only fixture must remain UTC midnight-aligned")
					}
				}
				o.Now *= 2
				// The envelope schema caps durations at 24h; test this guard
				// directly so earlier schema validation cannot mask its removal.
				if err := validateWeekdayCUSUMCadence(docs[0].WindowDuration); err == nil {
					t.Fatal("seasonal cadence guard accepted aligned two-day windows")
				}
			case "unaligned day":
				for i := range docs {
					docs[i].WindowStart += int64(time.Hour)
					docs[i].ObservedStart += int64(time.Hour)
					docs[i].ObservedEnd += int64(time.Hour)
					docs[i].EmittedAt += int64(time.Hour)
				}
				o.Now += int64(time.Hour)
			case "duration baseline":
				o.BaselineDuration = 6 * seasonalTestDay
			case "baseline below minimum":
				o.Baseline = 5
			case "baseline above maximum":
				o.Baseline = 385
			case "scale":
				options.ScaleFloor = math.NaN()
			case "shift":
				options.MinShift = 0
			case "key":
				docs[0].KeyID = "PRIVATE_SENTINEL"
			case "scope":
				docs[0].ScopeID = "PRIVATE_SENTINEL"
			case "accounting":
				docs[0].AccountingID = "PRIVATE_SENTINEL"
			}
			before := seasonalTokenCUSUMJSON(t, docs)
			if _, err := ScanTokenCUSUM(docs, o, options); err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
				t.Fatalf("unsupported option accepted or private input leaked: %v", err)
			}
			if !bytes.Equal(before, seasonalTokenCUSUMJSON(t, docs)) {
				t.Fatal("rejected input was mutated")
			}
		})
	}
}

func TestTokenCUSUMSeasonalSnapshotSelectionAndDeterministicOrder(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 49)
	latest := docs[42]
	latest.Sequence++
	latest.Counters = maps.Clone(latest.Counters)
	latest.Counters["input_tokens"] = 4000
	docs = append(docs, latest, latest)
	o := seasonalTokenCUSUMScanOptions(docs[:49], 6, 7)
	first := scanSeasonalTokenCUSUM(t, docs, o)
	requireSeasonalCUSUMScore(t, first.TokenCUSUM.Windows[0], 6, 1000, 0, 2999)
	if *first.TokenCUSUM.Windows[0].Total != 4000 {
		t.Fatal("cumulative snapshots were added rather than replaced")
	}
	for range 3 {
		slices.Reverse(docs)
		second := scanSeasonalTokenCUSUM(t, docs, o)
		if !bytes.Equal(seasonalTokenCUSUMJSON(t, first), seasonalTokenCUSUMJSON(t, second)) {
			t.Fatal("input order changed seasonal output")
		}
	}
	docs[0].Counters = maps.Clone(docs[0].Counters)
	docs[0].Counters["input_tokens"]++
	if _, err := ScanTokenCUSUM(docs, o, seasonalTokenCUSUMOptions()); err == nil {
		t.Fatal("conflicting replay bypassed existing source validation")
	}
}

func TestTokenCUSUMSeasonalDefaultHistoryFitsExistingBoundedReader(t *testing.T) {
	docs := seasonalTokenCUSUMSeries(t, 169)
	directory := t.TempDir()
	for i, doc := range docs {
		data, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		// File order deliberately opposes chronological order.
		name := filepath.Join(directory, fmt.Sprintf("%03d.json", len(docs)-i))
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	read, err := ReadSeries(directory)
	if err != nil || len(read) != 169 {
		t.Fatalf("default 24-week history did not fit existing reader: %d, %v", len(read), err)
	}
	o := seasonalTokenCUSUMScanOptions(docs, 24, 1)
	report := scanSeasonalTokenCUSUM(t, read, o)
	requireSeasonalCUSUMScore(t, report.TokenCUSUM.Windows[0], 24, 1000, 0, 0)
	data, err := docs[0].MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for i := len(docs) + 1; i <= 513; i++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("%03d.json", i)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadSeries(directory); err == nil || !strings.Contains(err.Error(), "512") {
		t.Fatalf("seasonal integration changed existing file bound: %v", err)
	}
}

func TestTokenCUSUMSeasonalEmptyModePreservesNonseasonalGolden(t *testing.T) {
	docs := tokenCUSUMSeries(t, 25)
	o := scanOptions(25)
	legacy, err := Scan(docs, o)
	if err != nil {
		t.Fatal(err)
	}
	report, err := ScanTokenCUSUM(docs, o, tokenCUSUMOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(seasonalTokenCUSUMJSON(t, legacy), seasonalTokenCUSUMJSON(t, report.ScanReport)) {
		t.Fatal("empty mode changed legacy scan bytes")
	}
	const want = `{"status":"uncalibrated","unit":"observed_tokens","clock":"collector_processing_time","reference_windows":24,"scale_floor":1,"minimum_shift":2,"windows":[{"start_unix_nano":1500000000000,"end_unix_nano":1560000000000,"status":"uncalibrated","observed_tokens":1000,"reference_samples":24,"diagnostic":{"median":1000,"mad":0,"scale":1,"residual":0,"statistic":0}}],"notes":["Calibration is pending; these scores are diagnostics rather than alert verdicts.","The measurement is observed input plus output tokens, not cost.","One compatible nonseasonal stratum; exclude declared incidents before evaluation.","Missing or ineligible positions reset the rolling reference and statistic; no refractory period.","Expected producers declare disjoint ownership; complete intervals do not prove every physical request arrived."]}`
	if got := string(seasonalTokenCUSUMJSON(t, report.TokenCUSUM)); got != want {
		t.Fatalf("nonseasonal diagnostic bytes changed:\ngot  %s\nwant %s", got, want)
	}
}
