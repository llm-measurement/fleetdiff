// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package compare composes sketchkit summaries into a read-only window report.
package compare

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

type Options struct {
	Expected     []string
	Top          int
	AllowPartial bool
	ShowHashes   bool
	FlagShare    float64
	// FlagShareSet distinguishes an explicit zero threshold from the default.
	FlagShareSet bool
}

type Interval struct {
	Lower int64 `json:"lower"`
	Upper int64 `json:"upper"`
}

type Counter struct {
	Name   string `json:"name"`
	Unit   string `json:"unit"`
	Before uint64 `json:"before"`
	After  uint64 `json:"after"`
	Delta  int64  `json:"delta"`
}

type Estimate struct {
	Estimate   float64 `json:"estimate"`
	NominalRSE float64 `json:"nominal_relative_standard_error"`
}

type Distinct struct {
	Name           string   `json:"name"`
	Before         Estimate `json:"before"`
	After          Estimate `json:"after"`
	EstimatedDelta float64  `json:"estimated_delta"`
}

type Mover struct {
	Item      string   `json:"item"`
	Hash      string   `json:"hash,omitempty"`
	Before    Interval `json:"before"`
	After     Interval `json:"after"`
	Delta     Interval `json:"delta"`
	Direction string   `json:"direction"`
}

type Concentration struct {
	Name           string   `json:"name"`
	WeightUnit     string   `json:"weight_unit"`
	BeforeWeight   int64    `json:"before_weight"`
	AfterWeight    int64    `json:"after_weight"`
	BeforeMaxError int64    `json:"before_max_error"`
	AfterMaxError  int64    `json:"after_max_error"`
	CandidateCount int      `json:"candidate_count"`
	UntrackedDelta Interval `json:"untracked_delta_bound"`
	Movers         []Mover  `json:"tracked_movers"`
}

type UsageCoverage struct {
	Requests uint64 `json:"requests"`
	Missing  uint64 `json:"missing_usage_requests"`
	Complete uint64 `json:"complete_usage_requests"`
}

type Window struct {
	Start             int64          `json:"start_unix_nano"`
	Duration          int64          `json:"duration_nano"`
	SelectedSnapshots int            `json:"selected_snapshots"`
	MissingProducers  []string       `json:"missing_producers"`
	PartialProducers  []string       `json:"partial_producers"`
	Usage             *UsageCoverage `json:"token_coverage,omitempty"`
}

type Report struct {
	Version             int             `json:"version"`
	Complete            bool            `json:"complete_observation_intervals"`
	Before              Window          `json:"before"`
	After               Window          `json:"after"`
	Counters            []Counter       `json:"counters"`
	Distinct            []Distinct      `json:"distinct"`
	Concentration       []Concentration `json:"concentration"`
	OmittedMeasurements int             `json:"omitted_measurements"`
	DroppedMeasurements []string        `json:"dropped_measurements,omitempty"`
	Notes               []string        `json:"notes"`
}

// Only reviewed measurement names are copied into output. Input metadata and
// arbitrary counter/sketch names can carry sensitive data even when valid JSON.
var counterUnits = map[string]string{
	"requests": "observed-model-spans", "agent_runs": "observed-root-agent-spans",
	"input_tokens": "reported-tokens", "output_tokens": "reported-tokens",
	"cache_read_input_tokens": "reported-input-subset", "cache_write_input_tokens": "reported-input-subset",
	"reasoning_output_tokens": "reported-output-subset", "missing_token_usage": "observed-model-spans",
	"dedup_suppressed": "suppressed-observations", "dedup_key_missing": "observations",
	"usage_provenance.v1.input.provider_reported": "observations", "usage_provenance.v1.output.provider_reported": "observations",
	"usage_provenance.v1.input.inferred": "observations", "usage_provenance.v1.output.inferred": "observations",
	"usage_provenance.v1.input.unavailable": "observations", "usage_provenance.v1.output.unavailable": "observations",
	"usage_provenance.v1.input.unknown": "observations", "usage_provenance.v1.output.unknown": "observations",
	"token_observations.input.reported": "observations", "token_observations.cache_read_input.reported": "observations",
	"token_observations.input.missing": "observations", "token_observations.cache_read_input.missing": "observations",
	"token_observations.input.invalid": "observations", "token_observations.cache_read_input.invalid": "observations",
	"token_observations.input.conflict": "observations", "token_observations.cache_read_input.conflict": "observations",
	"token_observations.input.subset_violation": "observations", "token_observations.cache_read_input.subset_violation": "observations",
}

