// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

const (
	MaxRoutingRoutes  = 128
	MaxRoutingAgents  = 1024
	MaxRoutingModels  = 128
	MaxRoutingCohorts = 4096
	MaxRoutingCells   = 16384
)

// RoutingFieldCoverage partitions rows into exactly one primary field state.
// MissingRows is the sum of absent, empty/null and invalid rows.
type RoutingFieldCoverage struct {
	KnownRows        uint64 `json:"known_rows"`
	ColumnAbsentRows uint64 `json:"column_absent_rows"`
	EmptyOrNullRows  uint64 `json:"empty_or_null_rows"`
	InvalidRows      uint64 `json:"invalid_rows"`
	MissingRows      uint64 `json:"missing_rows"`
}

type RoutingFields struct {
	Agent              RoutingFieldCoverage `json:"agent"`
	OriginalRoute      RoutingFieldCoverage `json:"original_route"`
	AttemptedFallbacks RoutingFieldCoverage `json:"attempted_fallbacks"`
	SelectedModel      RoutingFieldCoverage `json:"selected_model"`
	DeploymentID       RoutingFieldCoverage `json:"deployment_id"`
	OverheadMarker     RoutingFieldCoverage `json:"overhead_marker"`
}

type RoutingCallTypeCoverage struct {
	CallType string        `json:"call_type"`
	Before   RoutingFields `json:"before"`
	After    RoutingFields `json:"after"`
}

type RoutingCoverage struct {
	Before     RoutingFields             `json:"before"`
	After      RoutingFields             `json:"after"`
	ByCallType []RoutingCallTypeCoverage `json:"by_call_type"`
}

type ScopeCounts struct {
	MatchedRows          uint64 `json:"matched_rows"`
	NonmatchingKnownRows uint64 `json:"nonmatching_known_rows"`
	UnknownIdentityRows  uint64 `json:"unknown_identity_rows"`
}

// ScopeCoverage describes all in-period rows, before the scope predicate.
// Selector text and selector hashes are never reported.
type ScopeCoverage struct {
	Kind                    string                    `json:"kind"`
	Before                  ScopeCounts               `json:"before"`
	After                   ScopeCounts               `json:"after"`
	FieldCoverageByCallType []RoutingCallTypeCoverage `json:"field_coverage_by_call_type"`
}

type RoutingIdentity struct {
	Item  string `json:"item"`
	State string `json:"state"`
	Hash  string `json:"hash,omitempty"`
	Label string `json:"label,omitempty"`
}

type RoutingFallbacks struct {
	KnownZeroRows     uint64 `json:"known_zero_rows"`
	KnownPositiveRows uint64 `json:"known_positive_rows"`
	UnknownRows       uint64 `json:"unknown_rows"`
}

// RoutingMeasure uses the same token and recorded-cost accounting as Window.
// RecordedSpend is a known subtotal; Quality describes unavailable values.
type RoutingMeasure struct {
	Requests         uint64           `json:"requests"`
	Tokens           uint64           `json:"recorded_tokens"`
	InputTokens      uint64           `json:"recorded_input_tokens"`
	OutputTokens     uint64           `json:"recorded_output_tokens"`
	RecordedSpend    string           `json:"recorded_spend_usd"`
	Quality          Quality          `json:"quality"`
	FailureTokens    *FailureTokens   `json:"failure_tokens,omitempty"`
	UnknownModelRows uint64           `json:"unknown_model_rows"`
	Fallbacks        RoutingFallbacks `json:"recorded_fallback_counters"`
}

type RoutingMeasures struct {
	Before RoutingMeasure `json:"before"`
	After  RoutingMeasure `json:"after"`
}

// Shares are fractions rounded to 12 decimal places, using exact rational
// arithmetic before rounding. A zero denominator is represented by null.
type RoutingShares struct {
	Requests *string `json:"requests"`
	Tokens   *string `json:"tokens"`
	Spend    *string `json:"recorded_spend"`
}

type RoutingModel struct {
	RoutingIdentity
	Before       RoutingMeasure `json:"before"`
	After        RoutingMeasure `json:"after"`
	SharesBefore RoutingShares  `json:"shares_before"`
	SharesAfter  RoutingShares  `json:"shares_after"`
}

