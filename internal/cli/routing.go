// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func renderRouting(out io.Writer, r spend.Report) {
	if c := r.ScopeCoverage; c != nil {
		fmt.Fprintf(out, "Scope coverage (%s; all in-period rows before filtering)\n", c.Kind)
		fmt.Fprintf(out, "  Matched: %s -> %s; known nonmatching: %s -> %s; unknown identity: %s -> %s rows.\n",
			spendNumber(c.Before.MatchedRows), spendNumber(c.After.MatchedRows), spendNumber(c.Before.NonmatchingKnownRows), spendNumber(c.After.NonmatchingKnownRows), spendNumber(c.Before.UnknownIdentityRows), spendNumber(c.After.UnknownIdentityRows))
		if r.RoutingAudit == nil {
			renderRoutingCoverage(out, c.FieldCoverageByCallType)
		}
	}
	a := r.RoutingAudit
	if a == nil {
		if r.ScopeCoverage != nil {
			fmt.Fprintln(out)
		}
		return
	}
	if r.ScopeCoverage != nil {
		fmt.Fprintln(out)
	}
	fmt.Fprintln(out, "Routing audit: which models served each route?")
	if a.ConfigurationLabelsIncluded {
		fmt.Fprintln(out, "  Configuration labels included (--show-labels); review before sharing.")
	}
	fmt.Fprintln(out, "  Basis: gateway-selected model with a recorded deployment ID; provider snapshot not confirmed.")
	fmt.Fprintln(out, "  Rows are spend-log records, not a reconstruction of physical attempts.")
	renderRoutingMeasures(out, "Model-request rows", a.ModelRows.Before, a.ModelRows.After)
	for _, c := range a.Cohorts {
		label := routingLabel(c.Route)
		if c.Agent != nil {
			label += "; " + routingLabel(*c.Agent)
		}
		fmt.Fprintf(out, "  %s\n", label)
		renderRoutingMeasures(out, "  Cohort", c.Before, c.After)
		for _, m := range c.Models {
			fmt.Fprintf(out, "    %s: requests %s -> %s; tokens %s -> %s; recorded USD %s -> %s.\n",
				routingLabel(m.RoutingIdentity), spendNumber(m.Before.Requests), spendNumber(m.After.Requests), spendNumber(m.Before.Tokens), spendNumber(m.After.Tokens), m.Before.RecordedSpend, m.After.RecordedSpend)
			fmt.Fprintf(out, "      Cohort shares: requests %s -> %s; tokens %s -> %s; known recorded spend %s -> %s.\n",
				routingShare(m.SharesBefore.Requests), routingShare(m.SharesAfter.Requests), routingShare(m.SharesBefore.Tokens), routingShare(m.SharesAfter.Tokens), routingShare(m.SharesBefore.Spend), routingShare(m.SharesAfter.Spend))
			renderRoutingQuality(out, m.Before, m.After)
		}
	}
	if c := a.DisplayRemainder; c.Cohorts != 0 || c.ModelCells != 0 {
		fmt.Fprintf(out, "  Not displayed: %d cohorts, %d model cells; %s -> %s requests. Shares retain the full cohort denominator.\n", c.Cohorts, c.ModelCells, spendNumber(c.Before.Requests), spendNumber(c.After.Requests))
	}
	renderRoutingRaw(out, "Classifier routing overhead", a.ClassifierOverhead)
	renderRoutingRaw(out, "Other unanalyzed rows", a.OtherRows)
	fmt.Fprintln(out, "  Attribution coverage by call type (before -> after):")
	renderRoutingCoverage(out, a.Coverage.ByCallType)
	fmt.Fprintln(out, "  Recorded fallback counters do not count physical attempts. Unknown identity and missing usage stay unknown; model identity does not establish finality.")
	fmt.Fprintln(out, "\nRecorded usage comparison")
}

func routingLabel(id spend.RoutingIdentity) string {
	label := id.Item
	if id.Label != "" {
		label += " " + strconv.QuoteToASCII(id.Label)
	}
	if id.Hash != "" {
		label += " (" + id.Hash + ")"
	}
	return label
}

func routingShare(v *string) string {
	if v == nil {
		return "unknown"
	}
	r, ok := new(big.Rat).SetString(*v)
	if !ok {
		return "unknown"
	}
	r.Mul(r, big.NewRat(100, 1))
	return strings.TrimRight(strings.TrimRight(r.FloatString(2), "0"), ".") + "%"
}

func renderRoutingMeasures(out io.Writer, label string, a, b spend.RoutingMeasure) {
	fmt.Fprintf(out, "  %s: %s -> %s requests; %s -> %s recorded tokens; recorded USD %s -> %s.\n",
		label, spendNumber(a.Requests), spendNumber(b.Requests), spendNumber(a.Tokens), spendNumber(b.Tokens), a.RecordedSpend, b.RecordedSpend)
	if a.UnknownModelRows != 0 || b.UnknownModelRows != 0 {
		fmt.Fprintf(out, "    Served identity unknown: %s -> %s rows (included in share denominators).\n", spendNumber(a.UnknownModelRows), spendNumber(b.UnknownModelRows))
	}
	renderRoutingQuality(out, a, b)
	fmt.Fprintf(out, "    Recorded fallback counters: positive on %s -> %s rows; zero on %s -> %s; unknown on %s -> %s.\n",
		spendNumber(a.Fallbacks.KnownPositiveRows), spendNumber(b.Fallbacks.KnownPositiveRows), spendNumber(a.Fallbacks.KnownZeroRows), spendNumber(b.Fallbacks.KnownZeroRows), spendNumber(a.Fallbacks.UnknownRows), spendNumber(b.Fallbacks.UnknownRows))
}