var sketchKinds = map[string]string{
	"distinct_users": "hllpp", "distinct_prompts": "hllpp", "distinct_docs": "hllpp",
	"distinct_mcp_sessions": "hllpp", "distinct_mcp_methods": "hllpp", "distinct_mcp_resources": "hllpp",
	"top_prompts": "frequent_items", "top_tool_errors": "frequent_items",
}

// Compare returns no report on error and does not mutate input. Expected producers
// declare disjoint event ownership. Neither that assertion nor identity is authenticated.
func Compare(before, after []summary.Envelope, options Options) (Report, error) {
	if _, err := flagShare(options); err != nil {
		return Report{}, err
	}
	if options.Top < 1 || options.Top > 100 {
		return Report{}, errors.New("top must be between 1 and 100")
	}
	if len(before) == 0 || len(after) == 0 {
		return Report{}, errors.New("each selected window needs at least one snapshot; missing data is not zero")
	}
	if len(before) > MaxFiles || len(after) > MaxFiles {
		return Report{}, errors.New("invalid summary batch size")
	}
	if err := validateWindow(before); err != nil {
		return Report{}, errors.New("before window: " + summaryCause(err))
	}
	if err := validateWindow(after); err != nil {
		return Report{}, errors.New("after window: " + summaryCause(err))
	}
	before, after, dropped, err := projectTopK(before, after)
	if err != nil {
		return Report{}, errors.New("windows: " + summaryCause(err))
	}
	if hasUsageProvenance(before) || hasUsageProvenance(after) {
		var err error
		before, err = withUsageProvenance(before)
		if err != nil {
			return Report{}, errors.New("before window: " + summaryCause(err))
		}
		after, err = withUsageProvenance(after)
		if err != nil {
			return Report{}, errors.New("after window: " + summaryCause(err))
		}
	}
	a, err := summary.Combine(before, options.Expected)
	if err != nil {
		return Report{}, errors.New("before window: " + summaryCause(err))
	}
	b, err := summary.Combine(after, options.Expected)
	if err != nil {
		return Report{}, errors.New("after window: " + summaryCause(err))
	}
	if err := summary.Compatible(before[0], after[0]); err != nil {
		return Report{}, errors.New("windows: " + summaryCause(err))
	}
	if before[0].WindowStart+before[0].WindowDuration > after[0].WindowStart {
		return Report{}, errors.New("before window must end at or before the after window starts")
	}
	complete := len(a.Missing)+len(a.Partial)+len(b.Missing)+len(b.Partial) == 0
	if !complete && !options.AllowPartial {
		return Report{}, errors.New("missing producers or partial observation intervals; use --allow-partial to report only observed differences")
	}
	aw, err := windowReport(before[0], a, options.Expected)
	if err != nil {
		return Report{}, err
	}
	bw, err := windowReport(after[0], b, options.Expected)
	if err != nil {
		return Report{}, err
	}
	r := Report{Version: 1, Complete: complete, Before: aw, After: bw, Counters: []Counter{}, Distinct: []Distinct{}, Concentration: []Concentration{}, Notes: []string{
		"Counts describe supplied observations. Operators establish producer identity, disjoint traffic, and upstream delivery.",
		"Distinct-count RSE is statistical; tracked-item bounds are deterministic for the recorded weight.",
		"Candidates come from both windows, ordered by largest absolute delta-bound endpoint. Aliases are local to this report.",
	}}
	r.DroppedMeasurements = dropped
	r.OmittedMeasurements = len(dropped)
	if len(dropped) != 0 {
		r.Notes = append(r.Notes, "Optional attribution needs matching sketches in every input snapshot; missing measurements are listed above.")
	}
	if !complete {
		r.Notes = append(r.Notes, "PARTIAL: differences may reflect missing observations rather than a workload change.")
	}
	for _, name := range slices.Sorted(maps.Keys(a.Counters)) {
		if isTopKMarker(name) {
			continue // Contract metadata, not an additive quantity.
		}
		unit, ok := counterUnits[name]
		if !ok {
			r.OmittedMeasurements++
			continue
		}
		x, y := a.Counters[name], b.Counters[name]
		r.Counters = append(r.Counters, Counter{Name: name, Unit: unit, Before: x, After: y, Delta: int64(y) - int64(x)})
	}
	for _, name := range slices.Sorted(maps.Keys(a.Sketches)) {
		kind, ok := sketchKinds[name]
		if _, topK := topKUnits[name]; topK {
			kind, ok = "frequent_items", true
		}
		if !ok {
			r.OmittedMeasurements++
			continue
		}
		if a.Sketches[name].Kind != kind {
			return Report{}, errors.New("known measurement has an unexpected sketch kind")
		}
		switch kind {
		case "hllpp":
			x, err := hllpp.Parse(a.Sketches[name].Data)
			if err != nil {
				return Report{}, errors.New("invalid distinct sketch")
			}
			y, err := hllpp.Parse(b.Sketches[name].Data)
			if err != nil {
				return Report{}, errors.New("invalid distinct sketch")
			}
			r.Distinct = append(r.Distinct, Distinct{Name: name, Before: estimate(x), After: estimate(y), EstimatedDelta: y.Estimate() - x.Estimate()})
		case "frequent_items":
			c, err := concentration(name, a.Sketches[name], b.Sketches[name], options)
			if err != nil {
				return Report{}, err
			}
			r.Concentration = append(r.Concentration, c)
		}
	}
	return r, nil
}

