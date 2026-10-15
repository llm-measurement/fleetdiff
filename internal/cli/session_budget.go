// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func runSessionBudget(path, period, budget, secret, format, daily string, top int, hashes bool, window time.Duration, out, errout io.Writer) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
	if format != "text" && format != "json" || top < 1 || top > 100 {
		return fail(2, "use text or json output and top between 1 and 100")
	}
	r, err := spend.ReviewSessions(path, spend.SessionOptions{Period: period, BudgetUSD: budget, SecretEnv: secret, CounterWindow: window, Top: top, ShowHashes: hashes, DailySpendPath: daily})
	if err != nil {
		return fail(1, err.Error())
	}
	var b bytes.Buffer
	if format == "json" {
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			return fail(1, "cannot encode session review")
		}
	} else {
		renderSessionBudget(&b, r)
	}
	if _, err := io.Copy(out, &b); err != nil {
		return fail(1, "cannot write session review")
	}
	return 0
}

func sessionUSD(s string) string {
	whole, fraction, _ := strings.Cut(s, ".")
	if len(fraction) < 2 {
		fraction += strings.Repeat("0", 2-len(fraction))
	}
	return whole + "." + fraction
}

func renderSessionBudget(out io.Writer, r spend.SessionReport) {
	renderRowCoverage(out, r.RowCoverage)
	if r.UnsupportedRows != 0 {
		fmt.Fprintf(out, "Session review is incomplete: %s selected records have unsupported call types.\n", spendNumber(r.UnsupportedRows))
	}
	if r.BudgetUSD != nil {
		fmt.Fprintf(out, "%s of %s sessions with complete recorded spend exceed the supplied USD %s cap.\n", spendNumber(r.Exceeding), spendNumber(r.Spend.N), sessionUSD(*r.BudgetUSD))
	} else {
		fmt.Fprintf(out, "%s sessions with model calls; %s have complete recorded spend.\n", spendNumber(r.Sessions), spendNumber(r.Spend.N))
	}
	fmt.Fprintf(out, "Grouped by session ID alone (LiteLLM v1.104.0 counter unit). %s to %s (end exclusive).\n", r.Period.Start.Format("Mon 2006-01-02 15:04:05.999999999 UTC"), r.Period.End.Format("Mon 2006-01-02 15:04:05.999999999 UTC"))
	if r.ObservedSpan {
		fmt.Fprintln(out, "Period: the file's observed span of request starts; no complete UTC day is present. The end is one nanosecond after the last start.")
	} else if r.AutomaticPeriod {
		fmt.Fprintln(out, "Period: complete UTC days inside the file's observed time span.")
	}
	fmt.Fprintln(out, "\nObserved session totals")
	renderSessionDistribution(out, "Recorded USD", r.Spend, true)
	renderSessionDistribution(out, "Logged model calls", r.Calls, false)
	if r.Censored != 0 {
		fmt.Fprintf(out, "Sessions with recorded budget or iteration rejections: %s. Included in observed distributions: spend n=%s, calls n=%s.\n", spendNumber(r.Censored), spendNumber(r.CensoredSpendN), spendNumber(r.CensoredCallsN))
	}
	if r.RejectionOnlySessions != 0 {
		fmt.Fprintf(out, "Rejection-only sessions: %s, separate from model-call distributions.\n", spendNumber(r.RejectionOnlySessions))
	}
	if r.UnanalyzedOnlySessions != 0 {
		fmt.Fprintf(out, "Sessions with unsupported records and no analyzed model calls: %s.\n", spendNumber(r.UnanalyzedOnlySessions))
	}
	if r.Clipped != 0 {
		fmt.Fprintf(out, "Sessions clipped: %s, with the same session ID recorded outside the selected period.\n", spendNumber(r.Clipped))
	}
	if r.PossiblyClipped != 0 {
		fmt.Fprintf(out, "Sessions possibly clipped: %s, active within %g seconds of a period edge with no file rows beyond it.\n", spendNumber(r.PossiblyClipped), r.CounterWindowSeconds)
	}
	fmt.Fprintln(out, "\nLargest sessions by recorded spend")
	for _, s := range r.Largest {
		renderSessionRow(out, s)
	}
	if r.BudgetUSD != nil {
		fmt.Fprintln(out, "\nCap review")
		if r.NoRecordedRejection == nil {
			fmt.Fprintln(out, "Rejection coverage is unknown for some selected rows; absence of a rejection cannot be checked across all sessions.")
		} else {
			fmt.Fprintf(out, "Above-cap sessions with no recorded session-budget rejection: %s.\n", spendNumber(*r.NoRecordedRejection))
		}
		if r.PartialExceeding != 0 {
			fmt.Fprintf(out, "%s incomplete spend subtotals already exceed the cap (listed separately from complete totals).\n", spendNumber(r.PartialExceeding))
		}
		for _, s := range r.CapReview {
			renderSessionRow(out, s)
			if !s.RejectionCoverage {
				fmt.Fprintln(out, "    Session-budget rejection coverage unknown.")
			} else {
				fmt.Fprintf(out, "    Recorded session-budget rejections: %d.\n", s.Rejections["session_budget"])
			}
		}
		if len(r.CapReview) != 0 {
			fmt.Fprintln(out, "Review last-call or parallel-call overshoot, the counter window, agent budget configuration and failure logging. Recorded totals alone do not establish enforcement behavior.")
		}
	}
	if r.LongSpans != 0 {
		fmt.Fprintf(out, "\nSessions spanning more than %g seconds between first and last positive-spend request starts: %s.\n", r.CounterWindowSeconds, spendNumber(r.LongSpans))
		for _, s := range r.LongSpanReview {
			fmt.Fprintf(out, "  %s: %g seconds.\n", s.Alias, *s.SpendSpanSeconds)
		}
		fmt.Fprintln(out, "This is a recorded duration, not evidence that a counter reset.")
	}
	if r.SharedCounters != 0 {
		fmt.Fprintf(out, "\nSession IDs observed under multiple keys, teams or agents: %s. Each addresses the same counter key; shared storage and agent budget configuration determine actual tracking.\n", spendNumber(r.SharedCounters))
		for _, s := range r.SharedCounterReview {
			fmt.Fprintf(out, "  %s: keys=%d, teams=%d, agents=%d.\n", s.Alias, s.Keys, s.Teams, s.Agents)
		}
	}
	fmt.Fprintln(out, "\nIdentity and usage coverage")
	fmt.Fprintf(out, "%s logged model calls in the selected period.\n", spendNumber(r.ModelCalls))
	fmt.Fprintf(out, "%s of %s identified sessions contain one model call.\n", spendNumber(r.Singletons), spendNumber(r.Sessions))
	if r.Singletons*2 > r.Sessions {
		fmt.Fprintln(out, "Most sessions have one call; session IDs may have been generated per request. Check identity propagation before treating these as task runs.")
	}
	if r.UnattributedRows != 0 {
		fmt.Fprintf(out, "%s selected rows have no session ID and remain unattributed.\n", spendNumber(r.UnattributedRows))
		fmt.Fprintf(out, "Unattributed analyzed calls: USD %s known subtotal (%s spend values, %s missing, %s invalid); outside session distributions and cap review.\n", sessionUSD(r.UnattributedSpend), spendNumber(r.UnattributedSpendRows), spendNumber(r.UnattributedMissingSpend), spendNumber(r.UnattributedInvalidSpend))
	}
	fmt.Fprintf(out, "Rejection classification: %s of %s selected rows include the derived column; failure logging completeness is unknown.\n", spendNumber(r.ClassifiedRows), spendNumber(r.SelectedRows))
	if r.UnclassifiedFailures != 0 {
		fmt.Fprintf(out, "Unclassified failure rows: %s.\n", spendNumber(r.UnclassifiedFailures))
	}
	for _, kind := range []string{"session_budget", "session_iterations", "other_budget"} {
		if n := r.Rejections[kind]; n != 0 {
			fmt.Fprintf(out, "  %s recorded rejections: %s.\n", kind, spendNumber(n))
		}
	}
	for _, x := range []struct {
		name string
		n    uint64
	}{{"Missing token fields", r.Quality.Missing}, {"Invalid usage", r.Quality.Invalid}, {"Component/total mismatches", r.Quality.TotalMismatch}, {"Missing total token field", r.Quality.TotalMissing}, {"Unknown model-call status", r.Quality.UnknownStatus}, {"Zero-only usage, origin unknown", r.Quality.ZeroUnknown}, {"Failed model-call records", r.Quality.Failed}, {"Missing recorded spend", r.Quality.SpendMissing}, {"Invalid recorded spend", r.Quality.SpendInvalid}, {"Zero recorded spend, origin unknown", r.Quality.SpendZeroUnknown}} {
		if x.n != 0 {
			fmt.Fprintf(out, "%s: %s.\n", x.name, spendNumber(x.n))
		}
	}
	if r.Quality.Unclear != 0 {
		fmt.Fprintf(out, "Records with unclear usage, status or timing: %s.\n", spendNumber(r.Quality.Unclear))
	}
}

