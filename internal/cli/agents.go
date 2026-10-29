// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"
	"math/big"
	"strconv"
	"text/tabwriter"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func agentUSD(v *string) string {
	if v == nil {
		return "unavailable"
	}
	return *v
}

func agentShare(v *string) string {
	if v == nil {
		return "unavailable"
	}
	n, ok := new(big.Rat).SetString(*v)
	if !ok {
		return "unavailable"
	}
	return new(big.Rat).Mul(n, big.NewRat(100, 1)).FloatString(2) + "%"
}

func agentCostCoverage(c spend.AgentCostCoverage) string {
	return fmt.Sprintf("%d/%d/%d/%d", c.Usable, c.Missing, c.Invalid, c.ZeroUnknown)
}

func renderAgents(out io.Writer, a *spend.AgentReport) {
	fmt.Fprintf(out, "\nAgent attribution (%s); ranking: %s.\n", a.Source, a.RankBy)
	if a.LabelsIncluded {
		fmt.Fprintln(out, "  Sensitive agent labels included; review before sharing.")
	}
	for _, side := range []struct {
		name string
		w    spend.AgentWindow
	}{{"Before", a.Before}, {"After", a.After}} {
		c := side.w.Coverage
		fmt.Fprintf(out, "  %s: %d known, %d unknown, %d ambiguous rows; %d column-absent, %d empty/null, %d without a matching tag.\n", side.name, c.KnownRows, c.UnknownRows, c.AmbiguousRows, c.ColumnAbsentRows, c.EmptyOrNullRows, c.NoMatchingTagRows)
		fmt.Fprintf(out, "    Repeated agent tags: %d in %d rows; empty suffixes: %d in %d rows.\n", c.RepeatedAgentTags, c.RepeatedAgentTagRows, c.EmptySuffixTags, c.EmptySuffixRows)
		usd := func(w spend.Window) string {
			if w.Requests > 0 && w.Quality.SpendMissing+w.Quality.SpendInvalid == w.Requests {
				return "unavailable"
			}
			return w.RecordedSpend
		}
		fmt.Fprintf(out, "    Unknown: %d tokens / %s recorded USD; ambiguous: %d tokens / %s recorded USD.\n", side.w.Unknown.Tokens, usd(side.w.Unknown), side.w.Ambiguous.Tokens, usd(side.w.Ambiguous))
	}
	fmt.Fprintln(out, "  Shares use all usable recorded USD in each period, including unknown, ambiguous and undisplayed agents. Missing/invalid costs remain unquantified; recorded USD is not an invoice.")
	fmt.Fprintln(out, "  Cost coverage columns: usable/missing/invalid/zero-origin-unknown rows.")
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "  Agent\tTokens before\tTokens after\tUSD before\tUSD after\tUSD change\tCost coverage before\tCost coverage after\tShare before\tShare after")
	for _, c := range a.Contributors {
		label := c.Item
		if c.Label != "" {
			label += " " + strconv.QuoteToASCII(c.Label)
		}
		if c.Hash != "" {
			label += " (" + c.Hash + ")"
		}
		fmt.Fprintf(w, "  %s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", label, c.Before.Tokens, c.After.Tokens, agentUSD(c.Before.RecordedUSD), agentUSD(c.After.RecordedUSD), agentUSD(c.Delta), agentCostCoverage(c.Before.CostCoverage), agentCostCoverage(c.After.CostCoverage), agentShare(c.Before.Share), agentShare(c.After.Share))
	}
	_ = w.Flush()
	fmt.Fprintf(out, "  Known agents not displayed: %d -> %d rows; %d -> %d tokens; %s -> %s recorded USD.\n", a.UndisplayedBefore.Requests, a.UndisplayedAfter.Requests, a.UndisplayedBefore.Tokens, a.UndisplayedAfter.Tokens, agentUSD(a.UndisplayedBefore.RecordedUSD), agentUSD(a.UndisplayedAfter.RecordedUSD))
}
