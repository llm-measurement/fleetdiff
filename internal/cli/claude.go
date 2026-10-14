// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/clauderecords"
	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func runClaudeRecords(paths []string, before, after, group, secret, scope, format string, top int, hashes, dedup bool, out, errout io.Writer) int {
	if format != "text" && format != "json" || top < 1 || top > 100 || group != "user" && group != "session" {
		fmt.Fprintln(errout, "Claude logs need text/json, group-by user/session and top 1-100")
		return 2
	}
	r, err := clauderecords.Investigate(paths, clauderecords.Options{BeforePeriod: before, AfterPeriod: after, GroupBy: group, SecretEnv: secret, SourceScope: scope, Top: top, ShowHashes: hashes, DeduplicateExact: dedup})
	if err != nil {
		fmt.Fprintln(errout, err)
		return 1
	}
	var buf bytes.Buffer
	if format == "json" {
		e := json.NewEncoder(&buf)
		e.SetIndent("", "  ")
		if err := e.Encode(r); err != nil {
			fmt.Fprintln(errout, "cannot encode Claude log report")
			return 1
		}
	} else {
		renderClaudeRecords(&buf, r)
	}
	if _, err := io.Copy(out, &buf); err != nil {
		fmt.Fprintln(errout, "cannot write Claude log report")
		return 1
	}
	return 0
}