type RoutingCohort struct {
	Route  RoutingIdentity  `json:"route"`
	Agent  *RoutingIdentity `json:"agent,omitempty"`
	Before RoutingMeasure   `json:"before"`
	After  RoutingMeasure   `json:"after"`
	Models []RoutingModel   `json:"models"`
}

// Raw columns on unsupported rows are evidence, not supported-model usage.
type RoutingRawMeasure struct {
	Usage            CallTypeCounts `json:"raw_usage"`
	RecordedSpend    string         `json:"recorded_spend_usd"`
	SpendMissing     uint64         `json:"missing_recorded_spend"`
	SpendInvalid     uint64         `json:"invalid_recorded_spend"`
	SpendZeroUnknown uint64         `json:"zero_spend_origin_unknown"`
}

type RoutingRawMeasures struct {
	Before RoutingRawMeasure `json:"before"`
	After  RoutingRawMeasure `json:"after"`
}

type RoutingRemainder struct {
	Cohorts    int            `json:"cohorts"`
	ModelCells int            `json:"model_cells"`
	Before     RoutingMeasure `json:"before"`
	After      RoutingMeasure `json:"after"`
}

type RoutingAudit struct {
	Schema                      string             `json:"schema"`
	SourceContract              string             `json:"source_contract"`
	IdentityBasis               string             `json:"identity_basis"`
	ProviderSnapshotConfirmed   bool               `json:"provider_snapshot_confirmed"`
	GroupBy                     string             `json:"group_by"`
	ConfigurationLabelsIncluded bool               `json:"configuration_labels_included,omitempty"`
	Coverage                    RoutingCoverage    `json:"coverage"`
	ModelRows                   RoutingMeasures    `json:"model_rows"`
	ClassifierOverhead          RoutingRawMeasures `json:"classifier_overhead"`
	OtherRows                   RoutingRawMeasures `json:"other_rows"`
	Cohorts                     []RoutingCohort    `json:"cohorts"`
	DisplayRemainder            RoutingRemainder   `json:"display_remainder"`
}

type routingFieldState uint8

const (
	routingKnown routingFieldState = iota
	routingAbsent
	routingEmpty
	routingInvalid
)

func routingTextState(row Row, field string) routingFieldState {
	v, present := row.Values[field]
	if !present {
		return routingAbsent
	}
	if !utf8.ValidString(v) || len(v) > MaxFieldBytes {
		return routingInvalid
	}
	if strings.TrimSpace(v) == "" {
		return routingEmpty
	}
	return routingKnown
}

func routingModelState(row Row, field string) routingFieldState {
	state := routingTextState(row, field)
	if state == routingKnown && !RoutingSelectedTextKnown(row.Values[field]) {
		return routingInvalid
	}
	return state
}

// RoutingSelectedTextKnown rejects unavailable selected-identity placeholders.
// Valid identities retain their original text for keyed canonicalization.
func RoutingSelectedTextKnown(value string) bool {
	if !utf8.ValidString(value) || len(value) > MaxFieldBytes || strings.TrimSpace(value) == "" {
		return false
	}
	switch strings.TrimSpace(value) {
	case "unknown", "Unknown", "unknown_model", "__unknown__", "__other__", "__missing__", "__unmapped__", "__overflow__":
		return false
	}
	return true
}

// RoutingSelectedModelKnown applies only to the opt-in selected-deployment
// basis. Neither a route alias nor a deployment ID alone supplies a model.
func RoutingSelectedModelKnown(row Row) bool {
	return routingModelState(row, "model") == routingKnown && routingModelState(row, "model_id") == routingKnown
}

func routingFallback(row Row) (uint64, routingFieldState) {
	state := routingTextState(row, "attempted_fallbacks")
	if state != routingKnown {
		return 0, state
	}
	v := row.Values["attempted_fallbacks"]
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, routingInvalid
		}
	}
	n, err := strconv.ParseUint(v, 10, 63)
	if err != nil {
		return 0, routingInvalid
	}
	return n, routingKnown
}

