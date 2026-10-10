// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import "github.com/llm-measurement/fleetdiff/internal/compare"

type Window struct {
	Period             Period            `json:"period"`
	Requests           uint64            `json:"logged_model_requests"`
	Tokens             uint64            `json:"recorded_tokens"`
	Counters           map[string]uint64 `json:"counters"`
	Quality            Quality           `json:"quality"`
	RecordedSpend      string            `json:"recorded_spend_usd"`
	AttributedTokens   uint64            `json:"attributed_tokens"`
	Distinct           float64           `json:"distinct_groups_estimate"`
	DistinctNominalRSE float64           `json:"distinct_nominal_rse"`
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
