// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/attribution"
)

type Interval struct {
	Mean string `json:"central_usd,omitempty"`
	Low  string `json:"lower_usd"`
	High string `json:"upper_usd"`
}

type WorkloadCost struct {
	Workload string `json:"workload"`
	Interval
}

type MoveReport struct {
	Item          string         `json:"item"`
	From          string         `json:"from"`
	To            string         `json:"to"`
	Fraction      string         `json:"fraction"`
	Scoped        bool           `json:"scoped"`
	Covered       uint64         `json:"covered_observations"`
	Excluded      uint64         `json:"excluded_observations"`
	InputFactor   string         `json:"input_tokenizer_factor"`
	InputSource   string         `json:"input_factor_source"`
	Before        *string        `json:"moved_source_cost_usd"`
	Level1        *string        `json:"moved_level1_cost_usd"`
	OutputShare   *string        `json:"level1_moved_output_cost_share"`
	Level2        []WorkloadCost `json:"level2"`
	FactorSources []string       `json:"factor_sources"`
	Gap           string         `json:"gap,omitempty"`
}

type Report struct {
	Schema        string            `json:"schema"`
	Source        Source            `json:"source"`
	PriceDate     string            `json:"price_date"`
	PriceSource   string            `json:"price_source"`
	PriceVersion  int               `json:"price_version"`
	FactorVersion int               `json:"factor_version"`
	FactorDate    string            `json:"factor_date"`
	Covered       uint64            `json:"covered_observations"`
	Excluded      map[string]uint64 `json:"excluded_observations"`
	Unmapped      uint64            `json:"unmapped_moved_observations"`
	Before        *string           `json:"covered_source_cost_usd"`
	Level1        *string           `json:"covered_level1_cost_usd"`
	Level2        []WorkloadCost    `json:"level2"`
	Envelope      *Interval         `json:"workload_envelope"`
	Moves         []MoveReport      `json:"moves"`
	Dominant      string            `json:"dominant_uncertainty"`
	Assumptions   []string          `json:"assumptions"`
	Limits        []string          `json:"limits"`
}

type totals struct{ input, output, read, write uint64 }
type moveState struct {
	move              Move
	ratio             *big.Rat
	input             *big.Rat
	inputSource       string
	buckets           map[string]totals
	covered, excluded uint64
	scope             *hashedSelector
}
type hashedSelector struct {
	kind   string
	digest [32]byte
}
type Calculator struct {
	prices            Prices
	factors           Factors
	mapping           map[hashedSelector]string
	secret            []byte
	moves             []moveState
	baseline          big.Rat
	covered, unmapped uint64
	excluded          map[string]uint64
}

func New(prices Prices, factors Factors, scenario Scenario, mapping WorkloadMap) (*Calculator, error) {
	if err := validatePrices(prices); err != nil {
		return nil, err
	}
	if err := validateScenario(scenario, mapping); err != nil {
		return nil, err
	}
	if factors.Version != 1 || !validDate(factors.Date) {
		return nil, errors.New("invalid measured factor version or date")
	}
	if err := validateFactors(factors); err != nil {
		return nil, err
	}
	c := &Calculator{prices: prices, factors: factors, mapping: map[hashedSelector]string{}, excluded: map[string]uint64{}}
	for _, move := range scenario.Moves {
		from, to := c.price(move.From), c.price(move.To)
		if from == nil || to == nil {
			return nil, errors.New("move names an unknown model or price; supply prices for both known models, no rates were guessed")
		}
		ratio, _ := fraction(move.Fraction)
		input := big.NewRat(1, 1)
		source := "within-provider assumption: 1.0"
		if from.Provider != to.Provider {
			found := false
			for _, f := range factors.InputPairs {
				if f.From == move.From && f.To == move.To {
					input, _ = decimal(f.Factor)
					source = f.Source
					found = true
					break
				}
			}
			if !found {
				return nil, errors.New("no measured cross-provider input tokenizer factor for this move; no factor was extrapolated")
			}
		}
		c.moves = append(c.moves, moveState{move: move, ratio: ratio, input: input, inputSource: source, buckets: map[string]totals{}})
	}
	var err error
	c.secret, _, err = attribution.Secret("")
	if err != nil {
		return nil, err
	}
	for i := range c.moves {
		m := &c.moves[i]
		if m.move.Scope != nil {
			s, err := c.hashSelector(*m.move.Scope)
			if err != nil {
				c.Close()
				return nil, err
			}
			m.scope = &s
			m.move.Scope = nil
		}
	}
	for _, m := range mapping.Mappings {
		s, err := c.hashSelector(m.Selector)
		if err != nil {
			c.Close()
			return nil, err
		}
		if previous, ok := c.mapping[s]; ok && previous != m.Workload {
			c.Close()
			return nil, errors.New("canonical workload selectors conflict")
		}
		c.mapping[s] = m.Workload
	}
	return c, nil
}

