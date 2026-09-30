// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

// Investigation is the question-oriented JSON API. Evidence retains the exact
// counters and integer bounds; derived ratios and contributions are floating point.
type Investigation struct {
	Version   int        `json:"version"`
	Questions []Question `json:"questions"`
	Evidence  Report     `json:"evidence"`
}

type Question struct {
	ID           string        `json:"id"`
	Question     string        `json:"question"`
	Status       string        `json:"status"`
	Answer       string        `json:"answer"`
	Volume       *VolumeChange `json:"volume,omitempty"`
	Contributors []Contributor `json:"contributors,omitempty"`
}

type VolumeChange struct {
	BeforeTokens                 uint64  `json:"before_reported_tokens"`
	AfterTokens                  uint64  `json:"after_reported_tokens"`
	BeforeRequests               uint64  `json:"before_requests"`
	AfterRequests                uint64  `json:"after_requests"`
	BeforeAverage                float64 `json:"before_tokens_per_request"`
	AfterAverage                 float64 `json:"after_tokens_per_request"`
	RequestContribution          float64 `json:"request_count_contribution_tokens"`
	TokensPerRequestContribution float64 `json:"tokens_per_request_contribution_tokens"`
}

type Share struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

type Contributor struct {
	Item        string    `json:"item"`
	Hash        string    `json:"hash,omitempty"`
	BeforeShare *Share    `json:"before_share,omitempty"`
	AfterShare  *Share    `json:"after_share,omitempty"`
	Measurement string    `json:"measurement,omitempty"`
	WeightUnit  string    `json:"weight_unit,omitempty"`
	Before      *Interval `json:"before,omitempty"`
	After       *Interval `json:"after,omitempty"`
	Delta       *Interval `json:"delta,omitempty"`
	Flag        string    `json:"flag,omitempty"`
}

