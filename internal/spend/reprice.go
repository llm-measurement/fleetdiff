// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"errors"
	"fmt"
	"slices"
	"strings"
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
	return readReprice(path, period, visit, false)
}

// ReadRoutingReprice retains routing identities and reports source attribution
// before exclusions. Model plus deployment attests gateway selection, not an
// independently confirmed provider response. Legacy ReadReprice is unchanged.
func ReadRoutingReprice(path, period string, visit func(reprice.Record) error) (reprice.Source, error) {
	return readReprice(path, period, visit, true)
}

func readReprice(path, period string, visit func(reprice.Record) error, routing bool) (reprice.Source, error) {
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
	read := Read
	var callTypes map[string]*reprice.AttributionFields
	if routing {
		read = ReadRouting
		source.Attribution = &reprice.AttributionCoverage{IdentityBasis: "gateway-selected model+deployment; not provider-response confirmation"}
		callTypes = map[string]*reprice.AttributionFields{}
	}
	var seen requestDigests
	unsupported := false
	var failedUsage uint64
	err = read(path, func(row Row) error {
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
		if routing {
			r.Route, r.Agent = row.Values["original_model_group"], row.Values["agent_id"]
			modelKnown := routingModelState(row, "model") == routingKnown
			deploymentKnown := routingModelState(row, "model_id") == routingKnown
			r.ServedIdentityKnown = RoutingSelectedModelKnown(row)
			callType := row.Values["call_type"]
			if operation(callType) == "" && callType != "pass_through_endpoint" {
				callType = "unknown"
			}
			if callTypes[callType] == nil {
				callTypes[callType] = &reprice.AttributionFields{}
			}
			for _, fields := range []*reprice.AttributionFields{&source.Attribution.AttributionFields, callTypes[callType]} {
				fields.Rows++ // ReadRouting bounds all source rows to MaxRows.
				for _, field := range []struct {
					counts *reprice.IdentityCoverage
					known  bool
				}{
					{&fields.Route, routingTextState(row, "original_model_group") == routingKnown},
					{&fields.Agent, routingTextState(row, "agent_id") == routingKnown},
					{&fields.Model, modelKnown}, {&fields.Deployment, deploymentKnown},
					{&fields.ServedIdentity, r.ServedIdentityKnown},
				} {
					if field.known {
						field.counts.Known++
					} else {
						field.counts.Unknown++
					}
				}
			}
		}
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
	if routing {
		for name, fields := range callTypes {
			source.Attribution.ByCallType = append(source.Attribution.ByCallType, reprice.CallTypeAttribution{CallType: name, AttributionFields: *fields})
		}
		slices.SortFunc(source.Attribution.ByCallType, func(a, b reprice.CallTypeAttribution) int { return strings.Compare(a.CallType, b.CallType) })
	}
	return source, nil
}
