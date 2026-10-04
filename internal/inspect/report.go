// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
)

type Options struct {
	InputFormat string
	Top         int
	ShowHashes  bool
	ShowNames   bool
	SecretEnv   string
}

type Capture struct {
	Files             int    `json:"files"`
	Bytes             int    `json:"bytes"`
	Records           int    `json:"records"`
	Spans             int    `json:"spans"`
	DroppedAttributes uint64 `json:"dropped_attributes"`
}

type Question struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Covered  uint64 `json:"covered_attempts"`
	Total    uint64 `json:"total_attempts"`
	NextStep string `json:"next_step,omitempty"`
}

type Readiness struct {
	Ready     int        `json:"ready"`
	Total     int        `json:"total"`
	Questions []Question `json:"questions"`
}

type Dimension struct {
	Attribute    string  `json:"attribute"`
	NameHidden   bool    `json:"name_hidden,omitempty"`
	Observations uint64  `json:"observations"`
	Estimate     float64 `json:"estimated_distinct_values"`
	NominalRSE   float64 `json:"nominal_rse"`
	LabelRisk    string  `json:"label_risk"`
}

type Item struct {
	Alias      string  `json:"alias"`
	Hash       string  `json:"hash,omitempty"`
	Estimate   int64   `json:"estimate"`
	Lower      int64   `json:"lower_bound"`
	Upper      int64   `json:"upper_bound"`
	LowerShare float64 `json:"lower_share"`
	UpperShare float64 `json:"upper_share"`
}

type Ranking struct {
	Weight     string `json:"weight"`
	Total      int64  `json:"attributed_weight"`
	Candidates int    `json:"candidates"`
	MaxError   int64  `json:"max_error"`
	Items      []Item `json:"items"`
}

type Identity struct {
	Field        string    `json:"field"`
	Present      uint64    `json:"present_attempts"`
	TokenCovered uint64    `json:"token_covered_attempts"`
	Distinct     float64   `json:"estimated_distinct"`
	NominalRSE   float64   `json:"nominal_rse"`
	Rankings     []Ranking `json:"rankings"`
}

type Report struct {
	Schema            string            `json:"schema"`
	AccountingID      string            `json:"accounting_id"`
	Hashing           string            `json:"hashing"`
	Capture           Capture           `json:"capture"`
	Metrics           map[string]uint64 `json:"metrics"`
	ObservedCounters  map[string]uint64 `json:"observed_counters"`
	MetricSaturations []string          `json:"metric_saturations"`
	TokenObservations map[string]uint64 `json:"token_observations"`
	UsageProvenance   map[string]uint64 `json:"usage_provenance"`
	OperationMix      map[string]uint64 `json:"operation_mix"`
	Readiness         Readiness         `json:"readiness"`
	Dimensions        []Dimension       `json:"dimensions"`
	Identities        []Identity        `json:"identities"`
	NextSteps         []string          `json:"next_steps"`
	Notes             []string          `json:"notes"`
}

