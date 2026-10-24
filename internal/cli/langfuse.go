// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/langfuse"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func runLangfuse(dir, group, tag, format string, opts spend.Options, out, errout io.Writer) int {
	fail := func(code int, msg string) int { fmt.Fprintln(errout, msg); return code }
	if format != "text" && format != "json" || opts.Top < 1 || opts.Top > 100 {
		return fail(2, "use text or json output and top between 1 and 100")
	}
	r, err := langfuse.Investigate(dir, group, tag, opts)
	if err != nil {
		return fail(1, err.Error())
	}
	var b bytes.Buffer
	if format == "json" {
		e := json.NewEncoder(&b)
		e.SetIndent("", "  ")
		if e.Encode(r) != nil {
			return fail(1, "cannot encode Langfuse investigation")
		}
	} else {
		fmt.Fprintln(&b, "Langfuse GENERATION observations; recorded cost (Langfuse), not an invoice.")
		c := r.Coverage
		fmt.Fprintf(&b, "Local coverage: %d manifests, %d/%d files verified; %d missing, %d partial, %d unmanifested.\n", c.Manifests, c.VerifiedFiles, c.ListedFiles, c.MissingFiles, c.PartialFiles, c.ExtraFiles)
		fmt.Fprintf(&b, "Observations: %d physical, %d unique, %d duplicate copies removed; %d GENERATION.\n", c.PhysicalRows, c.UniqueRows, c.Duplicates, c.GenerationRows)
		fmt.Fprintf(&b, "Usage coverage: %d missing, %d partial, %d invalid; %d unmapped category occurrences; %d usage rows with zero recorded cost.\n", c.MissingUsage, c.PartialUsage, c.InvalidUsage, c.UnknownCategories, c.UsageZeroCost)
		if c.Incomplete {
			fmt.Fprintf(&b, "Incomplete local export coverage: %d schedule gaps, %d project-periods not fully manifested; absent data is not zero.\n", c.Gaps, c.UncoveredPeriods)
			fmt.Fprintln(&b, "All totals below are partial observations from verified files, not complete selected-period totals.")
		}
		var shared bytes.Buffer
		renderSpend(&shared, r.Report)
		text := strings.ReplaceAll(shared.String(), "Recorded spend (USD, analyzed calls)", "Recorded cost (Langfuse, analyzed GENERATION observations)")
		text = strings.ReplaceAll(text, "Comparison incomplete: some selected call types are not analyzed.", "Comparison incomplete: the supplied local export does not fully cover the selected periods.")
		text = strings.ReplaceAll(text, "gateway-recorded", "Langfuse-recorded")
		text = strings.ReplaceAll(text, "your local LiteLLM logs", "your local Langfuse export")
		text = strings.ReplaceAll(text, "Unknown status", "Call status unknown (Langfuse doesn't record it)")
		if c.Incomplete {
			text = strings.ReplaceAll(text, "Whole-period per-request split unavailable while selected call types remain unanalyzed.", "Whole-period per-request split unavailable while local export coverage is incomplete.")
			text = strings.ReplaceAll(text, "Next: review unsupported call types in your local Langfuse export; analyze those rows before drawing a whole-export token-change conclusion.", "Next: restore missing or partial files and manifests before drawing a whole-period conclusion; observed totals above remain partial.")
			text = strings.ReplaceAll(text, "analyzed calls only", "verified-file observations only, partial")
		}
		b.WriteString(text)
	}
	if _, err := io.Copy(out, &b); err != nil {
		return fail(1, "cannot write Langfuse investigation")
	}
	return 0
}
