// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestAgentSpendChangeAndShares(t *testing.T) {
	rows := agentFixtureRows()
	o := agentOptions(t)
	o.RankBy = "recorded-spend-change"
	r := routingReport(t, rows, o)
	first := r.Agents.Contributors[0]
	if first.Item != "agent-1" || *first.Before.RecordedUSD != "1" || *first.After.RecordedUSD != "3" || *first.Delta != "2" || *first.Before.Share != "0.333333333333" || *first.After.Share != "0.483870967742" {
		t.Fatal("USD change or denominator wrong", first)
	}
	o.Top = 1
	top := routingReport(t, rows, o)
	if *top.Agents.Contributors[0].After.Share != *first.After.Share || top.Agents.UndisplayedAfter.Requests != 2 || *top.Agents.UndisplayedAfter.RecordedUSD != "2" {
		t.Fatal("top changes accounting or share")
	}
	// A low-token agent has the largest cost increase, not the largest after cost.
	rows = rows[:6]
	for i, row := range rows {
		row["spend"] = []string{"100", "1", "1", "100", "3", "2"}[i]
	}
	r = routingReport(t, rows, o)
	if r.Agents.Contributors[0].Item != "agent-2" || *r.Agents.Contributors[0].Delta != "2" {
		t.Fatal("ranked by after spend or token candidates")
	}
	for i, row := range rows {
		row["spend"] = []string{"0", "0", "0", "94", "3", "3"}[i]
	}
	r = routingReport(t, rows, o)
	if *r.Agents.Contributors[0].After.Share != "0.940000000000" || r.Agents.Contributors[0].Before.Share != nil {
		t.Fatal("94 percent or zero denominator wrong")
	}
}

func TestAgentSpendMissingAndDisclosure(t *testing.T) {
	rows := agentFixtureRows()
	delete(rows[0], "spend")
	rows[1]["spend"] = "invalid"
	o := agentOptions(t)
	r := routingReport(t, rows, o)
	for _, c := range r.Agents.Contributors {
		if c.Item == "agent-1" && (c.Before.RecordedUSD != nil || c.Delta != nil || c.Before.CostCoverage.Missing != 1) {
			t.Fatal("missing cost became zero")
		}
	}
	for _, flags := range [][2]bool{{true, false}, {false, true}} {
		o.ShowLabels, o.AcknowledgeSensitiveLabels = flags[0], flags[1]
		if _, err := Investigate(writeRows(t, rows), o); err == nil {
			t.Fatal("one disclosure flag accepted")
		}
	}
	o.ShowLabels, o.AcknowledgeSensitiveLabels = true, true
	r = routingReport(t, rows, o)
	b, _ := json.Marshal(r)
	if !r.Agents.LabelsIncluded || !strings.Contains(string(b), `"label":"alpha"`) {
		t.Fatal("selected label missing")
	}
	for _, secret := range []string{"different-id", "unrelated-private-tag", "PRIVATE_KEY", "PRIVATE_ROUTE"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("unselected value leaked")
		}
	}
}

func TestAgentTextMetadataDoesNotChangeJSON(t *testing.T) {
	r := routingReport(t, agentFixtureRows(), agentOptions(t))
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	r.Agents.LeadingContributor = &AgentContributor{Item: "agent-1", Label: "text-only", ProjectAlias: "project-1"}
	for i := range r.Agents.Contributors {
		r.Agents.Contributors[i].ProjectAlias = "project-1"
	}
	after, err := json.Marshal(r)
	if err != nil || string(before) != string(after) {
		t.Fatal("text-only metadata changed JSON", err)
	}
}

func TestAgentCapNamesPrefix(t *testing.T) {
	var rows []map[string]string
	for i := 0; i <= MaxAgents; i++ {
		r := routingSample(fmt.Sprint(i), "2026-09-01", 1, 0)
		r["request_tags"] = fmt.Sprintf(`["agent:%d"]`, i)
		rows = append(rows, r)
	}
	_, err := Investigate(writeRows(t, rows), agentOptions(t))
	if err == nil || !strings.Contains(err.Error(), `prefix "agent:"`) || !strings.Contains(err.Error(), "narrower") {
		t.Fatal("agent cap missing actionable selector", err)
	}
}

func TestAgentCostAggregateExceedsPerRowBound(t *testing.T) {
	rows := agentFixtureRows()[:6]
	for _, row := range rows {
		row["spend"] = "1000000000000000000"
	}
	r := routingReport(t, rows, agentOptions(t))
	if r.After.RecordedSpend != "3000000000000000000" || *r.Agents.Contributors[0].After.Share != "0.333333333333" {
		t.Fatal("aggregate cost reapplied per-row validation")
	}
}

func TestAgentUnavailableCostNeverHiddenByTop(t *testing.T) {
	rows := agentFixtureRows()[:6]
	for i, row := range rows {
		row["spend"] = []string{"", "1", "", "9", "2", ""}[i]
	}
	o := agentOptions(t)
	o.RankBy, o.Top = "recorded-spend-change", 1
	r := routingReport(t, rows, o)
	if len(r.Agents.Contributors) != 1 || r.Agents.Contributors[0].Item != "agent-2" || len(r.Agents.UnavailableCosts) != 2 || r.Agents.UndisplayedBefore.Requests != 0 || r.Agents.UndisplayedAfter.Requests != 0 {
		t.Fatal("unavailable agent hidden by --top", r.Agents)
	}
	for _, c := range r.Agents.UnavailableCosts {
		if c.Delta != nil || c.Before.RecordedUSD != nil || c.Before.CostCoverage.Missing != 1 {
			t.Fatal("unavailable cost became a zero delta", c)
		}
	}
	// With every cost unavailable, no agents are silently folded into a remainder.
	for _, row := range rows {
		row["spend"] = ""
	}
	r = routingReport(t, rows, o)
	if len(r.Agents.Contributors) != 0 || len(r.Agents.UnavailableCosts) != 3 || r.Agents.UndisplayedAfter.Requests != 0 {
		t.Fatal("all-unavailable ranking dropped agents")
	}
}
