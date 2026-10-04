// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func renderInvestigation(out io.Writer, r compare.Investigation, threshold float64) {
	headline := investigationHeadline(r)
	fmt.Fprintln(out, headline)
	volumeShown, attributed, flagged := false, false, false
	for _, q := range r.Questions {
		if v := q.Volume; v != nil {
			volumeShown = true
			if !strings.HasPrefix(headline, "Reported tokens:") {
				fmt.Fprintf(out, "Reported tokens: %d -> %d; model attempts: %d -> %d.\n", v.BeforeTokens, v.AfterTokens, v.BeforeRequests, v.AfterRequests)
			}
			fmt.Fprintf(out, "  %+.0f tokens from attempt count; %+.0f tokens from tokens per attempt.\n", v.RequestContribution, v.TokensPerRequestContribution)
			fmt.Fprintf(out, "  Tokens per attempt: %.2f -> %.2f.\n", v.BeforeAverage, v.AfterAverage)
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
	for _, q := range r.Questions {
		if q.ID != "cache" {
			continue
		}
		fmt.Fprintln(out, "\nDid caching get worse?")
		if c := q.Cache; c != nil {
			fmt.Fprintf(out, "Cached-token share: %.2f%% -> %.2f%% (%+.2f percentage points; %s).\n", c.BeforeShare*100, c.AfterShare*100, c.DeltaPercentagePoints, c.Direction)
			fmt.Fprintf(out, "  Recorded cache-read/input tokens: %d/%d -> %d/%d.\n", c.BeforeCacheReadInputTokens, c.BeforeInputTokens, c.AfterCacheReadInputTokens, c.AfterInputTokens)
			for _, entry := range []struct {
				name   string
				window compare.Window
			}{{"Before", r.Evidence.Before}, {"After", r.Evidence.After}} {
				fmt.Fprintf(out, "  %s: %s for %s.\n", entry.name, time.Unix(0, entry.window.Start).UTC().Format(time.RFC3339Nano), time.Duration(entry.window.Duration))
			}
			fmt.Fprintln(out, "  Share of recorded input tokens served from cache.")
		} else {
			fmt.Fprintln(out, "Cached-token share unavailable: "+q.Answer)
		}
	}
	var notes []string
	if attributed {
		notes = append(notes, "Shares use ranked activity; missing keys are excluded. Aliases are local to each measurement.")
	}
	if flagged {
		notes = append(notes, fmt.Sprintf("Flags need a share lower bound above %g%% and complete coverage; check the session's traces.", threshold*100))
	}
	if len(r.Evidence.DroppedMeasurements) != 0 {
		notes = append(notes, "Some rankings are missing from input snapshots.")
	}
	notes = append(notes, "Use --format json for full details.")
	fmt.Fprintln(out, "\nNotes: "+strings.Join(notes, " "))
	var missingRankings []string
	for _, kind := range []string{"user", "session"} {
		needsRanking := slices.ContainsFunc(r.Questions, func(q compare.Question) bool {
			return q.ID == kind+"s" && q.Status == "cannot_determine" && len(q.Contributors) == 0
		})
		for _, name := range []string{"top_" + kind + "s", "top_" + kind + "s_requests"} {
			if slices.Contains(r.Evidence.DroppedMeasurements, name) || slices.ContainsFunc(r.Evidence.Concentration, func(c compare.Concentration) bool { return c.Name == name }) {
				needsRanking = false
			}
		}
		if needsRanking {
			missingRankings = append(missingRankings, kind)
		}
	}
	if len(missingRankings) > 0 {
		fmt.Fprintf(out, "Turn on %s rankings (topk_keys) for more answers.\n", strings.Join(missingRankings, " and "))
	}
}

func intervalText(v compare.Interval) string {
	if v.Lower == v.Upper {
		return fmt.Sprint(v.Lower)
	}
	return fmt.Sprintf("[%d, %d]", v.Lower, v.Upper)
}