func (c *RoutingFieldCoverage) add(state routingFieldState) {
	switch state {
	case routingKnown:
		c.KnownRows++
	case routingAbsent:
		c.ColumnAbsentRows++
	case routingEmpty:
		c.EmptyOrNullRows++
	case routingInvalid:
		c.InvalidRows++
	}
	if state != routingKnown {
		c.MissingRows++
	}
}

func (c *RoutingFields) add(row Row) {
	c.Agent.add(routingTextState(row, "agent_id"))
	c.OriginalRoute.add(routingTextState(row, "original_model_group"))
	_, fallback := routingFallback(row)
	c.AttemptedFallbacks.add(fallback)
	c.SelectedModel.add(routingModelState(row, "model"))
	c.DeploymentID.add(routingModelState(row, "model_id"))
	c.OverheadMarker.add(routingTextState(row, "internal_call_origin"))
}

func (c *RoutingCoverage) add(row Row, side int) {
	fields := &c.Before
	if side == 1 {
		fields = &c.After
	}
	fields.add(row)
	name := row.Values["call_type"]
	if operation(name) == "" && name != "pass_through_endpoint" {
		name = "unknown"
	}
	i := slices.IndexFunc(c.ByCallType, func(v RoutingCallTypeCoverage) bool { return v.CallType == name })
	if i < 0 {
		i = len(c.ByCallType)
		c.ByCallType = append(c.ByCallType, RoutingCallTypeCoverage{CallType: name})
	}
	fields = &c.ByCallType[i].Before
	if side == 1 {
		fields = &c.ByCallType[i].After
	}
	fields.add(row)
}

type routingID struct {
	hash  [32]byte
	known bool
}

func routingRowIdentity(row Row, kind, field string, key []byte) routingID {
	if routingTextState(row, field) != routingKnown {
		return routingID{}
	}
	hash, err := identity(key, kind, row.Values[field])
	return routingID{hash: hash, known: err == nil}
}

type routingTotals struct {
	a         *aggregate
	fallbacks RoutingFallbacks
	unknown   uint64
}

func newRoutingTotals() *routingTotals {
	return &routingTotals{a: newAggregate(Period{}, false)}
}

func (t *routingTotals) add(row Row, obs accounting.Observation, q Quality, known bool) error {
	if err := t.a.add(row, obs, q); err != nil {
		return err
	}
	if !known {
		t.unknown++
	}
	n, state := routingFallback(row)
	switch {
	case state != routingKnown:
		t.fallbacks.UnknownRows++
	case n == 0:
		t.fallbacks.KnownZeroRows++
	default:
		t.fallbacks.KnownPositiveRows++
	}
	return nil
}

func (t *routingTotals) result() RoutingMeasure {
	w := t.a.result()
	return RoutingMeasure{
		Requests: w.Requests, Tokens: w.Tokens,
		InputTokens:   w.Counters[accounting.Prefix+"input_tokens_total"],
		OutputTokens:  w.Counters[accounting.Prefix+"output_tokens_total"],
		RecordedSpend: w.RecordedSpend, Quality: w.Quality, FailureTokens: w.FailureTokens,
		UnknownModelRows: t.unknown, Fallbacks: t.fallbacks,
	}
}

func (t *routingTotals) merge(v *routingTotals) error {
	w, other := &t.a.window, &v.a.window
	for _, pair := range []struct {
		dst   *uint64
		value uint64
	}{
		{&w.Requests, other.Requests}, {&w.Tokens, other.Tokens}, {&t.unknown, v.unknown},
		{&t.fallbacks.KnownZeroRows, v.fallbacks.KnownZeroRows},
		{&t.fallbacks.KnownPositiveRows, v.fallbacks.KnownPositiveRows},
		{&t.fallbacks.UnknownRows, v.fallbacks.UnknownRows},
	} {
		n, err := accounting.AddBounded(*pair.dst, pair.value)
		if err != nil {
			return err
		}
		*pair.dst = n
	}
	if err := accounting.MergeCounts(w.Counters, other.Counters); err != nil {
		return err
	}
	w.Quality.add(other.Quality)
	t.a.cost.Add(&t.a.cost, &v.a.cost)
	if f := other.FailureTokens; f != nil {
		if w.FailureTokens == nil {
			w.FailureTokens = &FailureTokens{Finality: "unknown"}
		}
		for _, pair := range []struct {
			dst   *uint64
			value uint64
		}{
			{&w.FailureTokens.Records, f.Records}, {&w.FailureTokens.Input, f.Input},
			{&w.FailureTokens.Output, f.Output}, {&w.FailureTokens.Total, f.Total},
		} {
			n, err := accounting.AddBounded(*pair.dst, pair.value)
			if err != nil {
				return err
			}
			*pair.dst = n
		}
	}
	return nil
}