func measuredText(m clauderecords.Measured) string {
	if m.Value == nil {
		return "unknown"
	}
	return spendNumber(*m.Value)
}
func renderClaudeRecords(out io.Writer, r clauderecords.Report) {
	a, b := r.Before, r.After
	if len(r.ComparisonChecks) > 0 {
		fmt.Fprintf(out, "Record comparability needs review: %s -> %s observed api_request records; covered token totals %s -> %s.\n", spendNumber(a.Records), spendNumber(b.Records), measuredText(a.Total), measuredText(b.Total))
	} else {
		fmt.Fprintf(out, "Observed api_request token totals: %s -> %s, across %s -> %s records.\n", measuredText(a.Total), measuredText(b.Total), spendNumber(a.Records), spendNumber(b.Records))
	}
	fmt.Fprintln(out, "Unit: observed api_request records; finality unknown. Cancelled work may leave no event; retries can produce several records.")
	switch r.PeriodSelection {
	case "complete_utc_weeks":
		fmt.Fprintln(out, "Periods: the two most recent complete seven-day UTC periods within the observed timestamps; export coverage is unverified.")
	case "observed_span_halves":
		fmt.Fprintln(out, "Periods: two equal halves of the file set's observed span (fewer than 14 complete UTC days); export coverage is unverified.")
	}
	for _, p := range []struct {
		name string
		w    clauderecords.Window
	}{{"Before", a}, {"After", b}} {
		fmt.Fprintf(out, "%s: %s to %s (end exclusive).\n", p.name, p.w.Period.Start.Format("Mon 2006-01-02 15:04:05.999999999 MST"), p.w.Period.End.Format("Mon 2006-01-02 15:04:05.999999999 MST"))
	}
	fmt.Fprintln(out, "\nWhat changed?")
	for _, m := range r.Models {
		fmt.Fprintf(out, "  %s: %s -> %s covered tokens, %s -> %s records.\n", m.Name, measuredText(m.Before.Total), measuredText(m.After.Total), spendNumber(m.Before.Records), spendNumber(m.After.Records))
	}
	if d := r.Decomposition; d != nil {
		fmt.Fprintf(out, "  Record-count contribution: %+.0f tokens; tokens-per-record contribution: %+.0f (arithmetic split).\n", d.RecordContribution, d.SizeContribution)
	}
	fmt.Fprintf(out, "  Token coverage: %s of %s -> %s of %s records have all four components.\n", spendNumber(a.Total.Records), spendNumber(a.Records), spendNumber(b.Total.Records), spendNumber(b.Records))
	if a.Cost != nil || b.Cost != nil {
		cost := func(s *string) string {
			if s == nil {
				return "unknown"
			}
			return "USD " + *s
		}
		fmt.Fprintf(out, "  Recorded cost: %s -> %s (%s -> %s covered records; same unknown finality).\n", cost(a.Cost), cost(b.Cost), spendNumber(a.CostRecords), spendNumber(b.CostRecords))
	}
	if a.Cache.Input.Value != nil || b.Cache.Input.Value != nil {
		fmt.Fprintf(out, "  Cache reads: %s of %s input tokens -> %s of %s (%s -> %s covered records).\n", measuredText(a.Cache.Read), measuredText(a.Cache.Input), measuredText(b.Cache.Read), measuredText(b.Cache.Input), spendNumber(a.Cache.Input.Records), spendNumber(b.Cache.Input.Records))
		fmt.Fprintf(out, "  Cache writes: %s -> %s; uncached input: %s -> %s.\n", measuredText(a.Cache.Write), measuredText(b.Cache.Write), measuredText(a.Cache.Uncached), measuredText(b.Cache.Uncached))
	}
	claudeRanking(out, "Who drove the observed token change?", r.Rankings, r.GroupBy)
	claudeRanking(out, "Who drove the uncached input change?", r.UncachedRankings, r.GroupBy)
	fmt.Fprintf(out, "  Unattributed records: %s -> %s. Installation aliases identify installations, not people.\n", spendNumber(a.Unattributed), spendNumber(b.Unattributed))
	fmt.Fprintln(out, "\nComparison checks")
	for _, c := range r.ComparisonChecks {
		fmt.Fprintf(out, "  %s\n", claudeCheckText(c))
	}
	if len(r.ComparisonChecks) == 0 {
		fmt.Fprintln(out, "  Observed versions, capture profiles, attribution and record mix are consistent; this does not establish final usage or capture completeness.")
	}
	fmt.Fprintf(out, "  Prompt-attributed records / scoped prompts: %s / %s -> %s / %s.\n", spendNumber(a.PromptRecords), spendNumber(a.Prompts), spendNumber(b.PromptRecords), spendNumber(b.Prompts))
	fmt.Fprintf(out, "  api_error events: %s -> %s; retries_exhausted companions: %s -> %s (separate events, not additional requests).\n", spendNumber(a.ErrorEvents), spendNumber(b.ErrorEvents), spendNumber(a.ExhaustedEvents), spendNumber(b.ExhaustedEvents))
	keys := map[string]bool{}
	for k := range a.Quality {
		keys[k] = true
	}
	for k := range b.Quality {
		keys[k] = true
	}
	var names []string
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if k == "cost_decimal" {
			continue
		}
		if a.Quality[k]+b.Quality[k] > 0 {
			fmt.Fprintf(out, "  %s: %s -> %s records.\n", strings.ReplaceAll(k, "_", " "), spendNumber(a.Quality[k]), spendNumber(b.Quality[k]))
		}
	}
	if a.DuplicateCopies+b.DuplicateCopies > 0 {
		fmt.Fprintf(out, "  Exact exported copies after the first: %s -> %s; %s.\n", spendNumber(a.DuplicateCopies), spendNumber(b.DuplicateCopies), r.Deduplication)
	}
	fmt.Fprintln(out, "\nNext: check record mix and capture coverage before interpreting this as a workload change. Use native receipts for final usage.")
}
func claudeRanking(out io.Writer, title string, c compare.Concentration, group string) {
	fmt.Fprintln(out, "\n"+title)
	if len(c.Movers) == 0 {
		fmt.Fprintln(out, "  No attributed token weights available.")
		return
	}
	for _, m := range c.Movers {
		fmt.Fprintf(out, "  %s: %s -> %s tokens (%s).", m.Item, spendInterval(m.Before), spendInterval(m.After), signedInterval(m.Delta))
		if m.Hash != "" {
			fmt.Fprintf(out, " hash=%s", m.Hash)
		}
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "  Grouping: %s; sessions are scoped to the available account or installation identity.\n", group)
}
func claudeCheckText(code string) string {
	return map[string]string{
		"error_status_mix_changed":           "The mix of rate-limit, server and other API errors changed.",
		"period_without_api_request_records": "One period has no api_request records: missing observations are not zero usage.",
		"incomplete_token_components":        "Token components are incomplete or invalid; totals cover only complete records.",
		"unverified_source_profile":          "Source version or capture profile differs from the tested 2.1.296 recipe, or is missing.",
		"source_version_mix_changed":         "Source-version mix changed.", "capture_profile_mix_changed": "Capture-profile mix changed.",
		"identity_source_mix_changed": "Account/email/installation attribution mix changed.", "incomplete_prompt_identity": "Some records lack an owner, session or prompt identity for the per-prompt check.",
		"records_per_prompt_changed": "Records per scoped prompt changed; retry or request mix may differ.", "error_or_exhaustion_mix_changed": "Error or retry-exhaustion event counts changed relative to observed records.",
		"attribution_coverage_changed": "Attribution coverage changed.", "exact_export_copies_retained": "Exact exported copies were retained; use --deduplicate-exact only for overlapping exports.",
	}[code]
}