// Validate original snapshots before either optional-extension normalization.
// Replay identity must also survive projection: dropping a sketch cannot turn a
// conflicting sequence into a byte-identical duplicate.
func validateWindow(input []summary.Envelope) error {
	if len(input) > MaxFiles {
		return errors.New("invalid summary batch size")
	}
	remaining := MaxInputBytes
	encodedRemaining := MaxInputBytes
	type snapshotID struct {
		producer, epoch string
		start           int64
		sequence        uint64
	}
	seen := map[snapshotID][32]byte{}
	for _, envelope := range input {
		if len(envelope.Sketches) > 16 {
			return errors.New("invalid summary payload count")
		}
		for _, payload := range envelope.Sketches {
			if len(payload.Data) > remaining {
				return errors.New("summary batch exceeds size limit")
			}
			remaining -= len(payload.Data)
			if payload.Kind != "frequent_items" {
				continue
			}
			// Check every snapshot before merging, including superseded snapshots
			// and measurements omitted from the report. Never sum untrusted bounds.
			s, err := frequentitems.Parse(payload.Data)
			if err != nil {
				return errors.New("invalid summary sketch state")
			}
			items, err := s.FrequentItems(frequentitems.NoFalseNegatives)
			if err != nil {
				return errors.New("invalid summary sketch state")
			}
			unassigned := s.TotalWeight()
			for _, item := range items {
				if item.UpperBound > s.TotalWeight() || item.LowerBound > unassigned {
					return errors.New("inconsistent frequent-items total")
				}
				unassigned -= item.LowerBound
			}
			if s.MaxError() == 0 && unassigned != 0 {
				return errors.New("inconsistent frequent-items total")
			}
		}
		encoded, err := envelope.MarshalBinary()
		if err != nil {
			return err
		}
		if len(encoded) > encodedRemaining {
			return errors.New("summary batch exceeds size limit")
		}
		encodedRemaining -= len(encoded)
		if err := validateTopK(envelope); err != nil {
			return err
		}
		if requests, ok := envelope.Counters["requests"]; ok && envelope.Counters["missing_token_usage"] > requests {
			return errors.New("missing-token request count exceeds observed model requests")
		}
		id := snapshotID{envelope.ProducerID, envelope.Epoch, envelope.WindowStart, envelope.Sequence}
		digest := sha256.Sum256(encoded)
		if previous, ok := seen[id]; ok && previous != digest {
			return errors.New("conflicting summary sequence")
		}
		seen[id] = digest
	}
	return nil
}

// Match complete, reviewed messages only. New or decorated dependency errors may
// contain input values; never echo them or use substring matching here.
func summaryCause(err error) string {
	message := err.Error()
	switch message {
	case "invalid summary version or sequence", "invalid summary identifier",
		"invalid summary observation interval", "invalid summary payload count",
		"invalid summary counter", "invalid summary sketch", "invalid summary sketch state",
		"summary exceeds size limit", "invalid summary batch size",
		"invalid expected producer", "duplicate expected producer",
		"summary batch exceeds size limit", "unexpected summary producer",
		"cannot combine different windows", "incompatible summary measurement contract",
		"incompatible summary sketch metadata", "conflicting summary sequence",
		"summary observation regressed", "summary counter regressed",
		"combined counter overflow", "overlapping producer epochs", "inconsistent frequent-items total",
		"invalid top-k contract marker", "missing top-k contract marker", "known measurement has an unexpected sketch kind",
		"missing-token request count exceeds observed model requests":
		return message
	default:
		return "invalid, incompatible, or conflicting snapshots; check producers, scope, keys, accounting, replay, and input limits"
	}
}

