// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"
	"math/big"
	"text/tabwriter"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/compare"
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

func agentLabel(c spend.AgentContributor) string {
	label := routingLabel(spend.RoutingIdentity{Item: c.Item, Label: c.Label, Hash: c.Hash})
	if c.Label != "" && c.ProjectAlias != "" {
		label += " (" + c.ProjectAlias + ")"
	}
	return label
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
		if a.Source == "agent_id" {
			fmt.Fprintf(out, "  %s: %d known, %d unknown rows; %d column-absent, %d empty/null.\n", side.name, c.KnownRows, c.UnknownRows, c.ColumnAbsentRows, c.EmptyOrNullRows)
		} else {
			fmt.Fprintf(out, "  %s: %d known, %d unknown, %d ambiguous rows; %d column-absent, %d empty/null, %d without a matching tag.\n", side.name, c.KnownRows, c.UnknownRows, c.AmbiguousRows, c.ColumnAbsentRows, c.EmptyOrNullRows, c.NoMatchingTagRows)
			fmt.Fprintf(out, "    Repeated agent tags: %d in %d rows; empty suffixes: %d in %d rows.\n", c.RepeatedAgentTags, c.RepeatedAgentTagRows, c.EmptySuffixTags, c.EmptySuffixRows)
		}
		if c.NonStringTagRows > 0 {
			fmt.Fprintf(out, "    Non-string tag elements: %d rows counted as unknown identity.\n", c.NonStringTagRows)
		}
		usd := func(w spend.Window) string {
			if w.Requests > 0 && w.Quality.SpendMissing+w.Quality.SpendInvalid == w.Requests {
				return "unavailable"
			}
			return w.RecordedSpend
		}
		fmt.Fprintf(out, "    Unknown: %d tokens / %s recorded USD", side.w.Unknown.Tokens, usd(side.w.Unknown))
		if a.Source != "agent_id" {
			fmt.Fprintf(out, "; ambiguous: %d tokens / %s recorded USD", side.w.Ambiguous.Tokens, usd(side.w.Ambiguous))
		}
		fmt.Fprintln(out, ".")
	}
	fmt.Fprintln(out, "  Shares use all usable recorded USD in each period, including unknown, ambiguous and undisplayed agents. Missing/invalid costs remain unquantified; recorded USD is not an invoice.")
	showCosts := false
	for _, contributors := range [][]spend.AgentContributor{a.Contributors, a.UnavailableCosts} {
		for _, c := range contributors {
			for _, v := range []spend.AgentValues{c.Before, c.After} {
				showCosts = showCosts || v.CostCoverage.Missing+v.CostCoverage.Invalid+v.CostCoverage.ZeroUnknown != 0
			}
		}
	}
	if showCosts {
		fmt.Fprintln(out, "  Cost coverage columns show before -> after row counts.")
	} else {
		fmt.Fprintln(out, "  All displayed agents have usable recorded USD in both periods.")
	}
	w := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprint(w, "  Agent\tTokens before\tTokens after\tUSD before\tUSD after\tUSD change\tShare before\tShare after")
	if showCosts {
		fmt.Fprint(w, "\tUsable rows\tMissing rows\tInvalid rows\tZero-origin-unknown rows")
	}
	fmt.Fprintln(w)
	writeContributor := func(c spend.AgentContributor) {
		fmt.Fprintf(w, "  %s\t%d\t%d\t%s\t%s\t%s\t%s\t%s", agentLabel(c), c.Before.Tokens, c.After.Tokens, agentUSD(c.Before.RecordedUSD), agentUSD(c.After.RecordedUSD), agentUSD(c.Delta), agentShare(c.Before.Share), agentShare(c.After.Share))
		if showCosts {
			b, a := c.Before.CostCoverage, c.After.CostCoverage
			fmt.Fprintf(w, "\t%d -> %d\t%d -> %d\t%d -> %d\t%d -> %d", b.Usable, a.Usable, b.Missing, a.Missing, b.Invalid, a.Invalid, b.ZeroUnknown, a.ZeroUnknown)
		}
		fmt.Fprintln(w)
	}
	for _, c := range a.Contributors {
		writeContributor(c)
	}
	_ = w.Flush()
	if len(a.UnavailableCosts) > 0 {
		fmt.Fprintln(out, "  USD change unavailable (not ranked; shown regardless of --top):")
		for _, c := range a.UnavailableCosts {
			writeContributor(c)
		}
		_ = w.Flush()
		for _, c := range a.UnavailableCosts {
			delta := int64(c.After.Tokens) - int64(c.Before.Tokens)
			fmt.Fprintf(out, "    %s: token change %s; recorded USD change unavailable.\n", c.Item, signedInterval(compare.Interval{Lower: delta, Upper: delta}))
		}
	}
	buckets := []struct {
		name          string
		before, after spend.Window
	}{{"Unknown", a.Before.Unknown, a.After.Unknown}, {"Ambiguous", a.Before.Ambiguous, a.After.Ambiguous}}
	if a.Source == "agent_id" {
		buckets = buckets[:1]
	}
	for _, bucket := range buckets {
		cost := func(v spend.Window) *big.Rat {
			if v.Requests > 0 && v.Quality.SpendMissing+v.Quality.SpendInvalid == v.Requests {
				return nil
			}
			n, _ := new(big.Rat).SetString(v.RecordedSpend)
			return n
		}
		x, y := cost(bucket.before), cost(bucket.after)
		delta := "unavailable"
		if x != nil && y != nil {
			delta = accounting.CostText(new(big.Rat).Sub(y, x))
		}
		tokens := int64(bucket.after.Tokens) - int64(bucket.before.Tokens)
		fmt.Fprintf(out, "  %s (not ranked): %d -> %d rows; token change %s; recorded USD change %s.\n", bucket.name, bucket.before.Requests, bucket.after.Requests, signedInterval(compare.Interval{Lower: tokens, Upper: tokens}), delta)
	}
	fmt.Fprintf(out, "  Known agents not displayed: %d -> %d rows; %d -> %d tokens; %s -> %s recorded USD.\n", a.UndisplayedBefore.Requests, a.UndisplayedAfter.Requests, a.UndisplayedBefore.Tokens, a.UndisplayedAfter.Tokens, agentUSD(a.UndisplayedBefore.RecordedUSD), agentUSD(a.UndisplayedAfter.RecordedUSD))
}
