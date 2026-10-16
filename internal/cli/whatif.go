// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

const whatIfHelp = `Re-price a period of covered usage under explicit model-mix assumptions.

Usage:
  fleetdiff what-if --litellm-spend FILE --period PERIOD --from MODEL --to MODEL --fraction 0.5
  fleetdiff what-if --litellm-spend FILE --period PERIOD --scenario scenario.json
  fleetdiff what-if --summaries PATH --expected PRODUCER[,PRODUCER] --period PERIOD --scenario scenario.json

Options:
  --scope-key ID             Restrict a simple move to a recorded API key
  --scope-team ID            Restrict a simple move to a recorded team
  --prices FILE              Versioned, dated USD-per-million price override
  --workload-map FILE        Map key/team selectors to qa, extraction or coding
  --format text|json         Output format (default text)

PERIOD is a UTC date or RFC3339 start/end. Fractions are uniform shares of
original covered token volume. Raw identities are never printed. Put selectors
in a protected scenario file to avoid shell history. See docs/WHAT_IF.md.
No network requests are made. Missing model or token splits remain unknown.
`

func runWhatIf(args []string, out, errout io.Writer) int {
	fail := func(code int, s string) int { fmt.Fprintln(errout, s); return code }
	f := flag.NewFlagSet("what-if", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	spendFile := f.String("litellm-spend", "", "")
	summaryFile := f.String("summaries", "", "")
	expected := f.String("expected", "", "")
	period := f.String("period", "", "")
	from := f.String("from", "", "")
	to := f.String("to", "", "")
	fraction := f.String("fraction", "", "")
	scenarioFile := f.String("scenario", "", "")
	pricesFile := f.String("prices", "", "")
	mappingFile := f.String("workload-map", "", "")
	key := f.String("scope-key", "", "")
	team := f.String("scope-team", "", "")
	format := f.String("format", "text", "")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err = io.WriteString(out, whatIfHelp); err != nil {
				return fail(1, "cannot write what-if help")
			}
			return 0
		}
		return fail(2, "invalid what-if options; run fleetdiff what-if --help")
	}
	if f.NArg() != 0 || (*spendFile == "") == (*summaryFile == "") || *period == "" || (*format != "text" && *format != "json") || *spendFile != "" && *expected != "" || *summaryFile != "" && *expected == "" {
		return fail(2, "choose one input, a period, and text or json; summaries also require expected producers")
	}
	scenario := reprice.Scenario{Version: 1}
	if *scenarioFile != "" {
		if *from != "" || *to != "" || *fraction != "" || *key != "" || *team != "" {
			return fail(2, "use either a scenario file or simple move flags")
		}
		if err := reprice.ReadConfig(*scenarioFile, &scenario); err != nil {
			return fail(2, err.Error())
		}
	} else {
		if *key != "" && *team != "" {
			return fail(2, "a simple move accepts one key or team scope")
		}
		m := reprice.Move{From: *from, To: *to, Fraction: *fraction}
		if *key != "" {
			m.Scope = &reprice.Selector{Kind: "key", Value: *key}
		}
		if *team != "" {
			m.Scope = &reprice.Selector{Kind: "team", Value: *team}
		}
		scenario.Moves = []reprice.Move{m}
	}
	prices, factors, err := reprice.Bundled()
	if err != nil {
		return fail(1, err.Error())
	}
	if *pricesFile != "" {
		var override reprice.Prices
		if err = reprice.ReadConfig(*pricesFile, &override); err != nil {
			return fail(2, err.Error())
		}
		prices, err = reprice.OverridePrices(prices, override)
		if err != nil {
			return fail(2, err.Error())
		}
	}
	var mapping reprice.WorkloadMap
	if *mappingFile != "" {
		if err = reprice.ReadConfig(*mappingFile, &mapping); err != nil {
			return fail(2, err.Error())
		}
	}
	c, err := reprice.New(prices, factors, scenario, mapping)
	if err != nil {
		return fail(2, err.Error())
	}
	defer c.Close()
	var source reprice.Source
	if *spendFile != "" {
		source, err = spend.ReadReprice(*spendFile, *period, c.Add)
	} else {
		source, err = compare.ReadReprice(*summaryFile, *period, strings.Split(*expected, ","), c.Add)
	}
	if err != nil {
		return fail(1, err.Error())
	}
	r := c.Result(source)
	if *pricesFile != "" {
		r.PriceSource = "override"
	}
	var b bytes.Buffer
	if *format == "json" {
		enc := json.NewEncoder(&b)
		enc.SetIndent("", "  ")
		if enc.Encode(r) != nil {
			return fail(1, "cannot encode what-if report")
		}
	} else {
		renderWhatIf(&b, r)
	}
	if _, err = io.Copy(out, &b); err != nil {
		return fail(1, "cannot write what-if report")
	}
	return 0
}