// Investigate composes Compare without weakening validation or changing inputs.
// A cannot_determine answer is not an error and never means zero activity.
func Investigate(before, after []summary.Envelope, options Options) (Investigation, error) {
	r, err := Compare(before, after, options)
	if err != nil {
		return Investigation{}, err
	}
	volume := Question{ID: "volume", Question: "More model attempts, or more reported tokens per attempt?", Status: "cannot_determine"}
	contributors := Question{ID: "contributors", Question: "Which tracked prompt contributors changed?", Status: "cannot_determine", Answer: "No prompt-weight sketch is available; missing measurements are not zero."}
	sessions := Question{ID: "sessions", Question: "Which sessions need investigation?", Status: "cannot_determine", Answer: "These summaries have no session-level token attribution. Prompt signatures and distinct MCP sessions cannot identify high-consumption sessions or diagnose loops."}
	coverage := Question{ID: "coverage", Question: "How much of this comparison can I trust?", Status: "observed", Answer: "Observation intervals are complete and every observed model attempt reports input and output usage. This does not prove complete upstream delivery, unsampled traffic, or provider-reported rather than inferred usage."}
	var input, output *Counter
	for i := range r.Counters {
		switch r.Counters[i].Name {
		case "input_tokens":
			input = &r.Counters[i]
		case "output_tokens":
			output = &r.Counters[i]
		}
	}
	a, b := r.Before.Usage, r.After.Usage
	switch {
	case !r.Complete:
		volume.Answer = "An expected producer or observation interval is incomplete. Differences may reflect missing observations rather than workload changes."
	case a == nil || b == nil || input == nil || output == nil:
		volume.Answer = "Request, token, or missing-usage counters are unavailable."
	case a.Missing != 0 || b.Missing != 0:
		volume.Answer = "Some model attempts lack input or output usage. Recorded token totals remain visible, but a complete-usage average or workload explanation cannot be recovered from these totals."
	case a.Requests == 0 || b.Requests == 0:
		volume.Answer = "At least one window has no observed model attempts; tokens per attempt is undefined."
	default:
		// Validated counters are each at most MaxInt64, so this uint64 sum fits.
		x, y := input.Before+output.Before, input.After+output.After
		n0, n1 := float64(a.Requests), float64(b.Requests)
		p0, p1 := float64(x)/n0, float64(y)/n1
		volume.Status = "observed"
		volume.Answer = "Arithmetic split of observed token change, not a causal explanation. The symmetric split shares the interaction equally between attempt count and tokens per attempt; cache and reasoning subsets are not added again. Attempts are matching model spans, including failures and retries, not unique user requests."
		volume.Volume = &VolumeChange{x, y, a.Requests, b.Requests, p0, p1, (n1 - n0) * (p0 + p1) / 2, (p1 - p0) * (n0 + n1) / 2}
	}
	if !r.Complete || a == nil || b == nil || input == nil || output == nil || a.Missing != 0 || b.Missing != 0 {
		coverage.Status = "limited"
		coverage.Answer = "Coverage is incomplete or unknown. Check evidence.before and evidence.after for observed requests, missing usage, and producer coverage. Do not interpret a lower observed total as savings."
	}
	source := usageSourceQuestion(r)
	if volume.Volume != nil && source.Status != "observed" {
		volume.Answer += " Provider origin is not established for every count; zeros or estimates inserted upstream can change this arithmetic without changing actual consumption."
	}
	for _, c := range r.Concentration {
		if c.Name != "top_prompts" {
			continue
		}
		if c.BeforeWeight == 0 && c.AfterWeight == 0 {
			contributors.Answer = "No prompt weight was recorded. This does not establish that no prompts or tokens were used."
			break
		}
		contributors.Status = "observed"
		contributors.Answer = "Shares use each prompt sketch's recorded configured weight, not all application tokens. Items are tracked candidates, not a guaranteed top-k ranking or full concentration curve; integer counts and delta bounds are in evidence.concentration."
		if coverage.Status == "limited" {
			contributors.Status = "limited"
		}
		for _, m := range c.Movers {
			contributors.Contributors = append(contributors.Contributors, Contributor{Item: m.Item, Hash: m.Hash, BeforeShare: share(m.Before, c.BeforeWeight), AfterShare: share(m.After, c.AfterWeight)})
		}
	}
	threshold, _ := flagShare(options) // Compare has already validated the option.
	// The shortest decimal form defines the cutoff: .3 means exactly 3/10.
	cutoff, _ := new(big.Rat).SetString(strconv.FormatFloat(threshold, 'g', -1, 64))
	sessions = attributionQuestion(sessions, r, []string{"top_sessions", "top_sessions_requests"}, coverage.Status == "limited", cutoff)
	users := Question{ID: "users", Question: "Which tracked users contribute tokens or model attempts?", Status: "cannot_determine", Answer: "No comparable user-level attribution is available; missing measurements are not zero. This is not billing or a full user ranking."}
	users = attributionQuestion(users, r, []string{"top_users", "top_users_requests"}, coverage.Status == "limited", cutoff)
	for _, c := range r.Concentration {
		if c.Name == "top_prompts_requests" {
			contributors.Question = "Which tracked prompt contributors changed in tokens or model attempts?"
			contributors = attributionQuestion(contributors, r, []string{"top_prompts", "top_prompts_requests"}, coverage.Status == "limited", cutoff)
			break
		}
	}
	return Investigation{Version: 1, Questions: []Question{volume, contributors, sessions, coverage, users, source}, Evidence: r}, nil
}

