// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import "github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"

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
	Item        string `json:"item"`
	Hash        string `json:"hash,omitempty"`
	BeforeShare *Share `json:"before_share,omitempty"`
	AfterShare  *Share `json:"after_share,omitempty"`
}

// Investigate composes Compare without weakening validation or changing inputs.
// A cannot_determine answer is not an error and never means zero activity.
func Investigate(before, after []summary.Envelope, options Options) (Investigation, error) {
	r, err := Compare(before, after, options)
	if err != nil {
		return Investigation{}, err
	}
	volume := Question{ID: "volume", Question: "More requests, or more reported tokens per request?", Status: "cannot_determine"}
	contributors := Question{ID: "contributors", Question: "Which tracked prompt contributors changed?", Status: "cannot_determine", Answer: "No prompt-weight sketch is available; missing measurements are not zero."}
	sessions := Question{ID: "sessions", Question: "Which sessions need investigation?", Status: "cannot_determine", Answer: "These summaries have no session-level token attribution. Prompt signatures and distinct MCP sessions cannot identify high-consumption sessions or diagnose loops."}
	coverage := Question{ID: "coverage", Question: "How much of this comparison can I trust?", Status: "observed", Answer: "Observation intervals are complete and every observed model request reports input and output usage. This does not prove complete upstream delivery, unsampled traffic, or provider-reported rather than inferred usage."}
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
		volume.Answer = "Some requests lack input or output usage. Recorded token totals remain visible, but a complete-request average or workload explanation cannot be recovered from these totals."
	case a.Requests == 0 || b.Requests == 0:
		volume.Answer = "At least one window has no observed model requests; tokens per request is undefined."
	default:
		// Validated counters are each at most MaxInt64, so this uint64 sum fits.
		x, y := input.Before+output.Before, input.After+output.After
		n0, n1 := float64(a.Requests), float64(b.Requests)
		p0, p1 := float64(x)/n0, float64(y)/n1
		volume.Status = "observed"
		volume.Answer = "Arithmetic split of observed token change, not a causal explanation. The symmetric split shares the interaction equally between request count and tokens per request; cache and reasoning subsets are not added again."
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
			contributors.Contributors = append(contributors.Contributors, Contributor{m.Item, m.Hash, share(m.Before, c.BeforeWeight), share(m.After, c.AfterWeight)})
		}
	}
	return Investigation{Version: 1, Questions: []Question{volume, contributors, sessions, coverage, source}, Evidence: r}, nil
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