func renderRoutingQuality(out io.Writer, a, b spend.RoutingMeasure) {
	if a.Quality.SpendMissing+a.Quality.SpendInvalid+b.Quality.SpendMissing+b.Quality.SpendInvalid != 0 {
		fmt.Fprintf(out, "      Recorded spend is a known subtotal: missing on %s -> %s rows; invalid on %s -> %s.\n",
			spendNumber(a.Quality.SpendMissing), spendNumber(b.Quality.SpendMissing), spendNumber(a.Quality.SpendInvalid), spendNumber(b.Quality.SpendInvalid))
	}
	if a.Quality.SpendZeroUnknown+b.Quality.SpendZeroUnknown != 0 {
		fmt.Fprintf(out, "      Zero recorded spend, origin unknown: %s -> %s rows.\n", spendNumber(a.Quality.SpendZeroUnknown), spendNumber(b.Quality.SpendZeroUnknown))
	}
	if a.Quality.Missing+a.Quality.Invalid+b.Quality.Missing+b.Quality.Invalid != 0 {
		fmt.Fprintf(out, "      Missing token usage: %s -> %s rows; invalid usage: %s -> %s.\n", spendNumber(a.Quality.Missing), spendNumber(b.Quality.Missing), spendNumber(a.Quality.Invalid), spendNumber(b.Quality.Invalid))
	}
	if a.FailureTokens != nil || b.FailureTokens != nil {
		var before, after uint64
		if a.FailureTokens != nil {
			before = a.FailureTokens.Total
		}
		if b.FailureTokens != nil {
			after = b.FailureTokens.Total
		}
		fmt.Fprintf(out, "      Tokens recorded on failure rows: %s -> %s (gateway-recorded, finality unknown).\n", spendNumber(before), spendNumber(after))
	}
}

func renderRoutingCoverage(out io.Writer, rows []spend.RoutingCallTypeCoverage) {
	for _, row := range rows {
		fmt.Fprintf(out, "    %s:\n", row.CallType)
		for _, f := range []struct {
			name string
			a, b spend.RoutingFieldCoverage
		}{
			{"agent", row.Before.Agent, row.After.Agent},
			{"original route", row.Before.OriginalRoute, row.After.OriginalRoute},
			{"attempted fallbacks", row.Before.AttemptedFallbacks, row.After.AttemptedFallbacks},
			{"selected model", row.Before.SelectedModel, row.After.SelectedModel},
			{"deployment ID", row.Before.DeploymentID, row.After.DeploymentID},
			{"overhead marker", row.Before.OverheadMarker, row.After.OverheadMarker},
		} {
			fmt.Fprintf(out, "      %s known: %s -> %s; unknown: %s -> %s (column absent %s -> %s; null/empty %s -> %s; invalid %s -> %s).\n",
				f.name, spendNumber(f.a.KnownRows), spendNumber(f.b.KnownRows), spendNumber(f.a.MissingRows), spendNumber(f.b.MissingRows), spendNumber(f.a.ColumnAbsentRows), spendNumber(f.b.ColumnAbsentRows), spendNumber(f.a.EmptyOrNullRows), spendNumber(f.b.EmptyOrNullRows), spendNumber(f.a.InvalidRows), spendNumber(f.b.InvalidRows))
		}
	}
}

func renderRoutingRaw(out io.Writer, label string, pair spend.RoutingRawMeasures) {
	a, b := pair.Before, pair.After
	if a.Usage.Requests+b.Usage.Requests == 0 {
		return
	}
	fmt.Fprintf(out, "  %s: %s -> %s rows; raw recorded total tokens %s -> %s; recorded USD %s -> %s.\n",
		label, spendNumber(a.Usage.Requests), spendNumber(b.Usage.Requests), spendCoverageNumber(a.Usage.TotalTokens, a.Usage.TotalTokensOverflow), spendCoverageNumber(b.Usage.TotalTokens, b.Usage.TotalTokensOverflow), a.RecordedSpend, b.RecordedSpend)
	fmt.Fprintf(out, "    Raw usage missing: %s -> %s rows; invalid: %s -> %s. Spend missing: %s -> %s; invalid: %s -> %s; zero, origin unknown: %s -> %s.\n",
		spendNumber(a.Usage.MissingUsage), spendNumber(b.Usage.MissingUsage), spendNumber(a.Usage.InvalidUsage), spendNumber(b.Usage.InvalidUsage), spendNumber(a.SpendMissing), spendNumber(b.SpendMissing), spendNumber(a.SpendInvalid), spendNumber(b.SpendInvalid), spendNumber(a.SpendZeroUnknown), spendNumber(b.SpendZeroUnknown))
}
