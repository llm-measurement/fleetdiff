// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
)

const MaxAgents = 1024

type AgentCoverage struct {
	KnownRows            uint64 `json:"known_rows"`
	UnknownRows          uint64 `json:"unknown_rows"`
	AmbiguousRows        uint64 `json:"ambiguous_rows"`
	ColumnAbsentRows     uint64 `json:"column_absent_rows"`
	EmptyOrNullRows      uint64 `json:"empty_or_null_rows"`
	NoMatchingTagRows    uint64 `json:"no_matching_tag_rows"`
	NonStringTagRows     uint64 `json:"non_string_tag_rows"`
	RepeatedAgentTagRows uint64 `json:"repeated_agent_tag_rows"`
	RepeatedAgentTags    uint64 `json:"repeated_agent_tags"`
	EmptySuffixRows      uint64 `json:"empty_suffix_rows"`
	EmptySuffixTags      uint64 `json:"empty_suffix_tags"`
}

type AgentWindow struct {
	Known     Window        `json:"known"`
	Unknown   Window        `json:"unknown"`
	Ambiguous Window        `json:"ambiguous"`
	Coverage  AgentCoverage `json:"coverage"`
}

type AgentReport struct {
	Schema            string             `json:"schema"`
	Source            string             `json:"label_source"`
	Before            AgentWindow        `json:"before"`
	After             AgentWindow        `json:"after"`
	RankBy            string             `json:"rank_by"`
	LabelsIncluded    bool               `json:"sensitive_labels_included"`
	Contributors      []AgentContributor `json:"contributors"`
	UnavailableCosts  []AgentContributor `json:"unavailable_cost_contributors,omitempty"`
	UndisplayedBefore AgentValues        `json:"known_undisplayed_before"`
	UndisplayedAfter  AgentValues        `json:"known_undisplayed_after"`
	ShareBasis        string             `json:"spend_share_basis"`
}

type AgentCostCoverage struct {
	Usable      uint64 `json:"usable_rows"`
	Missing     uint64 `json:"missing_rows"`
	Invalid     uint64 `json:"invalid_rows"`
	ZeroUnknown uint64 `json:"zero_origin_unknown_rows"`
}

type AgentValues struct {
	Requests     uint64            `json:"logged_rows"`
	Tokens       uint64            `json:"recorded_tokens"`
	RecordedUSD  *string           `json:"recorded_usd"`
	Share        *string           `json:"share_of_recorded_usd"`
	CostCoverage AgentCostCoverage `json:"cost_coverage"`
}

type AgentContributor struct {
	Item   string      `json:"item"`
	Hash   string      `json:"hash,omitempty"`
	Label  string      `json:"label,omitempty"`
	Before AgentValues `json:"before"`
	After  AgentValues `json:"after"`
	Delta  *string     `json:"recorded_usd_delta"`
}

type agentResolution struct {
	state, label, source, prefix string
	absent, empty, nonString     bool
	repeated, emptySuffix        uint64
}

// ValidateAgentOptions is shared by the CLI before database dispatch and the
// offline reader. A selector never infers or falls back to a different source.
func ValidateAgentOptions(o Options) error {
	if o.GroupBy != "agent" {
		if o.AgentSource != "" || o.AgentTagPrefix != "" || o.RankBy != "" {
			return errors.New("agent-source, agent-tag-prefix and rank-by require group-by agent")
		}
		return nil
	}
	if o.AgentSource != "agent_id" && o.AgentSource != "request-tag" {
		return errors.New("group-by agent requires explicit agent-source agent_id or request-tag")
	}
	if o.AgentSource == "request-tag" {
		if strings.TrimSpace(o.AgentTagPrefix) == "" || len(o.AgentTagPrefix) > MaxFieldBytes || !utf8.ValidString(o.AgentTagPrefix) {
			return errors.New("request-tag requires a nonempty UTF-8 agent-tag-prefix of at most 8192 bytes")
		}
	} else if o.AgentTagPrefix != "" {
		return errors.New("agent-tag-prefix requires agent-source request-tag")
	}
	if o.RankBy != "" && o.RankBy != "recorded-spend-change" {
		return errors.New("rank-by supports recorded-spend-change for agent grouping")
	}
	if o.ScopeAgent != "" && o.AgentSource == "request-tag" {
		return errors.New("scope-agent selects agent_id; do not combine it with request-tag attribution")
	}
	return nil
}