type routingPair [2]*routingTotals

func newRoutingPair() routingPair { return routingPair{newRoutingTotals(), newRoutingTotals()} }

func (p routingPair) result() RoutingMeasures {
	return RoutingMeasures{Before: p[0].result(), After: p[1].result()}
}

type routingRaw struct {
	report  Report
	measure RoutingRawMeasure
	cost    big.Rat
}

func (r *routingRaw) add(row Row) error {
	if r.report.CallTypes == nil {
		r.report.CallTypes = make([]CallType, 1)
	}
	// Compose the established raw-column validator without retaining call labels.
	if err := recordCallType(&r.report, map[string]int{row.Values["call_type"]: 0}, row, 0); err != nil {
		return err
	}
	v := row.Values["spend"]
	if v == "" {
		r.measure.SpendMissing++
	} else if cost, ok := accounting.Cost(v); !ok {
		r.measure.SpendInvalid++
	} else {
		r.cost.Add(&r.cost, cost)
		if cost.Sign() == 0 {
			r.measure.SpendZeroUnknown++
		}
	}
	return nil
}

func (r *routingRaw) result() RoutingRawMeasure {
	m := r.measure
	m.RecordedSpend = accounting.CostText(&r.cost)
	if len(r.report.CallTypes) != 0 {
		m.Usage = r.report.CallTypes[0].Before
	}
	return m
}

type routingCohortKey struct{ route, agent routingID }
type routingCell struct {
	id      routingID
	totals  routingPair
	ordinal int
}
type routingCohort struct {
	id      routingCohortKey
	totals  routingPair
	models  map[routingID]*routingCell
	ordinal int
}

type routingAccumulator struct {
	coverage                              RoutingCoverage
	scope                                 *ScopeCoverage
	selection                             *scopeSelection
	audit                                 bool
	by                                    string
	totals                                routingPair
	overhead, other                       [2]routingRaw
	routes, agents, models                map[routingID]int
	routeLabels, agentLabels, modelLabels map[routingID]string
	showLabels                            bool
	cohorts                               map[routingCohortKey]*routingCohort
	cells                                 int
}

func newRoutingAccumulator(opts Options, selection *scopeSelection) *routingAccumulator {
	if !opts.RoutingAudit && selection == nil {
		return nil
	}
	a := &routingAccumulator{audit: opts.RoutingAudit, by: opts.RoutingBy, selection: selection}
	if selection != nil {
		a.scope = &ScopeCoverage{Kind: selection.kind}
	}
	if a.audit {
		a.totals = newRoutingPair()
		a.routes, a.models = map[routingID]int{}, map[routingID]int{}
		a.modelLabels = map[routingID]string{}
		a.showLabels = opts.ShowLabels
		if a.showLabels {
			a.routeLabels, a.agentLabels = map[routingID]string{}, map[routingID]string{}
		}
		if a.by == "route-agent" {
			a.agents = map[routingID]int{}
		}
		a.cohorts = map[routingCohortKey]*routingCohort{}
	}
	return a
}

func (a *routingAccumulator) observe(row Row, side int, key []byte) bool {
	if a == nil {
		return true
	}
	a.coverage.add(row, side)
	matched, known := a.selection.match(row, key)
	if a.scope != nil {
		counts := &a.scope.Before
		if side == 1 {
			counts = &a.scope.After
		}
		switch {
		case matched:
			counts.MatchedRows++
		case known:
			counts.NonmatchingKnownRows++
		default:
			counts.UnknownIdentityRows++
		}
	}
	return matched
}

