// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func runSpend(path, before, after, group, secret, format string, top int, hashes bool, out, errout io.Writer) int {
	fail := func(code int, msg string) int { fmt.Fprintln(errout, msg); return code }
	if format != "text" && format != "json" || top < 1 || top > 100 {
		return fail(2, "use text or json output and top between 1 and 100")
	}
	r, err := spend.Investigate(path, spend.Options{BeforePeriod: before, AfterPeriod: after, GroupBy: group, SecretEnv: secret, Top: top, ShowHashes: hashes})
	if err != nil {
		return fail(1, err.Error())
	}
	var b bytes.Buffer
	if format == "json" {
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return fail(1, "cannot encode spend investigation")
		}
	} else {
		renderSpend(&b, r)
	}
	if _, err := io.Copy(out, &b); err != nil {
		return fail(1, "cannot write spend investigation")
	}
	return 0
}

func renderSpend(out io.Writer, r spend.Report) {
	if r.Incomplete {
		fmt.Fprintln(out, "Comparison incomplete: some selected call types are not analyzed.")
		for _, c := range r.CallTypes {
			if c.Analyzed || c.Before.Requests == 0 && c.After.Requests == 0 {
				continue
			}
			fmt.Fprintf(out, "  %s: %s -> %s requests (unsupported).\n", c.Name, spendNumber(c.Before.Requests), spendNumber(c.After.Requests))
			fmt.Fprintf(out, "    Prompt tokens (unanalyzed): %s -> %s; completion tokens (unanalyzed): %s -> %s; total tokens (unanalyzed): %s -> %s.\n", spendCoverageNumber(c.Before.PromptTokens, c.Before.PromptTokensOverflow), spendCoverageNumber(c.After.PromptTokens, c.After.PromptTokensOverflow), spendCoverageNumber(c.Before.CompletionTokens, c.Before.CompletionTokensOverflow), spendCoverageNumber(c.After.CompletionTokens, c.After.CompletionTokensOverflow), spendCoverageNumber(c.Before.TotalTokens, c.Before.TotalTokensOverflow), spendCoverageNumber(c.After.TotalTokens, c.After.TotalTokensOverflow))
			if c.Before.MissingUsage != 0 || c.After.MissingUsage != 0 {
				fmt.Fprintf(out, "    Missing usage fields: %s -> %s records.\n", spendNumber(c.Before.MissingUsage), spendNumber(c.After.MissingUsage))
			}
			if c.Before.InvalidUsage != 0 || c.After.InvalidUsage != 0 {
				fmt.Fprintf(out, "    Invalid usage fields: %s -> %s records.\n", spendNumber(c.Before.InvalidUsage), spendNumber(c.After.InvalidUsage))
			}
		}
		fmt.Fprintf(out, "Analyzed calls only: %s -> %s recorded tokens; %s -> %s logged model requests.\n", spendNumber(r.Before.Tokens), spendNumber(r.After.Tokens), spendNumber(r.Before.Requests), spendNumber(r.After.Requests))
	} else {
		fmt.Fprintln(out, spendHeadline(r))
		renderSpendModelContribution(out, r)
		fmt.Fprintf(out, "Logged model requests: %s -> %s.\n", spendNumber(r.Before.Requests), spendNumber(r.After.Requests))
	}
	for _, entry := range []struct {
		name string
		w    spend.Window
	}{{"Before", r.Before}, {"After", r.After}} {
		fmt.Fprintf(out, "%s: %s to %s (end exclusive).\n", entry.name, entry.w.Period.Start.Format("Mon 2006-01-02 15:04:05.999999999 UTC"), entry.w.Period.End.Format("Mon 2006-01-02 15:04:05.999999999 UTC"))
	}
	fmt.Fprint(out, "\nWhat changed?")
	if r.Incomplete {
		fmt.Fprint(out, " (analyzed calls only)")
	}
	fmt.Fprintln(out)
	if r.Volume != nil && !r.Incomplete {
		renderSpendVolume(out, r.Volume)
	}
	for _, m := range r.Models {
		label := m.Item
		if m.Hash != "" {
			label += " (" + m.Hash + ")"
		}
		fmt.Fprintf(out, "  %s: %s -> %s recorded tokens; %s -> %s requests.\n", label, spendNumber(m.Before.Tokens), spendNumber(m.After.Tokens), spendNumber(m.Before.Requests), spendNumber(m.After.Requests))
		if m.Volume != nil {
			renderSpendVolume(out, m.Volume)
		}
	}
	if !r.Incomplete && r.After.Tokens > r.Before.Tokens {
		fmt.Fprintln(out, "\nWho drove the increase?")
	} else {
		fmt.Fprint(out, "\nWho drove the change?")
		if r.Incomplete {
			fmt.Fprint(out, " (analyzed calls only)")
		}
		fmt.Fprintln(out)
	}
	for _, m := range r.Rankings.Movers {
		label := m.Item
		if m.Hash != "" {
			label += " (" + m.Hash + ")"
		}
		fmt.Fprintf(out, "  %s: %s -> %s tokens; change %s.\n", label, spendInterval(m.Before), spendInterval(m.After), signedInterval(m.Delta))
	}
	if c := r.Increase; c != nil && !r.Incomplete {
		kind := r.GroupBy
		verb := "accounts"
		if c.Count != 1 {
			kind += "s"
			verb = "account"
		}
		share := spendPercent(100 * c.Share.Lower)
		if c.Delta.Lower != c.Delta.Upper {
			// Net-change shares can exceed 100%; shareText caps activity shares.
			lower, upper := c.Share.Lower*10000, c.Share.Upper*10000
			if c.Share.Lower == c.Share.Upper {
				lower, upper = math.Nextafter(lower, math.Inf(-1)), math.Nextafter(upper, math.Inf(1))
			}
			share = fmt.Sprintf("[%s, %s]", spendPercent(math.Floor(lower)/100), spendPercent(math.Ceil(upper)/100))
		}
		fmt.Fprintf(out, "  %s leading tracked %s %s for %s of the net recorded increase.\n", spendNumber(c.Count), kind, verb, share)
	}
	if len(r.Rankings.Movers) == 0 {
		fmt.Fprintln(out, "  No tracked contributors with attributable recorded tokens.")
	}
	fmt.Fprintf(out, "  Attributed tokens: %s -> %s.\n", spendNumber(r.Before.AttributedTokens), spendNumber(r.After.AttributedTokens))
	fmt.Fprintln(out, "\nHow complete is the export?")
	if r.Incomplete {
		fmt.Fprintln(out, "  Usage checks below cover analyzed calls only; unsupported token fields above remain unanalyzed.")
	}
	for _, c := range r.CallTypes {
		if !c.Analyzed {
			continue
		}
		for _, v := range []struct {
			name                          string
			before, after                 uint64
			beforeOverflow, afterOverflow bool
		}{
			{"prompt_tokens", c.Before.PromptTokens, c.After.PromptTokens, c.Before.PromptTokensOverflow, c.After.PromptTokensOverflow},
			{"completion_tokens", c.Before.CompletionTokens, c.After.CompletionTokens, c.Before.CompletionTokensOverflow, c.After.CompletionTokensOverflow},
			{"total_tokens", c.Before.TotalTokens, c.After.TotalTokens, c.Before.TotalTokensOverflow, c.After.TotalTokensOverflow},
		} {
			if v.beforeOverflow || v.afterOverflow {
				fmt.Fprintf(out, "  %s raw %s column sum: %s -> %s.\n", c.Name, v.name, spendCoverageNumber(v.before, v.beforeOverflow), spendCoverageNumber(v.after, v.afterOverflow))
			}
		}
	}
	a, b := r.Before.Quality, r.After.Quality
	for _, v := range []struct {
		label string
		a, b  uint64
	}{
		{"Records needing usage review (categories may overlap)", a.Unclear, b.Unclear},
		{"Zero-only records, origin unknown", a.ZeroUnknown, b.ZeroUnknown},
		{"Missing token fields", a.Missing, b.Missing},
		{"Invalid usage or cache subsets", a.Invalid, b.Invalid},
		{"Component/total mismatch", a.TotalMismatch, b.TotalMismatch},
		{"Total field absent", a.TotalMissing, b.TotalMissing},
		{"Failed records (overlap usage categories)", a.Failed, b.Failed},
		{"Unknown status", a.UnknownStatus, b.UnknownStatus},
		{"Group identities missing", a.MissingIdentity, b.MissingIdentity},
	} {
		if v.a != 0 || v.b != 0 {
			fmt.Fprintf(out, "  %s: %s -> %s.\n", v.label, spendNumber(v.a), spendNumber(v.b))
		}
	}
	fmt.Fprintln(out, "  No other usage problems found.")
	fmt.Fprintln(out, "  Zero-filled: "+r.ZeroFilled+". Provider origin: "+r.ProviderOrigin+".")
	if r.Before.RecordedSpend != "" && r.Before.RecordedSpend != "0" || r.After.RecordedSpend != "" && r.After.RecordedSpend != "0" {
		fmt.Fprintf(out, "  Recorded spend (USD, analyzed calls): %s -> %s.\n", spendNumber(r.Before.RecordedSpend), spendNumber(r.After.RecordedSpend))
	}
	if a.SpendZeroUnknown != 0 || b.SpendZeroUnknown != 0 {
		fmt.Fprintf(out, "  Zero-cost origin unknown: %s -> %s.\n", spendNumber(a.SpendZeroUnknown), spendNumber(b.SpendZeroUnknown))
	}
	if a.SpendMissing+a.SpendInvalid != 0 || b.SpendMissing+b.SpendInvalid != 0 {
		fmt.Fprintf(out, "  Missing/invalid cost: %s -> %s.\n", spendNumber(a.SpendMissing+a.SpendInvalid), spendNumber(b.SpendMissing+b.SpendInvalid))
	}
	fmt.Fprintf(out, "  Rows read: %s; unsupported rows: %s; outside periods: %s.\n", spendNumber(r.Rows), spendNumber(r.ExcludedRows), spendNumber(r.OutsidePeriods))
	if r.Incomplete {
		fmt.Fprintln(out, "  Whole-period per-request split unavailable while selected call types remain unanalyzed.")
	} else if r.Volume == nil {
		fmt.Fprintln(out, "  Whole-period per-request split needs clearer usage; recorded totals remain shown.")
	}
	for _, m := range r.Models {
		if m.Volume != nil {
			continue
		}
		if m.Before.Requests == 0 || m.After.Requests == 0 {
			fmt.Fprintf(out, "  %s per-request split needs analyzed requests in both periods.\n", m.Item)
		} else {
			fmt.Fprintf(out, "  %s per-request split needs clearer usage on %s before / %s after records.\n", m.Item, spendNumber(m.Before.Quality.Unclear), spendNumber(m.After.Quality.Unclear))
		}
	}
	if r.Incomplete {
		fmt.Fprintln(out, "\nNext: review unsupported call types in your local LiteLLM logs; analyze those rows before drawing a whole-export token-change conclusion.")
	} else if a.Unclear != 0 || b.Unclear != 0 || a.MissingIdentity != 0 || b.MissingIdentity != 0 {
		fmt.Fprintln(out, "\nNext: review the leading contributors and flagged records in your local LiteLLM logs.")
	} else {
		fmt.Fprintln(out, "\nNext: review the leading contributors in your local LiteLLM logs.")
	}
}

