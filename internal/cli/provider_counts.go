// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func providerCountText(n *uint64) string {
	if n == nil {
		return "unavailable"
	}
	return spendNumber(*n)
}

func renderProviderCoverage(out io.Writer, coverage *spend.ProviderCoverage) {
	if coverage == nil {
		return
	}
	fmt.Fprintln(out, "Provider count coverage:")
	if c := coverage.Contract; c != nil {
		fmt.Fprintf(out, "  Primary count basis: %s; scope matches: account=%s, project/workspace=%s, regions=%s, gateway=%s, operation=%s.\n", c.CountBasis, c.Population.Account, c.Population.Project, c.Population.Regions, c.Population.Gateway, c.Population.Operation)
		fmt.Fprintf(out, "  Counting units matched: %t; token units matched: %t; pagination complete: %t.\n", c.CountingUnitsMatch, c.TokenUnitsMatch, c.PaginationComplete)
		fmt.Fprintf(out, "  Settlement margin: %s seconds from day end to provider retrieval; retrieved at %s.\n", spendNumber(c.SettlementMarginSeconds), c.Retrieved.UTC().Format("2006-01-02T15:04:05.999999999Z"))
	} else {
		fmt.Fprintln(out, "  Population and counting units unverified; differences are arithmetic comparisons only.")
	}
	for _, d := range coverage.Days {
		fmt.Fprintf(out, "  %s UTC (%s; %s): logged rows %s; provider requests %s.\n", d.Date, d.Status, d.PopulationStatus, spendNumber(d.LoggedRows), providerCountText(d.ProviderRequests))
		fmt.Fprintf(out, "    LiteLLM daily requests (whole day): %s.\n", providerCountText(d.LiteLLMDailyRequests))
		if d.UnmappedLoggedRows != 0 {
			fmt.Fprintf(out, "    Unmapped logged rows: %s; model comparison incomplete.\n", spendNumber(d.UnmappedLoggedRows))
		}
		if d.LiteLLMTablesAgreeProviderDiffers && d.PopulationStatus == "matched" && coverage.Contract != nil && coverage.Contract.CountingUnitsMatch {
			fmt.Fprintln(out, "    Both LiteLLM tables agree with each other but differ from the provider count.")
		}
		for _, m := range d.Models {
			fmt.Fprintf(out, "    %s/%s: provider requests %s; logged rows %s; %s.\n", m.Provider, m.Model, providerCountText(m.ProviderRequests), spendNumber(m.LoggedRows), m.Status)
			if m.RequestDifference != nil {
				switch n := *m.RequestDifference; {
				case n > 0:
					fmt.Fprintf(out, "      Provider request count exceeds logged rows by %s", spendNumber(n))
					if m.Comparable {
						fmt.Fprint(out, " (net row deficit under declared matched population and counting units)")
					}
					fmt.Fprintln(out, ".")
				case n < 0:
					fmt.Fprintf(out, "      Logged rows exceed provider request count by %s.\n", spendNumber(-n))
				}
			}
			fmt.Fprintf(out, "      Logged failures: %s; unknown status: %s (included in logged rows).\n", spendNumber(m.FailedRows), spendNumber(m.UnknownStatusRows))
			fmt.Fprintf(out, "      Input tokens: provider %s; logged %s. Output tokens: provider %s; logged %s.\n", providerCountText(m.ProviderInputTokens), providerCountText(m.LoggedInputTokens), providerCountText(m.ProviderOutputTokens), providerCountText(m.LoggedOutputTokens))
			if m.InputDifference != nil {
				fmt.Fprintf(out, "      Provider minus logged input tokens: %s.\n", spendNumber(fmt.Sprintf("%+d", *m.InputDifference)))
			}
			if m.OutputDifference != nil {
				fmt.Fprintf(out, "      Provider minus logged output tokens: %s.\n", spendNumber(fmt.Sprintf("%+d", *m.OutputDifference)))
			}
		}
	}
	fmt.Fprintln(out, "Provider counts use one primary counter; provider errors and throttles are not added. Differences identify no individual requests. Recorded totals remain unchanged.")
	fmt.Fprintln(out)
}