func routingRegister(ids map[routingID]int, id routingID, limit int) error {
	if !id.known {
		return nil
	}
	if _, ok := ids[id]; ok {
		return nil
	}
	if len(ids) >= limit {
		return errors.New("routing audit exceeds identity limit; select a narrower export")
	}
	ids[id] = len(ids) + 1
	return nil
}

func (a *routingAccumulator) add(row Row, side int, key []byte, obs accounting.Observation, q Quality) error {
	if a == nil || !a.audit {
		return nil
	}
	if operation(row.Values["call_type"]) == "" {
		raw := &a.other[side]
		if row.Values["call_type"] == "pass_through_endpoint" && row.Values["internal_call_origin"] == "autorouter_classifier" {
			raw = &a.overhead[side]
		}
		return raw.add(row)
	}
	route := routingRowIdentity(row, "route", "original_model_group", key)
	if err := routingRegister(a.routes, route, MaxRoutingRoutes); err != nil {
		return err
	}
	agent := routingID{}
	if a.by == "route-agent" {
		agent = routingRowIdentity(row, "agent", "agent_id", key)
		if err := routingRegister(a.agents, agent, MaxRoutingAgents); err != nil {
			return err
		}
	}
	model := routingID{}
	if RoutingSelectedModelKnown(row) {
		model = routingRowIdentity(row, "model", "model", key)
	}
	if err := routingRegister(a.models, model, MaxRoutingModels); err != nil {
		return err
	}
	publicModel := reprice.PublicModelID(row.Values["model"], row.Values["custom_llm_provider"])
	routingRememberLabel(a.modelLabels, model, publicModel)
	if a.showLabels {
		routingRememberLabel(a.routeLabels, route, row.Values["original_model_group"])
		routingRememberLabel(a.agentLabels, agent, row.Values["agent_id"])
	}
	id := routingCohortKey{route, agent}
	c, exists := a.cohorts[id]
	if !exists {
		if len(a.cohorts) >= MaxRoutingCohorts {
			return errors.New("routing audit exceeds 4096 cohorts; select a narrower export")
		}
		c = &routingCohort{id: id, totals: newRoutingPair(), models: map[routingID]*routingCell{}, ordinal: row.Number}
		a.cohorts[id] = c
	}
	m, exists := c.models[model]
	if !exists {
		if a.cells >= MaxRoutingCells {
			return errors.New("routing audit exceeds 16384 model cells; select a narrower export")
		}
		m = &routingCell{id: model, totals: newRoutingPair(), ordinal: row.Number}
		c.models[model], a.cells = m, a.cells+1
	}
	for _, totals := range []*routingTotals{a.totals[side], c.totals[side], m.totals[side]} {
		if err := totals.add(row, obs, q, model.known); err != nil {
			return err
		}
	}
	return nil
}

func routingLabel(id routingID, kind string, aliases map[routingID]int, hashes bool) RoutingIdentity {
	if !id.known {
		return RoutingIdentity{Item: kind + "-unknown", State: "unknown"}
	}
	label := RoutingIdentity{Item: fmt.Sprintf("%s-%d", kind, aliases[id]), State: "known"}
	if hashes {
		label.Hash = fmt.Sprintf("%x", id.hash)
	}
	return label
}

// Canonical-equivalent source spellings share accounting, not permission to
// display one spelling as the identity of every row. Ambiguity stays aliased.
func routingRememberLabel(labels map[routingID]string, id routingID, value string) {
	if !id.known {
		return
	}
	if prior, exists := labels[id]; exists && prior != value {
		labels[id] = ""
	} else {
		labels[id] = value
	}
}

func routingShare(n, d *big.Rat) *string {
	if d.Sign() == 0 {
		return nil
	}
	s := new(big.Rat).Quo(n, d).FloatString(12)
	return &s
}

func routingShares(part, total *routingTotals) RoutingShares {
	count := func(v uint64) *big.Rat { return new(big.Rat).SetInt(new(big.Int).SetUint64(v)) }
	return RoutingShares{
		Requests: routingShare(count(part.a.window.Requests), count(total.a.window.Requests)),
		Tokens:   routingShare(count(part.a.window.Tokens), count(total.a.window.Tokens)),
		Spend:    routingShare(&part.a.cost, &total.a.cost),
	}
}

