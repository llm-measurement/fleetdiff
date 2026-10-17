// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func whatIfRoutingFile(t *testing.T, value any) string {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-input-sentinel.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func whatIfRoutingRow(id string) map[string]any {
	return map[string]any{
		"request_id": id, "call_type": "acompletion", "status": "failure",
		"startTime": "2020-01-02T12:00:00Z", "endTime": "2020-01-02T12:00:01Z",
		"prompt_tokens": 100, "completion_tokens": 50, "total_tokens": 150,
		"cache_read_input_tokens": 20, "cache_write_input_tokens": 10,
		"model": "claude-sonnet-5-5", "model_id": "private-deployment-sentinel",
		"model_group": "private-recovery-group-sentinel", "original_model_group": "private-route-sentinel",
		"agent_id": "private-agent-sentinel", "api_key": "private-key-sentinel", "team_id": "private-team-sentinel",
	}
}

func whatIfRoutingArgs(path string) []string {
	return []string{"what-if", "--litellm-spend", path, "--period", "2020-01-02", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5"}
}

func TestWhatIfRoutingScopesCoverageAndFailures(t *testing.T) {
	for _, kind := range []string{"route", "agent"} {
		t.Run(kind, func(t *testing.T) {
			column := "original_model_group"
			if kind == "agent" {
				column = "agent_id"
			}
			rows := []map[string]any{whatIfRoutingRow("match"), whatIfRoutingRow("nonmatch"), whatIfRoutingRow("missing"), whatIfRoutingRow("not-selected"), whatIfRoutingRow("no-cache"), whatIfRoutingRow("unsupported")}
			rows[1][column] = "private-other-sentinel"
			rows[2][column], rows[2]["call_type"] = nil, "aresponses"
			rows[3]["model_id"] = nil
			rows[4]["cache_write_input_tokens"] = nil
			rows[5]["call_type"] = "private-call-type-sentinel"
			path := whatIfRoutingFile(t, rows)
			args := append(whatIfRoutingArgs(path), "--scope-"+kind, "private-"+kind+"-sentinel")
			for _, format := range []string{"json", "text"} {
				var out, errs bytes.Buffer
				if code := Run(append(append([]string{}, args...), "--format", format), &out, &errs); code != 0 || errs.Len() != 0 {
					t.Fatal("routing what-if did not complete")
				}
				if strings.Contains(out.String()+errs.String(), "sentinel") {
					t.Fatal("routing output exposed raw identities or paths")
				}
				if format == "text" {
					for _, want := range []string{"unknown served identity: 1", "missing scope identity: 1", "Source attribution before pricing exclusions:", "not provider-confirmed", "aresponses: 1 rows", "Priced failure subset: 2 records"} {
						if !strings.Contains(out.String(), want) {
							t.Fatal("text omitted routing eligibility or coverage")
						}
					}
					continue
				}
				var got reprice.Report
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Source.Rows != 6 || got.Source.Attribution == nil || got.Source.Attribution.Rows != 6 ||
					got.Source.Attribution.Deployment.Unknown != 1 || got.Covered != 2 || got.Moves[0].Covered != 1 ||
					!reflect.DeepEqual(got.Excluded, map[string]uint64{"missing_scope_identity": 1, "unknown_served_identity": 1, "missing_split": 1, "unsupported_call_type": 1}) {
					t.Fatal("pricing or pre-exclusion coverage lost a source row")
				}
				if got.PricedFailures == nil || got.PricedFailures.Records != 2 || got.PricedFailures.Total.String() != "300" ||
					!reflect.DeepEqual(got.PricedFailures.Before, got.Before) || !reflect.DeepEqual(got.PricedFailures.Level1, got.Level1) ||
					!reflect.DeepEqual(got.PricedFailures.Level2, got.Level2) || !reflect.DeepEqual(got.PricedFailures.Envelope, got.Envelope) {
					t.Fatal("failure costs did not use the same filtered, original-volume calculation")
				}
			}
		})
	}
}

func TestWhatIfRoutingMapsAndScenarios(t *testing.T) {
	path := whatIfRoutingFile(t, []map[string]any{whatIfRoutingRow("one")})
	for _, kind := range []string{"route", "agent"} {
		selector := reprice.Selector{Kind: kind, Value: "private-" + kind + "-sentinel"}
		mapping := whatIfRoutingFile(t, reprice.WorkloadMap{Version: 1, Mappings: []reprice.Mapping{{Selector: selector, Workload: "qa"}}})
		for _, scenario := range []bool{false, true} {
			args := whatIfRoutingArgs(path)
			if scenario {
				config := whatIfRoutingFile(t, reprice.Scenario{Version: 1, Moves: []reprice.Move{{From: "claude-sonnet-5-5", To: "gpt-6.1-sol", Fraction: "0.5", Scope: &selector}}})
				args = append(args[:5], "--scenario", config)
			}
			args = append(args, "--workload-map", mapping, "--format", "json")
			var out, errs bytes.Buffer
			if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
				t.Fatal("routing scenario or map was rejected")
			}
			var got reprice.Report
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Source.Attribution == nil || got.Covered != 1 || got.Unmapped != 0 || len(got.Level2) != 1 || got.Level2[0].Workload != "mapped" || got.Moves[0].Scoped != scenario {
				t.Fatal("routing map did not activate the selected-identity contract")
			}
			if strings.Contains(out.String()+errs.String(), "sentinel") {
				t.Fatal("scenario or mapping selector leaked")
			}
		}
	}
}

