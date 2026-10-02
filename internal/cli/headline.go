// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"fmt"
	"math"
	"strconv"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func investigationHeadline(r compare.Investigation) string {
	for _, q := range r.Questions {
		if q.ID != "sessions" {
			continue
		}
		// Keep token and attempt shares separate, even when both are available.
		for _, measurement := range []struct{ name, unit string }{
			{"top_sessions", "tokens"}, {"top_sessions_requests", "model attempts"},
		} {
			tracked, flagged := 0, 0
			var leading *compare.Share
			ranged := false
			for _, c := range q.Contributors {
				if c.Measurement != measurement.name {
					continue
				}
				tracked++
				if c.Flag == "runaway_candidate" && c.AfterShare != nil {
					flagged++
					if leading == nil || c.AfterShare.Lower > leading.Lower {
						leading = c.AfterShare
						ranged = leading.Lower != leading.Upper || c.After != nil && c.After.Lower != c.After.Upper
					}
				}
			}
			if leading == nil {
				continue
			}
			population := "tracked"
			for _, c := range r.Evidence.Concentration {
				if c.Name == measurement.name && c.CandidateCount > tracked {
					population = "shown"
				}
			}
			percent := fmt.Sprintf("%.2f%%", leading.Lower*100)
			if ranged {
				// Round interval endpoints outward so display preserves the bounds.
				lower, upper := leading.Lower*10000, leading.Upper*10000
				if leading.Lower == leading.Upper {
					lower = math.Nextafter(lower, math.Inf(-1))
					upper = math.Nextafter(upper, math.Inf(1))
				}
				percent = fmt.Sprintf("[%.2f%%, %.2f%%]", max(0, math.Floor(lower)/100), min(100, math.Ceil(upper)/100))
			}
			label := ": "
			if flagged > 1 {
				label = "; one flagged session: "
			}
			return fmt.Sprintf("%d of %d %s sessions flagged%s%s of attributed %s.", flagged, tracked, population, label, percent, measurement.unit)
		}
	}
	for _, q := range r.Questions {
		if q.ID == "volume" {
			if v := q.Volume; v != nil {
				delta := "+" + strconv.FormatUint(v.AfterTokens-v.BeforeTokens, 10)
				if v.AfterTokens < v.BeforeTokens {
					delta = "-" + strconv.FormatUint(v.BeforeTokens-v.AfterTokens, 10)
				}
				return fmt.Sprintf("Reported tokens: %d -> %d (%s); model attempts: %d -> %d.", v.BeforeTokens, v.AfterTokens, delta, v.BeforeRequests, v.AfterRequests)
			}
			return "Token-change breakdown unavailable. " + q.Answer
		}
	}
	return "Comparison ready. See observed coverage below."
}