func renderSessionDistribution(out io.Writer, name string, d spend.Distribution, usd bool) {
	if d.N == 0 {
		fmt.Fprintf(out, "%s: n=0.\n", name)
		return
	}
	values := []string{*d.P50, *d.P95, *d.Max}
	if usd {
		for i := range values {
			values[i] = sessionUSD(values[i])
		}
	}
	fmt.Fprintf(out, "%s: p50 %s; p95 %s; max %s (n=%s, exact nearest-rank).\n", name, values[0], values[1], values[2], spendNumber(d.N))
}

func renderSessionRow(out io.Writer, s spend.Session) {
	fmt.Fprintf(out, "  %s: USD %s; calls=%s", s.Alias, sessionUSD(s.RecordedSpend), spendNumber(s.Calls))
	if !s.SpendComplete {
		fmt.Fprint(out, "; incomplete spend subtotal")
	}
	if s.Censored {
		fmt.Fprint(out, "; recorded budget/iteration rejection")
	}
	if s.Clipped {
		fmt.Fprint(out, "; clipped")
	} else if s.PossiblyClipped {
		fmt.Fprint(out, "; possibly clipped")
	}
	if s.Hash != "" {
		fmt.Fprintf(out, "; hash %s", s.Hash)
	}
	fmt.Fprintln(out, ".")
}