func resolveAgent(row Row, o Options) (*agentResolution, error) {
	a := &agentResolution{state: "unknown", source: o.AgentSource, prefix: o.AgentTagPrefix}
	field := "agent_id"
	if o.AgentSource == "request-tag" {
		field = "request_tags"
	}
	raw, present := row.Values[field]
	a.absent = !present
	if !present {
		return a, nil
	}
	if len(raw) > MaxFieldBytes || !utf8.ValidString(raw) {
		return nil, errors.New("invalid agent source field; use UTF-8 within the 8192-byte field limit")
	}
	if strings.TrimSpace(raw) == "" || field == "request_tags" && strings.TrimSpace(raw) == "null" {
		a.empty = true
		return a, nil
	}
	if field == "agent_id" {
		a.state, a.label = "known", raw
		return a, nil
	}
	tags, err := ParseRequestTags(raw)
	if err != nil {
		if errors.Is(err, ErrNonStringRequestTags) && row.AgentNamespace == "" {
			a.nonString = true
			return a, nil
		}
		return nil, err
	}
	// Deduplicate matching suffixes; retain no unselected tag values.
	labels := map[string]bool{}
	for _, tag := range tags {
		label, match := strings.CutPrefix(tag, o.AgentTagPrefix)
		if !match {
			continue
		}
		if strings.TrimSpace(label) == "" {
			a.emptySuffix++
			continue
		}
		if labels[label] {
			a.repeated++
		}
		labels[label] = true
	}
	if len(labels) == 1 {
		a.state = "known"
		for label := range labels {
			a.label = label
		}
	} else if len(labels) > 1 {
		a.state = "ambiguous"
	}
	return a, nil
}

var ErrNonStringRequestTags = errors.New("invalid request_tags; every array element must be a string")