func money(s string) string {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "unknown"
	}
	if r.Sign() > 0 && r.Cmp(big.NewRat(1, 1000000)) < 0 {
		return "< $0.000001"
	}
	return "$" + r.FloatString(6)
}
func percent(s string) string {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return "unknown"
	}
	r.Mul(r, big.NewRat(100, 1))
	return strings.TrimRight(strings.TrimRight(r.FloatString(2), "0"), ".") + "%"
}

func renderWhatIf(out io.Writer, r reprice.Report) {
	lead := "Covered usage under the model-mix scenario"
	matched := false
	for _, m := range r.Moves {
		matched = matched || m.Covered > 0
	}
	if len(r.Moves) == 1 {
		m := r.Moves[0]
		lead = fmt.Sprintf("Moving %s of %s traffic to %s (covered usage)", percent(m.Fraction), m.From, m.To)
	}
	if r.Before == nil {
		fmt.Fprintln(out, "No covered usage can be re-priced from this input.")
	} else if !matched {
		fmt.Fprintf(out, "No covered traffic matches the requested moves; covered source cost remains %s.\n", money(*r.Before))
	} else if r.Envelope != nil {
		fmt.Fprintf(out, "%s: %s -> %s to %s, depending on workload assumptions.\n", lead, money(*r.Before), money(r.Envelope.Low), money(r.Envelope.High))
	} else {
		fmt.Fprintf(out, "%s: %s -> %s at unchanged output verbosity.\n", lead, money(*r.Before), money(*r.Level1))
	}
	fmt.Fprintf(out, "Period: %s -> %s (end exclusive). Prices: %s (%s); measured factors: %s.\n", r.Source.Start.Format("Mon 2006-01-02 15:04:05 MST"), r.Source.End.Format("Mon 2006-01-02 15:04:05 MST"), r.PriceDate, r.PriceSource, r.FactorDate)
	if r.Level1 != nil {
		fmt.Fprintf(out, "\nLevel 1, same work and output verbosity: %s\n", money(*r.Level1))
	}
	for _, w := range r.Level2 {
		fmt.Fprintf(out, "Level 2, %s: %s (propagated mean-interval endpoints %s to %s)\n", w.Workload, money(w.Mean), money(w.Low), money(w.High))
	}
	for _, m := range r.Moves {
		fmt.Fprintf(out, "\n%s: %s of %s -> %s", m.Item, percent(m.Fraction), m.From, m.To)
		if m.Scoped {
			fmt.Fprint(out, " (selected identity scope)")
		}
		fmt.Fprintln(out)
		if m.Level1 != nil {
			fmt.Fprintf(out, "  Moved traffic, level 1: %s -> %s; input tokenizer factor %s\n", money(*m.Before), money(*m.Level1), m.InputFactor)
		}
		if m.OutputShare != nil {
			fmt.Fprintf(out, "  Output share of moved target cost at level 1: %s\n", percent(*m.OutputShare))
		}
		for _, w := range m.Level2 {
			fmt.Fprintf(out, "  %s: %s (%s to %s)\n", w.Workload, money(w.Mean), money(w.Low), money(w.High))
		}
		if m.Gap != "" {
			fmt.Fprintln(out, "  "+m.Gap)
		}
	}
	fmt.Fprintf(out, "\nCoverage: %d observations priced; missing splits are excluded, never zero.\n", r.Covered)
	for _, k := range []string{"missing_split", "missing_scope_identity", "unusable_usage", "unsupported_call_type", "unknown_model_price", "invalid_cache_partition", "outside_price_tier"} {
		if r.Excluded[k] > 0 {
			fmt.Fprintf(out, "  %s: %d\n", strings.ReplaceAll(k, "_", " "), r.Excluded[k])
		}
	}
	if r.Unmapped > 0 {
		fmt.Fprintf(out, "  %d moved observations have no workload mapping; all three workload assumptions are shown where measured.\n", r.Unmapped)
	}
	for _, gap := range r.Source.Gaps {
		fmt.Fprintln(out, "  "+gap)
	}
	fmt.Fprintln(out, "Dominant uncertainty: "+r.Dominant)
	fmt.Fprintln(out, "\nAssumptions")
	for _, s := range r.Assumptions {
		fmt.Fprintln(out, "- "+s)
	}
	fmt.Fprintln(out, "\nLimits")
	for _, s := range r.Limits {
		fmt.Fprintln(out, "- "+s)
	}
}
