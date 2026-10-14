// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package clauderecords

import (
	"math/big"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/spend"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

type Measured struct {
	Value   *uint64 `json:"tokens"`
	Records uint64  `json:"covered_records"`
}
type Cache struct {
	Input      Measured `json:"input"`
	Read       Measured `json:"read"`
	Write      Measured `json:"write"`
	Uncached   Measured `json:"uncached_input"`
	ReadShare  *float64 `json:"read_share"`
	WriteShare *float64 `json:"write_share"`
}
type AttributionCoverage struct {
	Records      uint64 `json:"records"`
	TokenRecords uint64 `json:"token_covered_records"`
	CostRecords  uint64 `json:"cost_covered_records"`
}
type Window struct {
	Period                spend.Period                   `json:"period"`
	Records               uint64                         `json:"observed_api_request_records"`
	Input                 Measured                       `json:"inclusive_input"`
	Output                Measured                       `json:"output"`
	Total                 Measured                       `json:"complete_total"`
	Cache                 Cache                          `json:"cache"`
	Cost                  *string                        `json:"recorded_cost_usd"`
	CostRecords           uint64                         `json:"cost_covered_records"`
	CostRepresentations   map[string]uint64              `json:"cost_source_types"`
	DecimalMinusMicros    *string                        `json:"decimal_minus_micros_usd"`
	PairedCosts           uint64                         `json:"paired_cost_records"`
	Unattributed          uint64                         `json:"unattributed_records"`
	IdentityKinds         map[string]uint64              `json:"identity_kinds"`
	IdentityCoverage      map[string]AttributionCoverage `json:"identity_coverage"`
	Quality               map[string]uint64              `json:"quality"`
	ErrorEvents           uint64                         `json:"api_error_events"`
	ErrorStatus           map[string]uint64              `json:"api_error_status_categories"`
	ExhaustedEvents       uint64                         `json:"api_retries_exhausted_events"`
	DuplicateCopies       uint64                         `json:"exact_copies_after_first"`
	PromptRecords         uint64                         `json:"prompt_attributed_records"`
	Prompts               uint64                         `json:"observed_scoped_prompts"`
	RecordsPerPrompt      *float64                       `json:"records_per_scoped_prompt"`
	MultipleRecordPrompts uint64                         `json:"prompts_with_multiple_records"`
	Versions              map[string]uint64              `json:"version_mix"`
	Profiles              map[string]uint64              `json:"capture_profile_mix"`
}
type Model struct {
	Name   string `json:"model"`
	Before Window `json:"before"`
	After  Window `json:"after"`
}
type Decomposition struct {
	RecordContribution float64 `json:"record_count_contribution"`
	SizeContribution   float64 `json:"tokens_per_record_contribution"`
}
type Report struct {
	Schema           string                `json:"schema"`
	Unit             string                `json:"unit"`
	Finality         string                `json:"finality"`
	AccountingID     string                `json:"accounting_contract"`
	Hashing          string                `json:"hashing"`
	GroupBy          string                `json:"group_by"`
	Deduplication    string                `json:"deduplication"`
	PeriodSelection  string                `json:"period_selection"`
	InputFiles       int                   `json:"input_files"`
	ReadRecords      uint64                `json:"read_log_records"`
	IgnoredEvents    uint64                `json:"ignored_events"`
	OutsidePeriods   uint64                `json:"outside_periods"`
	Before           Window                `json:"before"`
	After            Window                `json:"after"`
	Models           []Model               `json:"models"`
	Rankings         compare.Concentration `json:"token_growth"`
	UncachedRankings compare.Concentration `json:"uncached_input_growth"`
	ComparisonChecks []string              `json:"comparison_checks"`
	Decomposition    *Decomposition        `json:"record_decomposition"`
}

type aggregate struct {
	w             Window
	cost          big.Rat
	costDelta     big.Rat
	top, cacheTop *frequentitems.Sketch
}

func newAggregate(p spend.Period, rank bool) *aggregate {
	a := &aggregate{w: Window{Period: p, Quality: map[string]uint64{}, IdentityKinds: map[string]uint64{}, IdentityCoverage: map[string]AttributionCoverage{}, Versions: map[string]uint64{}, Profiles: map[string]uint64{}, CostRepresentations: map[string]uint64{}, ErrorStatus: map[string]uint64{}}}
	if rank {
		a.top, _ = frequentitems.New(frequentitems.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
		a.cacheTop, _ = frequentitems.New(frequentitems.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
	}
	return a
}
func addMeasured(m *Measured, n *uint64) error {
	if n == nil {
		return nil
	}
	var current uint64
	if m.Value != nil {
		current = *m.Value
	}
	sum, err := accounting.AddBounded(current, *n)
	if err != nil {
		return err
	}
	m.Value = &sum
	m.Records++
	return nil
}
func (a *aggregate) add(o observation) error {
	a.w.Records++
	for _, m := range []struct {
		dst *Measured
		n   *uint64
	}{{&a.w.Input, o.input}, {&a.w.Output, o.output}, {&a.w.Total, o.total}} {
		if err := addMeasured(m.dst, m.n); err != nil {
			return err
		}
	}
	// Cache shares use one common covered population, never missing-as-zero.
	if o.input != nil {
		uncached := *o.input - *o.read
		for _, m := range []struct {
			dst *Measured
			n   *uint64
		}{{&a.w.Cache.Input, o.input}, {&a.w.Cache.Read, o.read}, {&a.w.Cache.Write, o.write}, {&a.w.Cache.Uncached, &uncached}} {
			if err := addMeasured(m.dst, m.n); err != nil {
				return err
			}
		}
	}
	if o.cost != nil {
		a.cost.Add(&a.cost, o.cost)
		a.w.CostRecords++
		a.w.CostRepresentations[o.costKind]++
	}
	if o.costDelta != nil {
		a.costDelta.Add(&a.costDelta, o.costDelta)
		a.w.PairedCosts++
	}
	return accounting.MergeCounts(a.w.Quality, o.quality)
}
func (a *aggregate) result() Window {
	w := a.w
	if w.CostRecords > 0 {
		s := accounting.CostText(&a.cost)
		w.Cost = &s
	}
	if w.PairedCosts > 0 {
		s := accounting.CostText(&a.costDelta)
		w.DecimalMinusMicros = &s
	}
	if w.Cache.Input.Value != nil && *w.Cache.Input.Value > 0 {
		r := float64(*w.Cache.Read.Value) / float64(*w.Cache.Input.Value)
		w.Cache.ReadShare = &r
		v := float64(*w.Cache.Write.Value) / float64(*w.Cache.Input.Value)
		w.Cache.WriteShare = &v
	}
	if w.Prompts > 0 {
		n := float64(w.PromptRecords) / float64(w.Prompts)
		w.RecordsPerPrompt = &n
	}
	return w
}