func (c *Calculator) Close() { clear(c.secret) }
func (c *Calculator) hashSelector(s Selector) (hashedSelector, error) {
	digest, err := attribution.Identity(c.secret, "litellm-spend/v1", s.Kind, s.Value)
	if err != nil {
		return hashedSelector{}, errors.New("cannot canonicalize what-if identity selector")
	}
	return hashedSelector{s.Kind, digest}, nil
}

func validateFactors(f Factors) error {
	seen := map[string]bool{}
	for _, p := range f.InputPairs {
		key := p.From + "\x00" + p.To
		r, ok := decimal(p.Factor)
		if !ok || r.Sign() <= 0 || p.N <= 0 || p.Source == "" || seen[key] {
			return errors.New("invalid measured input factor")
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for _, p := range f.Pairs {
		key := p.From + "\x00" + p.To + "\x00" + p.Workload
		m, a := decimal(p.Output)
		lo, b := decimal(p.Low)
		hi, d := decimal(p.High)
		if !a || !b || !d || m.Sign() <= 0 || lo.Sign() <= 0 || lo.Cmp(m) > 0 || m.Cmp(hi) > 0 || p.N <= 0 || p.Source == "" || !slices.Contains(Workloads, p.Workload) || seen[key] {
			return errors.New("invalid measured output mean interval")
		}
		seen[key] = true
	}
	return nil
}

func (c *Calculator) price(id string) *Price {
	for i := range c.prices.Models {
		if c.prices.Models[i].ID == id {
			return &c.prices.Models[i]
		}
	}
	return nil
}

// Add consumes the existing readers' validated records. Fractions select a
// uniform share of each original cohort, never the output of an earlier move.
func (c *Calculator) Add(r Record) error {
	if r.Count == 0 {
		return errors.New("what-if observation count must be positive")
	}
	indices := []int{}
	unknownScope := false
	sum := new(big.Rat)
	identities := map[hashedSelector]bool{}
	for _, s := range []Selector{{"key", r.Key}, {"team", r.Team}} {
		if s.Value != "" {
			h, err := c.hashSelector(s)
			if err != nil {
				return err
			}
			identities[h] = true
		}
	}
	for i := range c.moves {
		m := &c.moves[i]
		if m.move.From == r.Model && m.scope != nil && (m.scope.kind == "key" && strings.TrimSpace(r.Key) == "" || m.scope.kind == "team" && strings.TrimSpace(r.Team) == "") {
			unknownScope = true
			indices = append(indices, i)
			continue
		}
		if m.move.From == r.Model && (m.scope == nil || identities[*m.scope]) {
			indices = append(indices, i)
			sum.Add(sum, m.ratio)
		}
	}
	if !unknownScope && sum.Cmp(big.NewRat(1, 1)) > 0 {
		return errors.New("overlapping moves exceed 100% of an original traffic cohort")
	}
	workload := ""
	if len(indices) > 0 {
		for h := range identities {
			if mapped, ok := c.mapping[h]; ok {
				if workload != "" && workload != mapped {
					return errors.New("key and team workload mappings conflict for selected traffic")
				}
				workload = mapped
			}
		}
	}
	issue := r.Issue
	if issue != "" {
		issue = "unusable_usage"
	}
	if r.Input == nil || r.Output == nil || r.CacheRead == nil || r.CacheWrite == nil {
		issue = "missing_split"
	}
	if c.price(r.Model) == nil {
		issue = "unknown_model_price"
	}
	if p := c.price(r.Model); p != nil && r.Input != nil && p.InputTierMax > 0 && *r.Input > p.InputTierMax {
		issue = "outside_price_tier"
	}
	if r.Issue == "unsupported_call_type" {
		issue = "unsupported_call_type"
	}
	if issue == "" && unknownScope {
		issue = "missing_scope_identity"
	}
	if issue == "" && (*r.CacheRead > *r.Input || *r.CacheWrite > *r.Input-*r.CacheRead) {
		issue = "invalid_cache_partition"
	}
	if issue != "" {
		if err := addCount(&c.excluded, issue, r.Count); err != nil {
			return err
		}
		for _, i := range indices {
			n, err := accounting.AddBounded(c.moves[i].excluded, r.Count)
			if err != nil {
				return err
			}
			c.moves[i].excluded = n
		}
		return nil
	}
	for _, i := range indices {
		m := &c.moves[i]
		p := c.price(m.move.To)
		if p.InputTierMax > 0 {
			input := new(big.Rat).SetInt(new(big.Int).SetUint64(*r.Input))
			input.Mul(input, m.input)
			limit := new(big.Rat).SetInt(new(big.Int).SetUint64(p.InputTierMax))
			if input.Cmp(limit) > 0 {
				return errors.New("a move exceeds the target's priced input tier; supply an applicable price table or select a narrower export")
			}
		}
	}
	n, err := accounting.AddBounded(c.covered, r.Count)
	if err != nil {
		return err
	}
	c.covered = n
	t := totals{*r.Input, *r.Output, *r.CacheRead, *r.CacheWrite}
	base, _ := cost(t, *c.price(r.Model), big.NewRat(1, 1), big.NewRat(1, 1))
	c.baseline.Add(&c.baseline, base)
	if len(indices) > 0 && workload == "" {
		n, err := accounting.AddBounded(c.unmapped, r.Count)
		if err != nil {
			return err
		}
		c.unmapped = n
	}
	for _, i := range indices {
		m := &c.moves[i]
		v := m.buckets[workload]
		for _, p := range []struct {
			dst *uint64
			src uint64
		}{{&v.input, t.input}, {&v.output, t.output}, {&v.read, t.read}, {&v.write, t.write}} {
			n, err := accounting.AddBounded(*p.dst, p.src)
			if err != nil {
				return err
			}
			*p.dst = n
		}
		m.buckets[workload] = v
		n, err := accounting.AddBounded(m.covered, r.Count)
		if err != nil {
			return err
		}
		m.covered = n
	}
	return nil
}

func addCount(m *map[string]uint64, k string, n uint64) error {
	v, err := accounting.AddBounded((*m)[k], n)
	if err == nil {
		(*m)[k] = v
	}
	return err
}
func rat(s string) *big.Rat  { r, _ := decimal(s); return r }
func text(r *big.Rat) string { return r.FloatString(9) }
func ptr(s string) *string   { return &s }

func cost(t totals, p Price, input, output *big.Rat) (*big.Rat, *big.Rat) {
	r := new(big.Rat)
	for _, v := range []struct {
		n uint64
		p string
	}{{t.input - t.read - t.write, p.Input}, {t.read, p.CacheRead}, {t.write, p.CacheWrite}} {
		term := new(big.Rat).SetInt(new(big.Int).SetUint64(v.n))
		term.Mul(term, rat(v.p))
		r.Add(r, term)
	}
	r.Mul(r, input)
	o := new(big.Rat).SetInt(new(big.Int).SetUint64(t.output))
	o.Mul(o, rat(p.Output))
	o.Mul(o, output)
	r.Add(r, o)
	r.Quo(r, big.NewRat(1000000, 1))
	o.Quo(o, big.NewRat(1000000, 1))
	return r, o
}

func (c *Calculator) outputFactor(from, to, workload string) *Factor {
	for i := range c.factors.Pairs {
		p := &c.factors.Pairs[i]
		if p.From == from && p.To == to && p.Workload == workload {
			return p
		}
	}
	return nil
}

func (c *Calculator) Result(source Source) Report {
	r := Report{Schema: "fleetdiff-what-if/v1", Source: source, PriceDate: c.prices.Date, FactorDate: c.factors.Date, Covered: c.covered, Excluded: c.excluded, Unmapped: c.unmapped, Moves: []MoveReport{}, Level2: []WorkloadCost{},
		Dominant:    "How output verbosity on your traffic differs from the measured workload averages.",
		Assumptions: []string{"Fractions move a uniform share of original covered token volume, with the same work and observed cache-read/write shares. Moves do not cascade.", "Level 1 keeps output tokens unchanged, including billed reasoning. Level 2 uses the study's geometric mean paired output ratios.", "Baseline and scenarios use the dated price table, not recorded spend. Cache reads and writes are disjoint input subsets; cache-write pricing assumes the measured short-lived cache class.", "Level 2 endpoints propagate approximate 95% log-mean intervals for geometric mean ratios; combined endpoints and the workload envelope are not joint confidence or prediction intervals."},
		Limits:      []string{"Factors are averages on public benchmark tasks; customer traffic may differ.", "Individual requests vary widely; the per-request range test failed.", "This re-pricing is not a forecast or an invoice.", "No quality equivalence is claimed."}}
	r.PriceSource = "bundled"
	r.PriceVersion = c.prices.Version
	r.FactorVersion = c.factors.Version
	if c.covered == 0 {
		r.Dominant = "No covered usage can be priced; missing splits and source gaps remain unknown."
		for i, m := range c.moves {
			r.Moves = append(r.Moves, MoveReport{Item: fmt.Sprintf("move-%d", i+1), From: m.move.From, To: m.move.To, Fraction: m.move.Fraction, Scoped: m.scope != nil, Excluded: m.excluded, InputFactor: text(m.input), InputSource: m.inputSource, Level2: []WorkloadCost{}, FactorSources: []string{}, Gap: "no covered matching usage"})
		}
		return r
	}
	r.Before = ptr(text(&c.baseline))
	l1 := new(big.Rat).Set(&c.baseline)
	type triple struct{ m, l, h *big.Rat }
	totals2 := map[string]triple{}
	variants := slices.Clone(Workloads)
	if c.unmapped == 0 {
		variants = []string{"mapped"}
	}
	for _, w := range variants {
		totals2[w] = triple{new(big.Rat).Set(&c.baseline), new(big.Rat).Set(&c.baseline), new(big.Rat).Set(&c.baseline)}
	}
	allL2 := true
	for _, m := range c.moves {
		mr := MoveReport{Item: fmt.Sprintf("move-%d", len(r.Moves)+1), From: m.move.From, To: m.move.To, Fraction: m.move.Fraction, Scoped: m.scope != nil, Covered: m.covered, Excluded: m.excluded, InputFactor: text(m.input), InputSource: m.inputSource, Level2: []WorkloadCost{}, FactorSources: []string{}}
		if m.covered == 0 {
			mr.Gap = "no covered matching usage"
			r.Moves = append(r.Moves, mr)
			allL2 = false
			continue
		}
		before, next, out := new(big.Rat), new(big.Rat), new(big.Rat)
		keys := make([]string, 0, len(m.buckets))
		for k := range m.buckets {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, w := range keys {
			t := m.buckets[w]
			b, _ := cost(t, *c.price(m.move.From), big.NewRat(1, 1), big.NewRat(1, 1))
			n, o := cost(t, *c.price(m.move.To), m.input, big.NewRat(1, 1))
			before.Add(before, b)
			next.Add(next, n)
			out.Add(out, o)
		}
		before.Mul(before, m.ratio)
		next.Mul(next, m.ratio)
		out.Mul(out, m.ratio)
		mr.Before = ptr(text(before))
		mr.Level1 = ptr(text(next))
		if next.Sign() > 0 {
			mr.OutputShare = ptr(text(new(big.Rat).Quo(out, next)))
		}
		l1.Add(l1, new(big.Rat).Sub(next, before))
		for _, variant := range variants {
			v := triple{new(big.Rat), new(big.Rat), new(big.Rat)}
			available := true
			for _, mapped := range keys {
				w := mapped
				if w == "" {
					w = variant
				}
				f := c.outputFactor(m.move.From, m.move.To, w)
				if f == nil {
					available = false
					break
				}
				if !slices.Contains(mr.FactorSources, f.Source) {
					mr.FactorSources = append(mr.FactorSources, f.Source)
				}
				for _, s := range []struct {
					dst    *big.Rat
					factor string
				}{{v.m, f.Output}, {v.l, f.Low}, {v.h, f.High}} {
					n, _ := cost(m.buckets[mapped], *c.price(m.move.To), m.input, rat(s.factor))
					s.dst.Add(s.dst, n)
				}
			}
			if !available {
				allL2 = false
				mr.Gap = "no measured verbosity factor"
				continue
			}
			for _, x := range []*big.Rat{v.m, v.l, v.h} {
				x.Mul(x, m.ratio)
			}
			mr.Level2 = append(mr.Level2, WorkloadCost{variant, Interval{text(v.m), text(v.l), text(v.h)}})
			t := totals2[variant]
			for _, s := range []struct{ dst, src *big.Rat }{{t.m, v.m}, {t.l, v.l}, {t.h, v.h}} {
				s.dst.Add(s.dst, new(big.Rat).Sub(s.src, before))
			}
		}
		r.Moves = append(r.Moves, mr)
	}
	r.Level1 = ptr(text(l1))
	if allL2 {
		var lo, hi *big.Rat
		for _, w := range variants {
			t := totals2[w]
			r.Level2 = append(r.Level2, WorkloadCost{w, Interval{text(t.m), text(t.l), text(t.h)}})
			if lo == nil || t.l.Cmp(lo) < 0 {
				lo = t.l
			}
			if hi == nil || t.h.Cmp(hi) > 0 {
				hi = t.h
			}
		}
		r.Envelope = &Interval{Low: text(lo), High: text(hi)}
	} else {
		r.Dominant = "One or more moves lack covered usage or a measured verbosity factor; only supported move-level results are shown."
	}
	return r
}
