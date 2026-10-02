// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func renderInvestigation(out io.Writer, r compare.Investigation, threshold float64) {
	headline := investigationHeadline(r)
	fmt.Fprintln(out, headline)
	var more []string
	volumeShown, attributed, flagged := false, false, false
	for _, q := range r.Questions {
		if v := q.Volume; v != nil {
			volumeShown = true
			if !strings.HasPrefix(headline, "Reported tokens:") {
				fmt.Fprintf(out, "Reported tokens: %d -> %d; model attempts: %d -> %d.\n", v.BeforeTokens, v.AfterTokens, v.BeforeRequests, v.AfterRequests)
			}
			fmt.Fprintf(out, "  %+.2f tokens from attempt count; %+.2f tokens from tokens per attempt.\n", v.RequestContribution, v.TokensPerRequestContribution)
			fmt.Fprintf(out, "  Tokens per attempt: %.2f -> %.2f.\n", v.BeforeAverage, v.AfterAverage)
		}
		if q.Status == "cannot_determine" {
			switch q.ID {
			case "volume":
				more = append(more, "use complete producer and usage coverage with model attempts in both windows to split token change")
			case "contributors":
				more = append(more, "supply prompt_key topk_keys with recorded weight in both windows")
			case "sessions":
				more = append(more, "supply session_key topk_keys with recorded weight in both windows")
			case "users":
				more = append(more, "supply user_key topk_keys with recorded weight in both windows")
			case "usage_source":
				more = append(more, "enable usage provenance to label provider-reported counts")
			}
		}
		if q.ID == "usage_source" && q.Status != "cannot_determine" {
			fmt.Fprintln(out, "Provider origin: "+q.Answer)
		}
	}
	if !volumeShown {
		var totalBefore, totalAfter uint64
		fields := 0
		for _, c := range r.Evidence.Counters {
			if c.Name == "input_tokens" || c.Name == "output_tokens" {
				totalBefore += c.Before
				totalAfter += c.After
				fields++
			}
		}
		if fields == 2 {
			fmt.Fprintf(out, "Recorded tokens: %d -> %d.\n", totalBefore, totalAfter)
		}
	}
	fmt.Fprint(out, "Coverage (before -> after): ")
	if a, b := r.Evidence.Before.Usage, r.Evidence.After.Usage; a != nil && b != nil {
		fmt.Fprintf(out, "%d/%d -> %d/%d attempts with both usage fields; ", a.Complete, a.Requests, b.Complete, b.Requests)
	} else {
		fmt.Fprint(out, "token coverage unknown; ")
	}
	fmt.Fprintf(out, "missing producers %d -> %d; partial producers %d -> %d.\n", len(r.Evidence.Before.MissingProducers), len(r.Evidence.After.MissingProducers), len(r.Evidence.Before.PartialProducers), len(r.Evidence.After.PartialProducers))
	for _, q := range r.Questions {
		if q.Status == "cannot_determine" || len(q.Contributors) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s", q.Question)
		if q.Status == "limited" {
			fmt.Fprint(out, " (partial coverage)")
		}
		fmt.Fprintln(out)
		table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "  key / measurement\tbefore -> after\tafter share\treview")
		for _, c := range q.Contributors {
			// Legacy prompt answers keep integer bounds in evidence, not the question.
			if q.ID == "contributors" && c.Measurement == "" {
				for _, measurement := range r.Evidence.Concentration {
					if measurement.Name != "top_prompts" {
						continue
					}
					for _, mover := range measurement.Movers {
						if mover.Item == c.Item {
							c.Measurement = measurement.Name
							c.Before, c.After = &mover.Before, &mover.After
						}
					}
				}
			}
			label := c.Item
			if q.ID == "sessions" || q.ID == "users" {
				label = strings.TrimSuffix(q.ID, "s") + "-" + strings.TrimPrefix(c.Item, "item-")
			}
			if c.Hash != "" {
				label += " (" + c.Hash + ")"
			}
			if c.Measurement != "" {
				label += " / " + c.Measurement
			}
			before, after := "unknown", "unknown"
			if c.Before != nil && c.After != nil {
				before, after = intervalText(*c.Before), intervalText(*c.After)
			}
			flag := ""
			if c.Flag == "runaway_candidate" {
				flag, flagged = "flagged for review", true
			}
			fmt.Fprintf(table, "  %s\t%s -> %s\t%s\t%s\n", label, before, after, shareText(c.AfterShare, c.After), flag)
			attributed = true
		}
		_ = table.Flush()
	}
	fmt.Fprintln(out, "\nNotes")
	if attributed {
		fmt.Fprintln(out, "  Shares use each sketch's attributed weight; activity without a key is excluded. Aliases are local to each measurement. Rows show tracked candidates up to --top.")
	}
	if flagged {
		fmt.Fprintf(out, "  Flags mark a share lower bound above %g%% with complete relevant observations. Open the session's traces to see why.\n", threshold*100)
	}
	if len(r.Evidence.DroppedMeasurements) != 0 {
		fmt.Fprintln(out, "  Some optional attribution is missing from input snapshots; see JSON dropped_measurements.")
	}
	fmt.Fprintln(out, "  Use --format json for integer bounds and all counters.")
	if len(more) > 0 {
		fmt.Fprintln(out, "More answers with more data: "+strings.Join(more, "; ")+".")
	}
}

func intervalText(v compare.Interval) string {
	if v.Lower == v.Upper {
		return fmt.Sprint(v.Lower)
	}
	return fmt.Sprintf("[%d, %d]", v.Lower, v.Upper)
}