func routingOrder(a routingPair, ai int, b routingPair, bi int) int {
	x, y := a[0].a.window.Requests+a[1].a.window.Requests, b[0].a.window.Requests+b[1].a.window.Requests
	if x > y {
		return -1
	}
	if x < y {
		return 1
	}
	return ai - bi
}

func (a *routingAccumulator) result(top int, hashes bool) (*RoutingAudit, *ScopeCoverage, error) {
	if a == nil {
		return nil, nil, nil
	}
	if a.coverage.ByCallType == nil {
		a.coverage.ByCallType = []RoutingCallTypeCoverage{}
	}
	if a.scope != nil {
		a.scope.FieldCoverageByCallType = a.coverage.ByCallType
	}
	if !a.audit {
		return nil, a.scope, nil
	}
	publicCounts := map[string]int{}
	for _, public := range a.modelLabels {
		if public != "" {
			publicCounts[public]++
		}
	}
	r := &RoutingAudit{
		Schema: "fleetdiff-litellm-routing-audit/v1", SourceContract: "litellm-1.104.0-spend-routing/v1",
		IdentityBasis: "gateway-selected-deployment", GroupBy: a.by, Coverage: a.coverage,
		ConfigurationLabelsIncluded: a.showLabels,
		ModelRows:                   a.totals.result(),
		ClassifierOverhead:          RoutingRawMeasures{a.overhead[0].result(), a.overhead[1].result()},
		OtherRows:                   RoutingRawMeasures{a.other[0].result(), a.other[1].result()},
		Cohorts:                     []RoutingCohort{},
	}
	cohorts := make([]*routingCohort, 0, len(a.cohorts))
	for _, c := range a.cohorts {
		cohorts = append(cohorts, c)
	}
	slices.SortFunc(cohorts, func(x, y *routingCohort) int { return routingOrder(x.totals, x.ordinal, y.totals, y.ordinal) })
	remainder := newRoutingPair()
	for i, c := range cohorts {
		if i >= top {
			r.DisplayRemainder.Cohorts++
			r.DisplayRemainder.ModelCells += len(c.models)
			for side := range 2 {
				if err := remainder[side].merge(c.totals[side]); err != nil {
					return nil, nil, err
				}
			}
			continue
		}
		cohort := RoutingCohort{Route: routingLabel(c.id.route, "route", a.routes, hashes), Before: c.totals[0].result(), After: c.totals[1].result(), Models: []RoutingModel{}}
		cohort.Route.Label = a.routeLabels[c.id.route]
		if a.by == "route-agent" {
			agent := routingLabel(c.id.agent, "agent", a.agents, hashes)
			agent.Label = a.agentLabels[c.id.agent]
			cohort.Agent = &agent
		}
		models := make([]*routingCell, 0, len(c.models))
		for _, m := range c.models {
			models = append(models, m)
		}
		slices.SortFunc(models, func(x, y *routingCell) int { return routingOrder(x.totals, x.ordinal, y.totals, y.ordinal) })
		for j, m := range models {
			if j >= top {
				r.DisplayRemainder.ModelCells++
				for side := range 2 {
					if err := remainder[side].merge(m.totals[side]); err != nil {
						return nil, nil, err
					}
				}
				continue
			}
			label := routingLabel(m.id, "selected-model", a.models, hashes)
			if public := a.modelLabels[m.id]; public != "" {
				if publicCounts[public] > 1 {
					label.Item = public + " [" + label.Item + "]"
				} else {
					label.Item = public
				}
			}
			cohort.Models = append(cohort.Models, RoutingModel{
				RoutingIdentity: label, Before: m.totals[0].result(), After: m.totals[1].result(),
				SharesBefore: routingShares(m.totals[0], c.totals[0]), SharesAfter: routingShares(m.totals[1], c.totals[1]),
			})
		}
		r.Cohorts = append(r.Cohorts, cohort)
	}
	r.DisplayRemainder.Before, r.DisplayRemainder.After = remainder[0].result(), remainder[1].result()
	return r, a.scope, nil
}
