// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"errors"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

// ReadReprice validates canonical summaries, selects the latest cumulative
// snapshots, and reports their attribution limits. The current collector exports
// aggregate components, not model/component records, so it never invokes visit.
// Rows and Outside count deduplicated model attempts, not snapshot files.
func ReadReprice(path, period string, expected []string, visit func(reprice.Record) error) (reprice.Source, error) {
	if visit == nil {
		return reprice.Source{}, errors.New("repricing visitor is required")
	}
	start, end, err := repricePeriod(period)
	if err != nil {
		return reprice.Source{}, err
	}
	now := time.Now().UTC()
	if end.After(now) {
		return reprice.Source{}, errors.New("select a completed repricing period ending before now")
	}
	docs, err := ReadSeries(path)
	if err != nil {
		return reprice.Source{}, err
	}
	// scanFrames composes validateWindow, Compatible, and Combine, including
	// replay conflicts, regressions, expected producers, and epoch intervals.
	frames, _, err := scanFrames(docs, expected)
	if err != nil {
		return reprice.Source{}, err
	}
	source := reprice.Source{Kind: "summaries", Start: start, End: end, Gaps: []string{
		"Current summaries have no per-model component split; top-k cannot recover model-by-key or model-by-team intersections.",
	}}
	selected, incomplete, missingCounts := false, false, false
	missingUsage, unknownUsage := false, false
	next := start.UnixNano()
	for _, f := range frames {
		requests, known := f.counters["requests"]
		if f.end <= start.UnixNano() || f.start >= end.UnixNano() {
			source.Outside, err = accounting.AddBounded(source.Outside, requests)
			if err != nil {
				return reprice.Source{}, err
			}
			continue
		}
		if f.start < start.UnixNano() || f.end > end.UnixNano() {
			return reprice.Source{}, errors.New("repricing period must contain whole summary windows; partial-window usage cannot be recovered")
		}
		if f.emitted > now.UnixNano() {
			return reprice.Source{}, errors.New("selected summary was emitted in the future; check producer clocks")
		}
		selected = true
		incomplete = incomplete || !f.complete || f.start != next
		missingCounts = missingCounts || !known
		missing, usageKnown := f.counters["missing_token_usage"]
		missingUsage = missingUsage || missing != 0
		unknownUsage = unknownUsage || !usageKnown
		next = f.end
		source.Rows, err = accounting.AddBounded(source.Rows, requests)
		if err != nil {
			return reprice.Source{}, err
		}
	}
	if !selected {
		return reprice.Source{}, errors.New("no summaries in the selected period; missing data is not zero")
	}
	if incomplete || next != end.UnixNano() {
		source.Gaps = append(source.Gaps, "The selected period has missing windows, missing producers, or partial observation intervals.")
	}
	if missingCounts {
		source.Gaps = append(source.Gaps, "Some selected summaries lack request counts; the observation count includes only recorded counts.")
	}
	if missingUsage {
		source.Gaps = append(source.Gaps, "Selected summaries record requests with missing input/output token usage; recorded request counts include these incomplete observations.")
	}
	if unknownUsage {
		source.Gaps = append(source.Gaps, "Some selected summaries lack token-usage coverage counts; token-usage completeness is unknown.")
	}
	return source, nil
}

// compare cannot import spend: spend already composes compare. Keep the same
// date/interval syntax using stdlib until a shared period contract is available.
func repricePeriod(period string) (time.Time, time.Time, error) {
	if len(period) == 10 {
		start, err := time.Parse("2006-01-02", period)
		if err == nil && start.Year() >= 1970 && start.Year() <= 2199 {
			return start, start.AddDate(0, 0, 1), nil
		}
	}
	a, b, ok := strings.Cut(period, "/")
	if ok {
		start, e1 := time.Parse(time.RFC3339Nano, a)
		end, e2 := time.Parse(time.RFC3339Nano, b)
		if e1 == nil && e2 == nil && start.Year() >= 1970 && start.Year() <= 2200 && end.Year() >= 1970 && end.Year() <= 2200 && start.Before(end) {
			return start.UTC(), end.UTC(), nil
		}
	}
	return time.Time{}, time.Time{}, errors.New("period must be a UTC date or RFC3339 start/end interval")
}