func TestWhatIfRoutingScopeFlagsRejectAmbiguityPrivately(t *testing.T) {
	base := whatIfRoutingArgs("private-path-sentinel")
	for _, kind := range []string{"key", "team", "route", "agent"} {
		for _, flags := range [][]string{
			{"--scope-" + kind, ""}, {"--scope-" + kind, " \t "},
			{"--scope-" + kind, "private-selector-sentinel", "--scope-" + kind, "private-selector-sentinel"},
			{"--scope-" + kind, "private-selector-sentinel", "--scope-agent", "private-other-sentinel"},
			{"--scope-" + kind, "private-selector-sentinel", "--scenario", "private-scenario-sentinel"},
		} {
			var out, errs bytes.Buffer
			if code := Run(append(append([]string{}, base...), flags...), &out, &errs); code != 2 || out.Len() != 0 || errs.Len() == 0 || strings.Contains(errs.String(), "sentinel") {
				t.Fatal("ambiguous scope flags accepted, produced partial output or leaked")
			}
		}
	}
}

func TestWhatIfRoutingSummaryRejectionAndLateInputErrors(t *testing.T) {
	for _, flag := range []string{"--scope-route", "--scope-agent", "--workload-map"} {
		args := []string{"what-if", "--summaries", "private-summary-sentinel", "--expected", "app", "--period", "2020-01-02", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5", flag, "private-selector-sentinel"}
		var out, errs bytes.Buffer
		if code := Run(args, &out, &errs); code != 2 || out.Len() != 0 || !strings.Contains(errs.String(), "intersections") || strings.Contains(errs.String(), "sentinel") {
			t.Fatal("summary scope/map rejection changed or leaked")
		}
	}
	for _, late := range []string{"duplicate", "malformed"} {
		path := whatIfRoutingFile(t, []map[string]any{whatIfRoutingRow("same"), whatIfRoutingRow("same")})
		if late == "malformed" {
			body, _ := json.Marshal(whatIfRoutingRow("one"))
			if err := os.WriteFile(path, append(append([]byte("["), body...), []byte(",private-value-sentinel]")...), 0600); err != nil {
				t.Fatal(err)
			}
		}
		var out, errs bytes.Buffer
		args := append(whatIfRoutingArgs(path), "--scope-route", "private-route-sentinel")
		if code := Run(args, &out, &errs); code != 1 || out.Len() != 0 || errs.Len() == 0 || strings.Contains(errs.String(), "sentinel") {
			t.Fatal("late routing error published partial costs or raw values")
		}
	}
}

func TestWhatIfRoutingColumnsDoNotChangeLegacyBytes(t *testing.T) {
	row := whatIfRoutingRow("one")
	base := whatIfRoutingFile(t, []map[string]any{row})
	delete(row, "original_model_group")
	delete(row, "model_id")
	delete(row, "agent_id")
	old := whatIfRoutingFile(t, []map[string]any{row})
	for _, format := range []string{"text", "json"} {
		for _, kind := range []string{"", "key", "team"} {
			var want string
			for i, path := range []string{base, old} {
				args := append(whatIfRoutingArgs(path), "--format", format)
				if kind != "" {
					args = append(args, "--scope-"+kind, "private-"+kind+"-sentinel")
				}
				var out, errs bytes.Buffer
				if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
					t.Fatal("legacy invocation failed on an additive export")
				}
				if i == 0 {
					want = out.String()
				} else if out.String() != want {
					t.Fatal("additive routing columns changed legacy output bytes")
				}
				if strings.Contains(out.String(), "attribution_coverage") || strings.Contains(out.String(), "Source attribution") {
					t.Fatal("legacy output opted into attribution coverage")
				}
			}
		}
	}
}