func attributionQuestion(q Question, r Report, names []string, tokenLimited bool, threshold *big.Rat) Question {
	q.Contributors = nil
	found, limited, dropped := false, false, false
	for _, name := range names {
		dropped = dropped || slices.Contains(r.DroppedMeasurements, name)
	}
	for _, c := range r.Concentration {
		if !slices.Contains(names, c.Name) || c.BeforeWeight == 0 && c.AfterWeight == 0 {
			continue
		}
		found = true
		requests := strings.HasSuffix(c.Name, "_requests")
		incomplete := !r.Complete || !requests && tokenLimited
		limited = limited || incomplete
		for _, m := range c.Movers {
			contributor := Contributor{
				Item: m.Item, Hash: m.Hash, Measurement: c.Name, WeightUnit: c.WeightUnit,
				Before: &m.Before, After: &m.After, Delta: &m.Delta,
				BeforeShare: share(m.Before, c.BeforeWeight), AfterShare: share(m.After, c.AfterWeight),
			}
			if q.ID == "sessions" && !incomplete && c.AfterWeight > 0 {
				// Display rounding must not move an integer bound across the threshold.
				lower := new(big.Rat).SetFrac64(m.After.Lower, c.AfterWeight)
				if lower.Cmp(threshold) > 0 {
					contributor.Flag = "runaway_candidate"
				}
			}
			q.Contributors = append(q.Contributors, contributor)
		}
	}
	if !found {
		if dropped {
			q.Answer = "Attribution is absent from one or more input snapshots and was omitted across both windows. Missing attribution is not zero; comparable contributor changes cannot be determined."
		} else {
			for _, c := range r.Concentration {
				if slices.Contains(names, c.Name) {
					q.Answer = "No attributed sketch weight was recorded. This does not establish zero traffic or zero consumption."
				}
			}
		}
		q.Status = "cannot_determine"
		return q
	}
	q.Status = "observed"
	q.Answer = "Shares and count/delta bounds describe tracked candidates relative only to each sketch's attributed weight, not all traffic or a guaranteed top-k ranking. Per-key missing-ID coverage is not recorded; shares exclude unattributed activity. Token weights are recorded input plus output usage; request weights are model attempts, including failures and retries, not unique user requests. This is not billing or a root-cause diagnosis."
	if q.ID == "sessions" {
		q.Answer += " A runaway candidate requires an after lower-bound share of attributed session sketch weight strictly above the configured threshold and complete relevant observations. The flag is an investigation prompt only, not a share of all application weight or proof of a loop."
	}
	if limited {
		q.Status = "limited"
		q.Answer += " Observation intervals or token coverage are incomplete or unknown. Flags are suppressed for the affected attribution; request-weight attribution does not require token usage."
	}
	if dropped {
		q.Answer += " Other requested attribution was absent from some snapshots and omitted; those changes cannot be determined."
	}
	return q
}

func usageSourceQuestion(r Report) Question {
	q := Question{ID: "usage_source", Question: "Were these counts reported by the provider?", Status: "cannot_determine", Answer: "Source provenance was not recorded for every input/output field. Numeric coverage does not establish provider coverage; gateways may fill absent counts with zeros or estimates."}
	if r.Before.Usage == nil || r.After.Usage == nil || r.Before.Usage.Requests == 0 || r.After.Usage.Requests == 0 {
		return q
	}
	providerOnly := true
	unknown := false
	for _, field := range []string{"input", "output"} {
		remaining := [2]uint64{r.Before.Usage.Requests, r.After.Usage.Requests}
		for _, source := range []string{"provider_reported", "inferred", "unavailable", "unknown"} {
			var counter *Counter
			for i := range r.Counters {
				if r.Counters[i].Name == "usage_provenance.v1."+field+"."+source {
					counter = &r.Counters[i]
					break
				}
			}
			if counter == nil {
				return q
			}
			for side, value := range [2]uint64{counter.Before, counter.After} {
				if value > remaining[side] {
					q.Answer = "Source provenance counts are inconsistent with observed requests. Do not use them to establish provider coverage."
					return q
				}
				remaining[side] -= value
				if source == "unknown" && value != 0 {
					unknown = true
				}
				if source != "provider_reported" && value != 0 {
					providerOnly = false
				}
			}
		}
		if remaining != [2]uint64{} {
			return q
		}
	}
	if unknown {
		return q
	}
	q.Status = "limited"
	q.Answer = "Some input/output fields are declared inferred or unavailable at source. Declared unavailable counts are excluded by the collector. See usage_provenance.v1 counters in evidence; do not interpret a lower total as provider savings."
	if providerOnly && r.Complete && r.Before.Usage.Missing == 0 && r.After.Usage.Missing == 0 {
		q.Status = "observed"
		q.Answer = "The instrumenter declares every observed input/output count provider-reported. This declaration is not authenticated proof, does not establish complete upstream delivery, and is not invoice reconciliation."
	} else if providerOnly {
		q.Answer = "Observed fields are declared provider-reported, but producer, interval, or numeric coverage is incomplete. The declarations do not establish complete provider usage."
	}
	return q
}

func share(value Interval, total int64) *Share {
	if total == 0 {
		return nil
	}
	return &Share{float64(max(0, value.Lower)) / float64(total), float64(min(total, value.Upper)) / float64(total)}
}