func renderSpendVolume(out io.Writer, v *compare.VolumeChange) {
	fmt.Fprintf(out, "    %s -> %s recorded tokens per request; %s from request count, %s from request size.\n", spendNumber(fmt.Sprintf("%.2f", v.BeforeAverage)), spendNumber(fmt.Sprintf("%.2f", v.AfterAverage)), spendNumber(fmt.Sprintf("%+.0f", v.RequestContribution)), spendNumber(fmt.Sprintf("%+.0f", v.TokensPerRequestContribution)))
}

func signedInterval(v compare.Interval) string {
	if v.Lower == v.Upper {
		return spendNumber(fmt.Sprintf("%+d", v.Lower))
	}
	return fmt.Sprintf("[%s, %s]", spendNumber(fmt.Sprintf("%+d", v.Lower)), spendNumber(fmt.Sprintf("%+d", v.Upper)))
}

func spendInterval(v compare.Interval) string {
	if v.Lower == v.Upper {
		return spendNumber(intervalText(v))
	}
	return fmt.Sprintf("[%s, %s]", spendNumber(v.Lower), spendNumber(v.Upper))
}

func spendCoverageNumber(value uint64, overflow bool) string {
	if overflow {
		return "exceeds report range"
	}
	return spendNumber(value)
}

func spendNumber(value any) string {
	text := fmt.Sprint(value)
	end := strings.IndexByte(text, '.')
	if end < 0 {
		end = len(text)
	}
	start := 0
	if len(text) > 0 && (text[0] == '+' || text[0] == '-') {
		start = 1
	}
	for i := end - 3; i > start; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}