func windowReport(e summary.Envelope, result summary.Result, expected []string) (Window, error) {
	ids := slices.Clone(expected)
	slices.Sort(ids)
	aliases := func(names []string) []string {
		out := make([]string, 0, len(names))
		for _, id := range ids {
			if slices.Contains(names, id) {
				out = append(out, fmt.Sprintf("producer-%d", slices.Index(ids, id)+1))
			}
		}
		return out
	}
	w := Window{Start: e.WindowStart, Duration: e.WindowDuration, SelectedSnapshots: len(result.Sources), MissingProducers: aliases(result.Missing), PartialProducers: aliases(result.Partial)}
	requests, hasRequests := result.Counters["requests"]
	missing, hasMissing := result.Counters["missing_token_usage"]
	if hasRequests && hasMissing {
		if missing > requests {
			return Window{}, errors.New("missing-token request count exceeds observed model requests")
		}
		w.Usage = &UsageCoverage{Requests: requests, Missing: missing, Complete: requests - missing}
	}
	return w, nil
}

func estimate(s *hllpp.Sketch) Estimate {
	// sketchkit v0.2.0 stores one byte per dense register, so bytes equal 2^p.
	// Profile tests pin this assumption. Use the nominal dense scale even when sparse.
	registers := s.DenseRegisterBytes()
	return Estimate{Estimate: s.Estimate(), NominalRSE: 1.04 / math.Sqrt(float64(registers))}
}

func concentration(name string, a, b summary.Payload, options Options) (Concentration, error) {
	x, err := frequentitems.Parse(a.Data)
	if err != nil {
		return Concentration{}, errors.New("invalid frequent-items sketch")
	}
	y, err := frequentitems.Parse(b.Data)
	if err != nil {
		return Concentration{}, errors.New("invalid frequent-items sketch")
	}
	left, err := x.FrequentItems(frequentitems.NoFalseNegatives)
	if err != nil {
		return Concentration{}, errors.New("cannot query frequent items")
	}
	right, err := y.FrequentItems(frequentitems.NoFalseNegatives)
	if err != nil {
		return Concentration{}, errors.New("cannot query frequent items")
	}
	candidates := map[uint64]bool{}
	for _, items := range [][]frequentitems.Item{left, right} {
		for _, item := range items {
			candidates[item.Hash] = true
		}
	}
	type candidate struct {
		key   uint64
		mover Mover
	}
	items := make([]candidate, 0, len(candidates))
	for key := range candidates {
		prior := Interval{x.LowerBoundHash(key), x.UpperBoundHash(key)}
		next := Interval{y.LowerBoundHash(key), y.UpperBoundHash(key)}
		// Each window has nonnegative bounded weights; endpoint subtraction is
		// valid even when the key was untracked in one window.
		delta := Interval{next.Lower - prior.Upper, next.Upper - prior.Lower}
		direction := "uncertain"
		switch {
		case delta.Lower > 0:
			direction = "increased"
		case delta.Upper < 0:
			direction = "decreased"
		case delta.Lower == 0 && delta.Upper == 0:
			direction = "unchanged"
		}
		items = append(items, candidate{key: key, mover: Mover{Before: prior, After: next, Delta: delta, Direction: direction}})
	}
	strength := func(i Interval) int64 { return max(abs(i.Lower), abs(i.Upper)) }
	sort.Slice(items, func(i, j int) bool {
		a, b := strength(items[i].mover.Delta), strength(items[j].mover.Delta)
		if a != b {
			return a > b
		}
		return items[i].key < items[j].key
	})
	c := Concentration{Name: name, WeightUnit: "configured-weight", BeforeWeight: x.TotalWeight(), AfterWeight: y.TotalWeight(), BeforeMaxError: x.MaxError(), AfterMaxError: y.MaxError(), CandidateCount: len(items), UntrackedDelta: Interval{-x.MaxError(), y.MaxError()}, Movers: []Mover{}}
	if unit, ok := topKUnits[name]; ok {
		c.WeightUnit = unit
	}
	for i, item := range items[:min(options.Top, len(items))] {
		m := item.mover
		m.Item = fmt.Sprintf("item-%d", i+1)
		if options.ShowHashes {
			m.Hash = fmt.Sprintf("%016x", item.key)
		}
		c.Movers = append(c.Movers, m)
	}
	return c, nil
}

func abs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
