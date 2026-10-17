// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import "math/big"

// PricedFailures is a contribution to the covered totals, not an additional cost
// or a final provider receipt. Cache components are disjoint subsets of input.
type PricedFailures struct {
	Records    uint64         `json:"records"`
	Input      *big.Int       `json:"recorded_input_tokens"`
	Output     *big.Int       `json:"recorded_output_tokens"`
	CacheRead  *big.Int       `json:"recorded_cache_read_input_tokens"`
	CacheWrite *big.Int       `json:"recorded_cache_write_input_tokens"`
	Total      *big.Int       `json:"recorded_tokens"`
	Before     *string        `json:"source_cost_usd"`
	Level1     *string        `json:"level1_cost_usd"`
	Level2     []WorkloadCost `json:"level2"`
	Envelope   *Interval      `json:"workload_envelope"`
	UsageBasis string         `json:"usage_basis"`
	Finality   string         `json:"finality"`
}

// Called only after the parent has accepted and priced the record. The child
// shares immutable configuration and hashed selectors, never raw identities.
func (c *Calculator) addFailure(r Record) error {
	if c.failures == nil {
		c.failures = &Calculator{prices: c.prices, factors: c.factors, mapping: c.mapping, secret: c.secret, excluded: map[string]uint64{}}
		for _, m := range c.moves {
			c.failures.moves = append(c.failures.moves, moveState{
				move: m.move, ratio: m.ratio, input: m.input, inputSource: m.inputSource,
				scope: m.scope, buckets: map[string]totals{},
			})
		}
	}
	r.Failed = false
	if err := c.failures.Add(r); err != nil {
		return err
	}
	// The parent does not bound aggregate tokens across unselected traffic or
	// separate move buckets. Exact integers avoid adding a reporting-only limit.
	for i, n := range []*uint64{r.Input, r.Output, r.CacheRead, r.CacheWrite} {
		c.failureTokens[i].Add(&c.failureTokens[i], new(big.Int).SetUint64(*n))
	}
	return nil
}

func (c *Calculator) pricedFailures(level2 []WorkloadCost) *PricedFailures {
	// A move with no failures has zero contribution, not a missing factor. Keep
	// the parent's supported workload variants even when only successes are unmapped.
	subset := *c.failures
	subset.moves = nil
	for _, m := range c.failures.moves {
		if m.covered > 0 {
			subset.moves = append(subset.moves, m)
		}
	}
	variants := make([]string, len(level2))
	for i, w := range level2 {
		variants[i] = w.Workload
	}
	r := subset.result(Source{}, variants)
	return &PricedFailures{
		Records: c.failures.covered,
		Input:   new(big.Int).Set(&c.failureTokens[0]), Output: new(big.Int).Set(&c.failureTokens[1]),
		CacheRead: new(big.Int).Set(&c.failureTokens[2]), CacheWrite: new(big.Int).Set(&c.failureTokens[3]),
		Total:  new(big.Int).Add(&c.failureTokens[0], &c.failureTokens[1]),
		Before: r.Before, Level1: r.Level1, Level2: r.Level2, Envelope: r.Envelope,
		UsageBasis: "gateway-recorded", Finality: "unknown",
	}
}