func spendPercent(value float64) string {
	if value > 0 && value < 0.005 {
		return "<0.01%"
	}
	return spendNumber(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")) + "%"
}

func spendHeadline(r spend.Report) string {
	a, b := r.Before.Tokens, r.After.Tokens
	change := "were unchanged"
	switch {
	case b > a && b-a == a:
		change = "doubled"
	case b > a && a == 0:
		change = "rose from zero"
	case b > a:
		change = "rose by " + spendPercent(100*float64(b-a)/float64(a))
	case b < a:
		change = "fell by " + spendPercent(100*float64(a-b)/float64(a))
	}
	return fmt.Sprintf("Recorded tokens %s (%s -> %s).", change, spendNumber(a), spendNumber(b))
}

func renderSpendModelContribution(out io.Writer, r spend.Report) {
	net := int64(r.After.Tokens) - int64(r.Before.Tokens)
	if net == 0 {
		return
	}
	var leading *spend.Model
	var delta int64
	for i := range r.Models {
		m := &r.Models[i]
		d := int64(m.After.Tokens) - int64(m.Before.Tokens)
		if net > 0 && d > delta || net < 0 && d < delta {
			leading, delta = m, d
		}
	}
	if leading == nil {
		return
	}
	direction := "increase"
	if net < 0 {
		direction = "decrease"
	}
	fmt.Fprintf(out, "%s accounts for %s of the net recorded %s (%s tokens).\n", leading.Item, spendPercent(100*float64(delta)/float64(net)), direction, signedInterval(compare.Interval{Lower: delta, Upper: delta}))
}
