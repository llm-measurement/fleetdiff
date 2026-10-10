// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

type Window struct {
	Period             Period            `json:"period"`
	Requests           uint64            `json:"logged_model_requests"`
	Tokens             uint64            `json:"recorded_tokens"`
	Counters           map[string]uint64 `json:"counters"`
	Quality            Quality           `json:"quality"`
	RecordedSpend      string            `json:"recorded_spend_usd"`
	AttributedTokens   uint64            `json:"attributed_tokens,omitempty"`
	Distinct           float64           `json:"distinct_groups_estimate,omitempty"`
	DistinctNominalRSE float64           `json:"distinct_nominal_rse,omitempty"`
}

type Model struct {
	Item   string                `json:"item"`
	Hash   string                `json:"hash,omitempty"`
	Before Window                `json:"before"`
	After  Window                `json:"after"`
	Volume *compare.VolumeChange `json:"recorded_volume_split,omitempty"`
}

type IncreaseShare struct {
	Count int              `json:"tracked_contributors"`
	Delta compare.Interval `json:"recorded_token_delta"`
	Share compare.Share    `json:"share_of_net_recorded_increase"`
}

type Report struct {
	Incomplete     bool                  `json:"comparison_incomplete"`
	CallTypes      []CallType            `json:"call_types"`
	Schema         string                `json:"schema"`
	AccountingID   string                `json:"token_accounting"`
	SourceContract string                `json:"source_contract"`
	GroupBy        string                `json:"group_by"`
	Hashing        string                `json:"hashing"`
	Rows           int                   `json:"input_rows"`
	ExcludedRows   int                   `json:"out_of_scope_rows"`
	OutsidePeriods int                   `json:"outside_period_rows"`
	Before         Window                `json:"before"`
	After          Window                `json:"after"`
	Volume         *compare.VolumeChange `json:"recorded_volume_split,omitempty"`
	Models         []Model               `json:"models"`
	Rankings       compare.Concentration `json:"rankings"`
	Increase       *IncreaseShare        `json:"concentration_of_increase,omitempty"`
	ZeroFilled     string                `json:"zero_filled"`
	ProviderOrigin string                `json:"provider_origin"`
	Notes          []string              `json:"notes"`
}

type CallType struct {
	Name     string         `json:"call_type"`
	Analyzed bool           `json:"analyzed"`
	Before   CallTypeCounts `json:"before"`
	After    CallTypeCounts `json:"after"`
}

// Raw column sums describe coverage only; unsupported mappings are never mixed
// into analyzed token totals. Unknown call-type names receive local aliases.
type CallTypeCounts struct {
	Requests                 uint64 `json:"requests"`
	PromptTokens             uint64 `json:"prompt_tokens"`
	CompletionTokens         uint64 `json:"completion_tokens"`
	TotalTokens              uint64 `json:"total_tokens"`
	PromptTokensOverflow     bool   `json:"prompt_tokens_overflow,omitempty"`
	CompletionTokensOverflow bool   `json:"completion_tokens_overflow,omitempty"`
	TotalTokensOverflow      bool   `json:"total_tokens_overflow,omitempty"`
	InvalidUsage             uint64 `json:"invalid_usage"`
	MissingUsage             uint64 `json:"missing_usage"`
}

// Omit unavailable sums without hiding genuine zero counts in other columns.
func (c CallTypeCounts) MarshalJSON() ([]byte, error) {
	type counts CallTypeCounts
	wire := struct {
		counts
		PromptTokens     *uint64 `json:"prompt_tokens,omitempty"`
		CompletionTokens *uint64 `json:"completion_tokens,omitempty"`
		TotalTokens      *uint64 `json:"total_tokens,omitempty"`
	}{counts: counts(c)}
	if !c.PromptTokensOverflow {
		wire.PromptTokens = &c.PromptTokens
	}
	if !c.CompletionTokensOverflow {
		wire.CompletionTokens = &c.CompletionTokens
	}
	if !c.TotalTokensOverflow {
		wire.TotalTokens = &c.TotalTokens
	}
	return json.Marshal(wire)
}