func TestWhatIfRoutingPublicFixture(t *testing.T) {
	for _, tc := range []struct {
		name, flag, value string
		moved             uint64
		level1            string
	}{
		{"original-route", "--scope-route", "coding-default", 1, "0.000610000"},
		{"agent", "--scope-agent", "agent-a", 1, "0.000610000"},
		{"recovery-label-is-not-original-route", "--scope-route", "recovery-route", 0, "0.000800000"},
		{"nonmatching-agent", "--scope-agent", "agent-unmatched", 0, "0.000800000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"what-if", "--litellm-spend", "../../examples/routing/synthetic.csv", "--period", "2026-10-02",
				"--from", "gpt-6.1-sol", "--to", "gpt-6-luna", "--fraction", "0.5", tc.flag, tc.value, "--format", "json"}
			var out, errs bytes.Buffer
			if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
				t.Fatal("public routing fixture could not be repriced")
			}
			var got reprice.Report
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Source.Rows != 4 || got.Source.Outside != 4 || got.Covered != 2 || got.Moves[0].Covered != tc.moved ||
				got.Before == nil || *got.Before != "0.000800000" || got.Level1 == nil || *got.Level1 != tc.level1 ||
				!reflect.DeepEqual(got.Excluded, map[string]uint64{"missing_split": 1, "unsupported_call_type": 1}) {
				t.Fatal("public fixture needs complete endTime for priced rows; classifier and missing Responses splits must stay excluded")
			}
			a := got.Source.Attribution
			if a == nil || a.Rows != 4 || a.Agent.Known != 2 || a.Agent.Unknown != 2 || a.Route.Known != 4 || len(a.ByCallType) != 3 ||
				a.ByCallType[0].CallType != "acompletion" || a.ByCallType[0].Rows != 2 ||
				a.ByCallType[1].CallType != "aresponses" || a.ByCallType[1].Agent.Unknown != 1 ||
				a.ByCallType[2].CallType != "pass_through_endpoint" || a.ByCallType[2].Rows != 1 || a.ByCallType[2].Agent.Unknown != 1 {
				t.Fatal("scope hid unknown agent coverage or merged classifier rows into model usage")
			}
			if got.Unmapped != tc.moved || tc.moved > 0 && len(got.Level2) != 3 {
				t.Fatal("route label spelling was treated as a workload classification")
			}
			for _, raw := range []string{"coding-default", "strategy-default", "recovery-route", "agent-a", "agent-unmatched", "deployment-", "session-a", "key-a"} {
				if strings.Contains(out.String()+errs.String(), raw) {
					t.Fatal("public fixture identity escaped pseudonymous reporting")
				}
			}
		})
	}
}
