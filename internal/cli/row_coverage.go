// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func renderRowCoverage(out io.Writer, coverage *spend.RowCoverage) {
	if coverage == nil {
		return
	}
	fmt.Fprintln(out, "Spend-log row coverage (relative to LiteLLM daily request counts):")
	for _, day := range coverage.Days {
		suffix := "s"
		if day.ExportedRows == 1 {
			suffix = ""
		}
		fmt.Fprintf(out, "  %s UTC: %s exported row%s", day.Date, spendNumber(day.ExportedRows), suffix)
		if day.APIRequests != nil {
			suffix := "s"
			if *day.APIRequests == 1 {
				suffix = ""
			}
			fmt.Fprintf(out, "; %s daily request%s (%s successful, %s failed)", spendNumber(*day.APIRequests), suffix, spendNumber(*day.SuccessfulRequests), spendNumber(*day.FailedRequests))
		}
		switch day.Status {
		case "matched":
			fmt.Fprintln(out, "; counts match.")
		case "missing_rows":
			suffix := "s"
			if *day.MissingRows == 1 {
				suffix = ""
			}
			fmt.Fprintf(out, "; %s missing row%s.\n", spendNumber(*day.MissingRows), suffix)
		case "excess_rows":
			fmt.Fprintln(out, "; exported rows exceed daily counts; check scope and export timing.")
		case "partial_day":
			fmt.Fprintln(out, "; partial UTC day selected; coverage unknown.")
		case "daily_counts_missing":
			fmt.Fprintln(out, "; daily counts not exported; coverage unknown.")
		}
	}
	fmt.Fprintln(out, "Token and spend totals below use exported rows only.")
	fmt.Fprintln(out)
}
