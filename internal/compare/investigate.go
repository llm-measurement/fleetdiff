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
	Cache        *CacheChange  `json:"cache,omitempty"`
}

type CacheChange struct {
	BeforeInputTokens          uint64  `json:"before_input_tokens"`
	AfterInputTokens           uint64  `json:"after_input_tokens"`
	BeforeCacheReadInputTokens uint64  `json:"before_cache_read_input_tokens"`
	AfterCacheReadInputTokens  uint64  `json:"after_cache_read_input_tokens"`
	BeforeShare                float64 `json:"before_share"`
	AfterShare                 float64 `json:"after_share"`
	DeltaPercentagePoints      float64 `json:"delta_percentage_points"`
	Direction                  string  `json:"direction"`
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
	contributors := Question{ID: "contributors", Question: "Which tracked prompt contributors changed?", Status: "cannot_determine", Answer: "Add prompt-weight sketches to compare prompt contributors."}
	sessions := Question{ID: "sessions", Question: "Which sessions need investigation?", Status: "cannot_determine", Answer: "Add session-weight sketches to compare session contributors."}
	coverage := Question{ID: "coverage", Question: "Which observations are covered?", Status: "observed", Answer: "Declared intervals are complete; every observed model attempt has both usage fields. Coverage describes the supplied observations."}
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
		volume.Answer = "An expected producer or observation interval is incomplete."
	case a == nil || b == nil || input == nil || output == nil:
		volume.Answer = "Request, token, or missing-usage counters are unavailable."
	case a.Missing != 0 || b.Missing != 0:
		volume.Answer = "Some model attempts lack usage fields. Recorded totals and coverage are shown below."
	case a.Requests == 0 || b.Requests == 0:
		volume.Answer = "At least one window has no observed model attempts; tokens per attempt is undefined."
	default:
		// Validated counters are each at most MaxInt64, so this uint64 sum fits.
		x, y := input.Before+output.Before, input.After+output.After
		n0, n1 := float64(a.Requests), float64(b.Requests)
		p0, p1 := float64(x)/n0, float64(y)/n1
		volume.Status = "observed"
		volume.Answer = "The arithmetic split shares the interaction equally between attempt count and tokens per attempt. Attempts include failures and retries."
		volume.Volume = &VolumeChange{x, y, a.Requests, b.Requests, p0, p1, (n1 - n0) * (p0 + p1) / 2, (p1 - p0) * (n0 + n1) / 2}
	}
	if !r.Complete || a == nil || b == nil || input == nil || output == nil || a.Missing != 0 || b.Missing != 0 {
		coverage.Status = "limited"
		coverage.Answer = "Coverage is incomplete or unknown. Check evidence.before and evidence.after for missing usage and producers."
	}
	source := usageSourceQuestion(r)
	if volume.Volume != nil && source.Status != "observed" {
		volume.Answer += " See provider origin below."
	}
	for _, c := range r.Concentration {
		if c.Name != "top_prompts" {
			continue
		}
		if c.BeforeWeight == 0 && c.AfterWeight == 0 {
			contributors.Answer = "Prompt attribution has no recorded weight in these windows."
			break
		}
		contributors.Status = "observed"
		contributors.Answer = "Shares use recorded prompt weight. Bounds describe tracked candidates; see evidence.concentration for integer counts."
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
	users := Question{ID: "users", Question: "Which tracked users contribute tokens or model attempts?", Status: "cannot_determine", Answer: "Add user-weight sketches to compare user contributors."}
	users = attributionQuestion(users, r, []string{"top_users", "top_users_requests"}, coverage.Status == "limited", cutoff)
	for _, c := range r.Concentration {
		if c.Name == "top_prompts_requests" {
			contributors.Question = "Which tracked prompt contributors changed in tokens or model attempts?"
			contributors = attributionQuestion(contributors, r, []string{"top_prompts", "top_prompts_requests"}, coverage.Status == "limited", cutoff)
			break
		}
	}
	return Investigation{Version: 1, Questions: []Question{volume, contributors, sessions, coverage, users, source, cacheQuestion(r, before, after)}, Evidence: r}, nil
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
			q.Answer = "Attribution needs matching sketches in every input snapshot; some are missing."
		} else {
			for _, c := range r.Concentration {
				if slices.Contains(names, c.Name) {
					q.Answer = "Attribution has no recorded weight in these windows."
				}
			}
		}
		q.Status = "cannot_determine"
		return q
	}
	q.Status = "observed"
	q.Answer = "Shares use attributed tokens or model attempts, excluding activity without a key. Per-key missing-ID coverage is unknown. Bounds describe tracked candidates."
	if q.ID == "sessions" {
		q.Answer += " Flags mark lower-bound shares above the review threshold with complete relevant observations; investigate the flagged sessions."
	}
	if limited {
		q.Status = "limited"
		q.Answer += " Incomplete intervals or token coverage suppress affected flags. Attempt-weight flags need no token data."
	}
	if dropped {
		q.Answer += " Some other attribution is missing from input snapshots."
	}
	return q
}

func usageSourceQuestion(r Report) Question {
	q := Question{ID: "usage_source", Question: "Were these counts reported by the provider?", Status: "cannot_determine", Answer: "Provider origin is unknown for some fields; gateways can supply zeros or estimates. Enable source provenance to distinguish them."}
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
					q.Answer = "Source provenance counts disagree with observed attempts. Check the producer's accounting."
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
	q.Answer = "Some fields are declared inferred or unavailable. The collector excludes declared unavailable counts; see usage_provenance.v1 counters for the breakdown."
	if providerOnly && r.Complete && r.Before.Usage.Missing == 0 && r.After.Usage.Missing == 0 {
		q.Status = "observed"
		q.Answer = "The instrumenter declares all observed input/output counts provider-reported."
	} else if providerOnly {
		q.Answer = "Observed fields are declared provider-reported; producer, interval, or usage coverage is incomplete."
	}
	return q
}

func share(value Interval, total int64) *Share {
	if total == 0 {
		return nil
	}
	return &Share{float64(max(0, value.Lower)) / float64(total), float64(min(total, value.Upper)) / float64(total)}
}
