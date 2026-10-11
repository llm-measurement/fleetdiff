// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import "strings"

// CacheDetail uses a denominator from the same observations as its numerator.
// Nil quantities mean unknown; explicit zero detail remains a measured zero.
type CacheDetail struct {
	CoverageKnown bool              `json:"coverage_known"`
	Issue         string            `json:"issue,omitempty"`
	IssueCounts   map[string]uint64 `json:"issue_counts,omitempty"`
	Reported      uint64            `json:"reported_requests"`
	// Summary quality categories overlap; their sum is not a distinct-request
	// count. Nil counts are unknown, while pointers to zero are measured zeros.
	Covered *uint64 `json:"covered_requests"`
	Missing *uint64 `json:"missing_detail_requests"`
	Invalid *uint64 `json:"invalid_detail_or_input_requests"`
	// RecordedTokens preserves raw summary totals, including invalid subsets.
	// It is not a covered-population numerator.
	RecordedTokens *uint64 `json:"recorded_tokens,omitempty"`
	// Nil means the origin of recorded zeros cannot be reconstructed, not that
	// all zeros are verified. Summaries do not retain this request count.
	ZeroOriginUnknown *uint64  `json:"zero_origin_unknown_requests"`
	Input             *uint64  `json:"covered_input_tokens"`
	Tokens            *uint64  `json:"tokens"`
	Share             *float64 `json:"share_of_recorded_input"`
}

func (d CacheDetail) HasDetail() bool {
	if d.Reported != 0 || d.Invalid != nil && *d.Invalid != 0 || d.RecordedTokens != nil && (!d.CoverageKnown || *d.RecordedTokens != 0) {
		return true
	}
	for name, count := range d.IssueCounts {
		if count != 0 && strings.HasPrefix(name, "token_observations.cache_") &&
			(strings.HasSuffix(name, ".invalid") || strings.HasSuffix(name, ".conflict") || strings.HasSuffix(name, ".subset_violation")) {
			return true
		}
	}
	return false
}

type CacheWindow struct {
	Requests        uint64      `json:"requests"`
	Read            CacheDetail `json:"read"`
	Write           CacheDetail `json:"write"`
	Uncached        *uint64     `json:"uncached_input_tokens"`
	MissingIdentity uint64      `json:"read_covered_requests_without_group_identity"`
}

type CacheView struct {
	Before      CacheWindow    `json:"before"`
	After       CacheWindow    `json:"after"`
	Rankings    *Concentration `json:"uncached_growth_rankings"`
	GroupingGap string         `json:"grouping_gap,omitempty"`
}

func (d *CacheDetail) SetTotals(input, tokens uint64) {
	d.Input, d.Tokens = &input, &tokens
	if input != 0 {
		share := float64(tokens) / float64(input)
		d.Share = &share
	}
}

func (w CacheWindow) HasDetail() bool { return w.Read.HasDetail() || w.Write.HasDetail() }

// CompleteRead is required for a whole-population change statement, not for
// displaying a clearly labeled covered-population measurement.
func (w CacheWindow) CompleteRead() bool {
	return w.Requests > 0 && w.Read.Covered != nil && *w.Read.Covered == w.Requests && w.Uncached != nil && w.Read.Share != nil
}
