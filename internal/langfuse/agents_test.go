// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentTagsProjectScopeAndCoverage(t *testing.T) {
	root := t.TempDir()
	for _, project := range []string{"one", "two"} {
		body := strings.ReplaceAll(rowJSON("a", project, "2026-10-10"), `["PRIVATE_TAG"]`, `["agent:alpha","agent:alpha","agent:"]`)
		body += strings.ReplaceAll(rowJSON("b", project, "2026-10-17"), `["PRIVATE_TAG"]`, `["agent:alpha","agent:beta"]`)
		addRun(t, root, project, "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 2)
	}
	o := options()
	o.AgentSource, o.AgentTagPrefix = "request-tag", "agent:"
	r, err := Investigate(root, "agent", "", o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Before.Tokens != 280 || r.After.Tokens != 280 || r.Agents.Before.Known.Requests != 2 || r.Agents.Before.Coverage.RepeatedAgentTagRows != 2 || r.Agents.Before.Coverage.EmptySuffixTags != 2 || r.Agents.After.Ambiguous.Requests != 2 || len(r.Rankings.Movers) != 2 {
		t.Fatal("project or tag attribution lost", r.Agents)
	}
	if len(r.Agents.Contributors) != 2 || r.Agents.Contributors[0].Item == r.Agents.Contributors[1].Item {
		t.Fatal("projects merged in exact table")
	}
	o.ShowLabels, o.AcknowledgeSensitiveLabels = true, true
	shown, err := Investigate(root, "agent", "", o)
	if err != nil || shown.Agents.Contributors[0].Label != "alpha" || shown.Agents.Contributors[1].Label != "alpha" || shown.Before.Tokens != r.Before.Tokens {
		t.Fatal("Langfuse disclosure or scope changed", err)
	}
	if shown.Agents.Contributors[0].ProjectAlias != "project-1" || shown.Agents.Contributors[1].ProjectAlias != "project-2" {
		t.Fatal("project labels lack stable aliases", shown.Agents.Contributors)
	}
	data, _ := json.Marshal(shown.Agents)
	if strings.Contains(string(data), "project-") || strings.Contains(string(data), "ProjectAlias") || strings.Contains(string(data), "LeadingContributor") {
		t.Fatal("text-only disambiguation changed JSON")
	}
	for _, c := range r.Agents.Contributors {
		if c.ProjectAlias != "" || c.Label != "" {
			t.Fatal("undisclosed project metadata escaped")
		}
	}
	o.AgentSource, o.AgentTagPrefix = "agent_id", ""
	if _, err := Investigate(root, "agent", "", o); err == nil {
		t.Fatal("Langfuse agent_id inference accepted")
	}
}

func TestAgentTagDuplicateNormalizationAndValidation(t *testing.T) {
	root := t.TempDir()
	body := strings.ReplaceAll(rowJSON("a", "project", "2026-10-10"), `["PRIVATE_TAG"]`, `["agent:alpha", "other:b", "agent:alpha"]`)
	duplicate := strings.ReplaceAll(body, `["agent:alpha", "other:b", "agent:alpha"]`, `["other:b","agent:alpha","agent:alpha"]`)
	addRun(t, root, "project", "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body+duplicate, 2)
	o := options()
	o.AgentSource, o.AgentTagPrefix = "request-tag", "agent:"
	r, err := Investigate(root, "agent", "", o)
	if err != nil || r.Before.Requests != 1 || r.Coverage.Duplicates != 1 || r.Agents.Before.Coverage.RepeatedAgentTags != 1 {
		t.Fatal("duplicate tag normalization changed accounting", err)
	}
	root = t.TempDir()
	body = strings.ReplaceAll(rowJSON("a", "project", "2026-10-10"), `["PRIVATE_TAG"]`, `[null]`)
	addRun(t, root, "project", "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 1)
	if _, err := Investigate(root, "agent", "", o); err == nil {
		t.Fatal("malformed observation tag array accepted")
	}
}
