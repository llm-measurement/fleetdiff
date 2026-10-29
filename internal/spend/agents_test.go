// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func agentOptions(t *testing.T) Options {
	o := routingOptions(t)
	o.RoutingAudit = false
	o.GroupBy, o.AgentSource, o.AgentTagPrefix = "agent", "request-tag", "agent:"
	return o
}

func TestAgentTagResolution(t *testing.T) {
	for _, tc := range []struct {
		raw, state, label string
		repeats, empty    uint64
	}{
		{`[]`, "unknown", "", 0, 0},
		{`["other:a","Agent:a"]`, "unknown", "", 0, 0},
		{`["agent:a","agent:a","agent:"]`, "known", "a", 1, 1},
		{`["agent:a","agent:b","agent:a"]`, "ambiguous", "", 1, 0},
		{`["agent:","agent: "]`, "unknown", "", 0, 2},
	} {
		r, err := resolveAgent(Row{Values: map[string]string{"request_tags": tc.raw}}, agentOptions(t))
		if err != nil || r.state != tc.state || r.label != tc.label || r.repeated != tc.repeats || r.emptySuffix != tc.empty {
			t.Fatal("tag resolution mismatch", tc, err)
		}
	}
	for _, raw := range []string{`{}`, `[null]`, `[1]`, `"agent:a"`, `["agent:a"`, `["agent:\ud800"]`, strings.Repeat("x", MaxFieldBytes+1)} {
		if _, err := resolveAgent(Row{Values: map[string]string{"request_tags": raw}}, agentOptions(t)); err == nil {
			t.Fatal("malformed tags accepted")
		}
	}
}

func agentFixtureRows() []map[string]string {
	var rows []map[string]string
	for side, day := range []string{"2026-09-01", "2026-09-02"} {
		for i, name := range []string{"alpha", "beta", "gamma"} {
			n := 100
			if side == 1 && i == 0 {
				n = 300
			}
			r := routingSample(fmt.Sprintf("%d-%d", side, i), day, n, 0)
			r["request_tags"] = `["agent:` + name + `","unrelated-private-tag"]`
			r["agent_id"], r["spend"] = "different-id", fmt.Sprint(n/100)
			rows = append(rows, r)
		}
	}
	for i, tags := range []string{`["unrelated-private-tag"]`, `["agent:alpha","agent:beta"]`} {
		r := routingSample(fmt.Sprint("extra", i), "2026-09-02", 50+i*20, 0)
		r["request_tags"], r["spend"] = tags, []string{"0.50", "0.70"}[i]
		rows = append(rows, r)
	}
	return rows
}

func TestAgentPartitionReconciles(t *testing.T) {
	rows, opts := agentFixtureRows(), agentOptions(t)
	r := routingReport(t, rows, opts)
	w := r.Agents.After
	if r.After.Tokens != 620 || r.After.RecordedSpend != "6.2" || w.Known.Tokens != 500 || w.Known.RecordedSpend != "5" || w.Unknown.Tokens != 50 || w.Unknown.RecordedSpend != "0.5" || w.Ambiguous.Tokens != 70 || w.Ambiguous.RecordedSpend != "0.7" {
		t.Fatal("exclusive partition did not reconcile")
	}
	if w.Coverage.KnownRows != 3 || w.Coverage.UnknownRows != 1 || w.Coverage.AmbiguousRows != 1 {
		t.Fatal("coverage mismatch")
	}
	opts.GroupBy, opts.AgentSource, opts.AgentTagPrefix = "key", "", ""
	base := routingReport(t, rows, opts)
	r.Before.Quality.MissingIdentity, r.After.Quality.MissingIdentity = 0, 0
	r.Before.AttributedTokens, r.After.AttributedTokens = base.Before.AttributedTokens, base.After.AttributedTokens
	r.Before.Distinct, r.After.Distinct = base.Before.Distinct, base.After.Distinct
	if !reflect.DeepEqual(r.Before, base.Before) || !reflect.DeepEqual(r.After, base.After) {
		t.Fatal("grouping changed base accounting")
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "unrelated-private-tag") || strings.Contains(string(data), "alpha") {
		t.Fatal("default agent labels leaked")
	}
}

func TestAgentColumnAbsenceAndSourceChoice(t *testing.T) {
	rows := agentFixtureRows()[:6]
	for _, row := range rows {
		delete(row, "request_tags")
	}
	o := agentOptions(t)
	r := routingReport(t, rows, o)
	if r.Agents.After.Coverage.ColumnAbsentRows != 3 || r.Agents.After.Unknown.Requests != 3 {
		t.Fatal("missing column was inferred")
	}
	o.AgentSource, o.AgentTagPrefix = "agent_id", ""
	r = routingReport(t, rows, o)
	if r.Agents.After.Known.Requests != 3 {
		t.Fatal("explicit agent_id not selected")
	}
}
