// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"errors"
	"fmt"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

// ReadReprice streams selected request-level observations without retaining
// identities or aggregating components. Rows counts selected rows, including
// unsupported call types, which visit as exclusions with unknown components;
// Outside counts all other rows. Discard callback results
// on error: duplicate detection and input validation can finish after visits.
// Visitor errors are returned unchanged and must be safe for display.
func ReadReprice(path, period string, visit func(reprice.Record) error) (reprice.Source, error) {
	if visit == nil {
		return reprice.Source{}, errors.New("repricing visitor is required")
	}
	p, err := ParsePeriod(period)
	if err != nil {
		return reprice.Source{}, err
	}
	now := time.Now().UTC()
	if p.End.After(now) {
		return reprice.Source{}, errors.New("select a completed repricing period ending before now")
	}
	key, _, err := secret("")
	if err != nil {
		return reprice.Source{}, err
	}
	defer clear(key)
	source := reprice.Source{Kind: "litellm-spend", Start: p.Start, End: p.End, Gaps: []string{}}
	var seen requestDigests
	unsupported := false
	var failedUsage uint64
	err = Read(path, func(row Row) error {
		if row.Values["request_id"] == "" {
			return errors.New("each request-level row needs a nonempty request_id")
		}
		seen.add(requestDigest(key, row.Values["request_id"]))
		start, err := timestamp(row.Values["startTime"])
		if err != nil {
			return err
		}
		if !p.contains(start) {
			source.Outside++
			return nil
		}
		source.Rows++
		r := reprice.Record{Model: row.Values["model"], Key: row.Values["api_key"], Team: row.Values["team_id"], Count: 1, Failed: row.Values["status"] == "failure"}
		if operation(row.Values["call_type"]) == "" {
			unsupported = true
			r.Issue = "unsupported_call_type"
			return visit(r)
		}
		if endText := row.Values["endTime"]; endText != "" {
			end, err := timestamp(endText)
			if err != nil {
				return err
			}
			if end.Before(start) {
				return errors.New("endTime precedes startTime; review the export")
			}
			if end.After(now) {
				return errors.New("export contains unfinished or future records; export completed requests")
			}
		}
		obs, err := accounting.Tokens(attributes(row))
		if err != nil {
			return err
		}
		for _, field := range []struct {
			name, metric string
			dst          **uint64
		}{
			{"input", "input_tokens_total", &r.Input},
			{"output", "output_tokens_total", &r.Output},
			{"cache_read_input", "cache_read_input_tokens_total", &r.CacheRead},
			{"cache_write_input", "cache_write_input_tokens_total", &r.CacheWrite},
		} {
			if validCacheField(obs, field.name) {
				n := obs.Metrics[accounting.Prefix+field.metric]
				*field.dst = &n
			}
		}
		q := classify(row, obs)
		switch {
		case q.Invalid != 0:
			r.Issue = "invalid_usage"
		case q.Missing != 0:
			r.Issue = "missing_usage"
		case q.TotalMismatch != 0:
			r.Issue = "component_total_mismatch"
		case q.TotalMissing != 0:
			r.Issue = "missing_total_field"
		case q.ZeroUnknown != 0:
			r.Issue = "zero_only_origin_unknown"
		case q.UnknownStatus != 0:
			r.Issue = "unknown_status"
		case row.Values["endTime"] == "":
			r.Issue = "missing_end_time"
		}
		// Repricing partitions inclusive input into disjoint cache components.
		if r.Input != nil && r.CacheRead != nil && r.CacheWrite != nil && *r.CacheWrite > *r.Input-*r.CacheRead {
			r.CacheRead, r.CacheWrite = nil, nil
			r.Issue = "invalid_usage"
		}
		if q.Failed != 0 && r.Issue == "" && r.Input != nil && r.Output != nil && r.CacheRead != nil && r.CacheWrite != nil {
			failedUsage++
		}
		return visit(r)
	})
	if err != nil {
		return reprice.Source{}, err
	}
	if seen.duplicated() {
		return reprice.Source{}, errors.New("duplicate request_id; export each request once from one consistent database snapshot")
	}
	if source.Rows == 0 {
		return reprice.Source{}, errors.New("no rows in the selected period; choose a period containing request-level rows")
	}
	if unsupported {
		source.Gaps = append(source.Gaps, "Unsupported call types are counted as excluded observations.")
	}
	if failedUsage != 0 {
		source.Gaps = append(source.Gaps, fmt.Sprintf("Complete recorded usage on failed requests: %d; billing finality is unknown.", failedUsage))
	}
	return source, nil
}
