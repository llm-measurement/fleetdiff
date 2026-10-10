// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

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
	fmt.Fprintf(out, "Recorded tokens: %d -> %d (%+d); logged model requests: %d -> %d.\n", r.Before.Tokens, r.After.Tokens, int64(r.After.Tokens)-int64(r.Before.Tokens), r.Before.Requests, r.After.Requests)
	for _, entry := range []struct {
		name string
		w    spend.Window
	}{{"Before", r.Before}, {"After", r.After}} {
		fmt.Fprintf(out, "%s: %s to %s (end exclusive).\n", entry.name, entry.w.Period.Start.Format("Mon 2006-01-02 15:04:05.999999999 UTC"), entry.w.Period.End.Format("Mon 2006-01-02 15:04:05.999999999 UTC"))
	}
	fmt.Fprintln(out, "\nWhat changed?")
	if r.Volume != nil {
		renderSpendVolume(out, r.Volume)
	} else {
		fmt.Fprintln(out, "  Whole-period per-request split needs clearer usage; recorded totals remain shown.")
	}
	for _, m := range r.Models {
		label := m.Item
		if m.Hash != "" {
			label += " (" + m.Hash + ")"
		}
		fmt.Fprintf(out, "  %s: %d -> %d recorded tokens; %d -> %d requests.\n", label, m.Before.Tokens, m.After.Tokens, m.Before.Requests, m.After.Requests)
		if m.Volume != nil {
			renderSpendVolume(out, m.Volume)
		} else {
			fmt.Fprintf(out, "    Per-request split needs clearer usage on %d before / %d after records, or requests in both periods.\n", m.Before.Quality.Unclear, m.After.Quality.Unclear)
		}
	}
	fmt.Fprintln(out, "\nWho drove the increase?")
	for _, m := range r.Rankings.Movers {
		label := m.Item
		if m.Hash != "" {
			label += " (" + m.Hash + ")"
		}
		fmt.Fprintf(out, "  %s: %s -> %s tokens; change %s.\n", label, intervalText(m.Before), intervalText(m.After), signedInterval(m.Delta))
	}
	if c := r.Increase; c != nil {
		kind := r.GroupBy
		verb := "accounts"
		if c.Count != 1 {
			kind += "s"
			verb = "account"
		}
		share := fmt.Sprintf("%.2f%%", 100*c.Share.Lower)
		if c.Delta.Lower != c.Delta.Upper {
			share = fmt.Sprintf("[%.2f%%, %.2f%%]", 100*c.Share.Lower, 100*c.Share.Upper)
		}
		fmt.Fprintf(out, "  %d leading tracked %s %s for %s of the net recorded increase.\n", c.Count, kind, verb, share)
	} else {
		fmt.Fprintln(out, "  Concentration of increase needs a positive recorded change and an increasing tracked contributor.")
	}
	fmt.Fprintf(out, "  Group identities missing: %d -> %d requests; attributed tokens: %d -> %d.\n", r.Before.Quality.MissingIdentity, r.After.Quality.MissingIdentity, r.Before.AttributedTokens, r.After.AttributedTokens)
	fmt.Fprintln(out, "\nHow complete is the export?")
	a, b := r.Before.Quality, r.After.Quality
	for _, v := range []struct {
		label string
		a, b  uint64
	}{
		{"Zero-only records, origin unknown", a.ZeroUnknown, b.ZeroUnknown},
		{"Missing token fields", a.Missing, b.Missing},
		{"Invalid usage or cache subsets", a.Invalid, b.Invalid},
		{"Component/total mismatch", a.TotalMismatch, b.TotalMismatch},
		{"Total field absent", a.TotalMissing, b.TotalMissing},
		{"Failed records (overlap usage categories)", a.Failed, b.Failed},
		{"Unknown status", a.UnknownStatus, b.UnknownStatus},
	} {
		fmt.Fprintf(out, "  %s: %d -> %d.\n", v.label, v.a, v.b)
	}
	fmt.Fprintln(out, "  Zero-filled: "+r.ZeroFilled+". Provider origin: "+r.ProviderOrigin+".")
	fmt.Fprintf(out, "  Recorded spend (USD): %s -> %s. Zero-cost origin unknown: %d -> %d; missing/invalid cost: %d -> %d.\n", r.Before.RecordedSpend, r.After.RecordedSpend, a.SpendZeroUnknown, b.SpendZeroUnknown, a.SpendMissing+a.SpendInvalid, b.SpendMissing+b.SpendInvalid)
	fmt.Fprintf(out, "  Rows read: %d; other call types: %d; outside periods: %d.\n", r.Rows, r.ExcludedRows, r.OutsidePeriods)
	fmt.Fprintln(out, "\nNext: review the leading contributors and zero-only or failed records in your local LiteLLM logs.")
	fmt.Fprintln(out, "\nNotes")
	for _, n := range r.Notes {
		fmt.Fprintln(out, "  "+n)
	}
}

func renderSpendVolume(out io.Writer, v *compare.VolumeChange) {
	fmt.Fprintf(out, "    %.2f -> %.2f recorded tokens per request; %+.0f from request count, %+.0f from request size.\n", v.BeforeAverage, v.AfterAverage, v.RequestContribution, v.TokensPerRequestContribution)
}

func signedInterval(v compare.Interval) string {
	if v.Lower == v.Upper {
		return fmt.Sprintf("%+d", v.Lower)
	}
	return fmt.Sprintf("[%+d, %+d]", v.Lower, v.Upper)
}