// ParseRequestTags validates bounded scalar-array text for SQL and observation
// adapters before attribution or duplicate normalization can discard evidence.
func ParseRequestTags(raw string) ([]string, error) {
	if len(raw) > MaxFieldBytes {
		return nil, errors.New("request_tags exceeds the 8192-byte field limit")
	}
	if strings.TrimSpace(raw) == "null" {
		return nil, nil
	}
	var elements []json.RawMessage
	if len(raw) > MaxFieldBytes || preflightSpendJSON([]byte(raw), 0) != nil || json.Unmarshal([]byte(raw), &elements) != nil {
		return nil, errors.New("invalid request_tags; use a JSON array of strings within the 8192-byte field limit")
	}
	tags := make([]string, 0, len(elements))
	for _, element := range elements {
		var tag string
		if len(element) == 0 || element[0] != '"' || json.Unmarshal(element, &tag) != nil {
			return nil, ErrNonStringRequestTags
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

type agentPair [2]*aggregate

func newAgentPair(before, after Period) agentPair {
	return agentPair{newAggregate(before, false), newAggregate(after, false)}
}

type agentEntry struct {
	digest [32]byte
	label  string
	pair   agentPair
	order  int
}

type agentAccumulator struct {
	opts                      Options
	before, after             Period
	known, unknown, ambiguous agentPair
	coverage                  [2]AgentCoverage
	entries                   map[[32]byte]*agentEntry
}

func newAgentAccumulator(o Options, before, after Period) *agentAccumulator {
	if o.GroupBy != "agent" {
		return nil
	}
	return &agentAccumulator{opts: o, before: before, after: after, known: newAgentPair(before, after), unknown: newAgentPair(before, after), ambiguous: newAgentPair(before, after), entries: map[[32]byte]*agentEntry{}}
}

func (a *agentAccumulator) add(row Row, side int, digest [32]byte, obs accounting.Observation, q Quality) error {
	if a == nil {
		return nil
	}
	r, c := row.agent, &a.coverage[side]
	if r.absent {
		c.ColumnAbsentRows++
	}
	if r.empty {
		c.EmptyOrNullRows++
	}
	if r.nonString {
		c.NonStringTagRows++
	}
	if r.repeated > 0 {
		c.RepeatedAgentTagRows++
		c.RepeatedAgentTags += r.repeated
	}
	if r.emptySuffix > 0 {
		c.EmptySuffixRows++
		c.EmptySuffixTags += r.emptySuffix
	}
	var p agentPair
	switch r.state {
	case "known":
		c.KnownRows++
		p = a.known
		e := a.entries[digest]
		if e == nil {
			if len(a.entries) == MaxAgents {
				if a.opts.AgentSource == "request-tag" {
					return fmt.Errorf("agent attribution exceeds 1024 agents for prefix %s; select a narrower agent-tag-prefix or export", strconv.QuoteToASCII(a.opts.AgentTagPrefix))
				}
				return errors.New("agent attribution exceeds 1024 agents; select a narrower export")
			}
			e = &agentEntry{digest: digest, pair: newAgentPair(a.before, a.after), order: len(a.entries)}
			if a.opts.ShowLabels && a.opts.AcknowledgeSensitiveLabels {
				e.label = r.label
			}
			a.entries[digest] = e
		}
		if err := e.pair[side].add(row, obs, q); err != nil {
			return err
		}
	case "ambiguous":
		c.AmbiguousRows++
		p = a.ambiguous
	default:
		c.UnknownRows++
		if !r.absent && !r.empty && !r.nonString && r.source == "request-tag" {
			c.NoMatchingTagRows++
		}
		p = a.unknown
	}
	return p[side].add(row, obs, q)
}

func (a *agentAccumulator) result() *AgentReport {
	if a == nil {
		return nil
	}
	r := &AgentReport{Schema: "fleetdiff-agent-attribution/v1", Source: a.opts.AgentSource}
	for i, w := range []*AgentWindow{&r.Before, &r.After} {
		w.Known, w.Unknown, w.Ambiguous = a.known[i].result(), a.unknown[i].result(), a.ambiguous[i].result()
		w.Coverage = a.coverage[i]
	}
	return r
}

func (a *agentAccumulator) ordered() []*agentEntry {
	var entries []*agentEntry
	for _, e := range a.entries {
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b *agentEntry) int { return a.order - b.order })
	return entries
}

func agentValues(w Window, total *big.Rat) AgentValues {
	c := AgentCostCoverage{Missing: w.Quality.SpendMissing, Invalid: w.Quality.SpendInvalid, ZeroUnknown: w.Quality.SpendZeroUnknown}
	c.Usable = w.Requests - c.Missing - c.Invalid
	v := AgentValues{Requests: w.Requests, Tokens: w.Tokens, CostCoverage: c}
	if c.Usable > 0 || w.Requests == 0 {
		usd := w.RecordedSpend
		v.RecordedUSD = &usd
		n, _ := new(big.Rat).SetString(usd)
		v.Share = routingShare(n, total)
	}
	return v
}

func (a *agentAccumulator) aliases() map[string]string {
	labels := map[string]string{}
	if a != nil {
		for _, e := range a.ordered() {
			labels[fmt.Sprintf("%016x", binary.BigEndian.Uint64(e.digest[:8]))] = fmt.Sprintf("agent-%d", e.order+1)
		}
	}
	return labels
}

func (a *agentAccumulator) finish(r *Report) error {
	if a == nil {
		return nil
	}
	entries := a.ordered()
	slices.SortStableFunc(entries, func(x, y *agentEntry) int {
		if a.opts.RankBy == "recorded-spend-change" {
			available := func(e *agentEntry) bool {
				for _, p := range e.pair {
					w := p.window
					if w.Requests > 0 && w.Quality.SpendMissing+w.Quality.SpendInvalid == w.Requests {
						return false
					}
				}
				return true
			}
			xok, yok := available(x), available(y)
			if xok != yok {
				if xok {
					return -1
				}
				return 1
			}
			if !xok {
				return 0
			}
			xd := new(big.Rat).Sub(&x.pair[1].cost, &x.pair[0].cost)
			yd := new(big.Rat).Sub(&y.pair[1].cost, &y.pair[0].cost)
			return yd.Cmp(xd)
		}
		xd, yd := int64(x.pair[1].window.Tokens)-int64(x.pair[0].window.Tokens), int64(y.pair[1].window.Tokens)-int64(y.pair[0].window.Tokens)
		if xd > yd {
			return -1
		}
		if xd < yd {
			return 1
		}
		return 0
	})
	report := a.result()
	report.RankBy = a.opts.RankBy
	if report.RankBy == "" {
		report.RankBy = "recorded-token-change"
	}
	report.LabelsIncluded = a.opts.ShowLabels && a.opts.AcknowledgeSensitiveLabels
	report.ShareBasis = "usable recorded USD in all analyzed rows of each period, including unknown, ambiguous and undisplayed agents; missing and invalid costs remain unquantified"
	totals := [2]*big.Rat{}
	for i, w := range []Window{r.Before, r.After} {
		totals[i], _ = new(big.Rat).SetString(w.RecordedSpend)
	}
	remaining := [2]*routingTotals{newRoutingTotals(), newRoutingTotals()}
	report.Contributors = []AgentContributor{}
	for i, e := range entries {
		c := AgentContributor{Item: fmt.Sprintf("agent-%d", e.order+1), Label: e.label, Before: agentValues(e.pair[0].result(), totals[0]), After: agentValues(e.pair[1].result(), totals[1])}
		if a.opts.ShowHashes {
			c.Hash = fmt.Sprintf("%x", e.digest)
		}
		if c.Before.RecordedUSD != nil && c.After.RecordedUSD != nil {
			delta := costText(new(big.Rat).Sub(&e.pair[1].cost, &e.pair[0].cost))
			c.Delta = &delta
		}
		if a.opts.RankBy == "recorded-spend-change" && c.Delta == nil {
			report.UnavailableCosts = append(report.UnavailableCosts, c)
			continue
		}
		if i >= a.opts.Top {
			for side := range remaining {
				if err := remaining[side].merge(&routingTotals{a: e.pair[side]}); err != nil {
					return err
				}
			}
			continue
		}
		report.Contributors = append(report.Contributors, c)
	}
	report.UndisplayedBefore = agentValues(remaining[0].a.result(), totals[0])
	report.UndisplayedAfter = agentValues(remaining[1].a.result(), totals[1])
	r.Agents = report
	return nil
}