func (a *analyzer) report(opts Options) Report {
	r := a.result
	for _, state := range a.identities {
		id := Identity{Field: state.field, Present: state.present, TokenCovered: state.tokenCovered, Distinct: state.distinct.Estimate(), NominalRSE: nominalRSE, Rankings: []Ranking{}}
		aliases := map[uint64]string{}
		for _, s := range []*frequentitems.Sketch{state.tokens, state.attempts} {
			items, _ := s.FrequentItems(frequentitems.NoFalseNegatives)
			for _, item := range items {
				if aliases[item.Hash] == "" {
					aliases[item.Hash] = fmt.Sprintf("%s-%d", state.field, len(aliases)+1)
				}
			}
		}
		for _, weighted := range []struct {
			name   string
			sketch *frequentitems.Sketch
		}{{"attempts", state.attempts}, {"tokens", state.tokens}} {
			id.Rankings = append(id.Rankings, rank(weighted.name, weighted.sketch, opts, aliases))
		}
		r.Identities = append(r.Identities, id)
	}
	for _, d := range a.dimensions {
		risk := "review"
		if sensitiveAttribute(d.name) {
			risk = "sensitive_identifier"
		} else if d.sketch.Estimate() >= 1000 {
			risk = "high_cardinality"
		}
		r.Dimensions = append(r.Dimensions, Dimension{Attribute: d.name, NameHidden: d.hidden, Observations: d.observations, Estimate: d.sketch.Estimate(), NominalRSE: nominalRSE, LabelRisk: risk})
	}
	slices.SortFunc(r.Dimensions, func(a, b Dimension) int {
		if a.Estimate > b.Estimate {
			return -1
		}
		if a.Estimate < b.Estimate {
			return 1
		}
		return strings.Compare(a.Attribute, b.Attribute)
	})
	attempts := r.Metrics[metricPrefix+"requests_total"]
	add := func(id string, covered uint64, next string) {
		status := "cannot_determine"
		if covered > 0 {
			status = "partial"
			if covered == attempts {
				status = "ready"
				next = ""
				r.Readiness.Ready++
			}
		}
		r.Readiness.Questions = append(r.Readiness.Questions, Question{id, status, covered, attempts, next})
		if next != "" && !slices.Contains(r.NextSteps, next) {
			r.NextSteps = append(r.NextSteps, next)
		}
	}
	add("model_activity", attempts, "Capture model spans with gen_ai.operation.name or gen_ai.request.model.")
	add("token_usage", a.cleanUsage, "Record both input and output usage; resolve invalid, conflicting, or subset-violating values. Declare unavailable usage instead of filling in zeros.")
	add("usage_origin", a.knownOrigin, "Add usage provenance at the provider/estimator hook. With no declaration, a real zero and a filled-in zero cannot be distinguished.")
	user, session, prompt := a.identities[0], a.identities[1], a.identities[2]
	add("distinct_users", user.present, "Add enduser.id or user.id to model spans or their resource; keep it out of metric labels.")
	add("top_users_by_tokens", user.cleanTokens, "Include a user key and complete, consistent token usage on the same model attempts.")
	add("session_rankings", session.present, "Add gen_ai.conversation.id or session.id to model spans to unlock session rankings by attempts.")
	add("prompt_rankings", prompt.present, "Prompt rankings use gen_ai.request.prompt when present; retain your existing prompt collection policy.")
	r.Readiness.Total = len(r.Readiness.Questions)
	if len(r.Dimensions) > 0 && r.Dimensions[0].NameHidden {
		d := r.Dimensions[0]
		r.NextSteps = append([]string{fmt.Sprintf("%s has ~%.0f distinct values; rerun with --show-names to see which attribute. Review it before using it as a metric label.", d.Attribute, d.Estimate)}, r.NextSteps...)
	}
	r.Notes = []string{
		"Readiness describes fields in this capture, not completeness of traffic. Counts are model attempts; deduplication is off.",
		"Distinct estimates describe this capture. A label's value count is not the total number of Prometheus series; other labels multiply combinations.",
		"Ranking shares use attributed weight. Token rankings require both usage fields; attempt rankings also include missing-usage attempts.",
		"Usage provenance is a declaration, not authenticated provider evidence. Token subsets are not added again.",
		"Hashes are pseudonymous. Default aliases and ephemeral keys do not support identity matching between runs.",
	}
	if opts.ShowNames {
		r.Notes = append(r.Notes, "Custom attribute names are visible because --show-names was requested. Names may contain sensitive information; review before sharing. Values remain hidden.")
	}
	if r.Capture.DroppedAttributes > 0 {
		r.Notes = append(r.Notes, fmt.Sprintf("The capture declares %d dropped attributes; readiness covers retained attributes only.", r.Capture.DroppedAttributes))
	}
	r.ObservedCounters = r.Metrics
	r.Metrics = maps.Clone(r.ObservedCounters)
	r.MetricSaturations = []string{}
	for name, count := range r.Metrics {
		if count > math.MaxInt64 {
			r.Metrics[name] = math.MaxInt64
			r.MetricSaturations = append(r.MetricSaturations, name)
		}
	}
	slices.Sort(r.MetricSaturations)
	if len(r.MetricSaturations) > 0 {
		r.Notes = append(r.Notes, "Some collector metric samples saturate at signed 64-bit maximum; observed_counters retains exact unsigned totals for this capture.")
	}
	return r
}

func rank(weight string, s *frequentitems.Sketch, opts Options, aliases map[uint64]string) Ranking {
	items, _ := s.FrequentItems(frequentitems.NoFalseNegatives)
	r := Ranking{Weight: weight, Total: s.TotalWeight(), Candidates: len(items), MaxError: s.MaxError(), Items: []Item{}}
	for _, item := range items[:min(len(items), opts.Top)] {
		x := Item{Alias: aliases[item.Hash], Estimate: item.Estimate, Lower: item.LowerBound, Upper: item.UpperBound}
		if opts.ShowHashes {
			x.Hash = fmt.Sprintf("%016x", item.Hash)
		}
		if r.Total > 0 {
			x.LowerShare = float64(x.Lower) / float64(r.Total)
			x.UpperShare = min(1, float64(x.Upper)/float64(r.Total))
		}
		r.Items = append(r.Items, x)
	}
	return r
}
