// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func agentCLIArgs(path string) []string {
	return []string{"investigate", "--litellm-spend", path, "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--group-by", "agent", "--agent-source", "request-tag", "--agent-tag-prefix", "agent:", "--rank-by", "recorded-spend-change", "--hash-secret-env", "AGENT_CLI_SECRET"}
}

func TestAgentCLIFormatsAndPrivacy(t *testing.T) {
	t.Setenv("AGENT_CLI_SECRET", "synthetic-agent-secret-for-tests-at-least-32-bytes")
	data, err := os.ReadFile("../../examples/litellm-spend/agent-tags.csv")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]string
	for _, row := range rows[1:] {
		r := map[string]string{}
		for i, k := range rows[0] {
			r[k] = row[i]
		}
		records = append(records, r)
	}
	var jsonl bytes.Buffer
	for _, r := range records {
		if json.NewEncoder(&jsonl).Encode(r) != nil {
			t.Fatal("encode fixture")
		}
	}
	array, _ := json.Marshal(records)
	var baseline string
	for _, input := range []struct {
		ext  string
		data []byte
	}{{"csv", data}, {"json", array}, {"jsonl", jsonl.Bytes()}} {
		path := filepath.Join(t.TempDir(), "agents."+input.ext)
		if err := os.WriteFile(path, input.data, 0600); err != nil {
			t.Fatal(err)
		}
		for _, format := range []string{"text", "json"} {
			for _, disclose := range []bool{false, true} {
				args := append(agentCLIArgs(path), "--format", format)
				if disclose {
					args = append(args, "--show-labels", "--acknowledge-sensitive-labels", "--routing-audit")
				}
				code, out, errs := databaseRun(args, nil)
				if code != 0 || errs != "" {
					t.Fatal("agent report failed", code, errs)
				}
				for _, secret := range []string{"other:private", "different-id", "before-a", path} {
					if strings.Contains(out+errs, secret) {
						t.Fatal("unselected identity leaked")
					}
				}
				if strings.Contains(out, "alpha") != disclose {
					t.Fatal("label disclosure mismatch")
				}
				if format == "json" {
					var r spend.Report
					if json.Unmarshal([]byte(out), &r) != nil || r.Agents.After.Coverage.RepeatedAgentTagRows != 1 || r.Agents.After.Coverage.EmptySuffixRows != 1 || r.After.RecordedSpend != "6.2" {
						t.Fatal("partition/coverage mismatch")
					}
					if !disclose {
						if baseline == "" {
							baseline = out
						} else if out != baseline {
							t.Fatal("source format changed report")
						}
					}
					if disclose && r.RoutingAudit.ConfigurationLabelsIncluded {
						t.Fatal("agent disclosure expanded to routing labels")
					}
				} else if !strings.Contains(out, "USD change") || !strings.Contains(out, "Share after") || disclose && !strings.Contains(out, "review before sharing") {
					t.Fatal("required columns or warning missing")
				}
				if format == "text" {
					answer := "agent-1"
					if disclose {
						answer += ` "alpha"`
					}
					for _, want := range []string{
						answer + " accounts for 62.5% of the net recorded increase (+200 of +320 tokens)",
						"Unknown (not ranked): 0 -> 1 rows; token change +50; recorded USD change 0.5",
						"Ambiguous (not ranked): 0 -> 1 rows; token change +70; recorded USD change 0.7",
					} {
						if !strings.Contains(out, want) {
							t.Fatal("answer or unattributed change missing", want)
						}
					}
				}
			}
		}
	}
}

func TestAgentUnavailableCostReproduction(t *testing.T) {
	t.Setenv("AGENT_CLI_SECRET", "synthetic-agent-secret-for-tests-at-least-32-bytes")
	var input bytes.Buffer
	w := csv.NewWriter(&input)
	_ = w.Write([]string{"request_id", "call_type", "startTime", "endTime", "status", "model", "prompt_tokens", "completion_tokens", "total_tokens", "spend", "agent_id", "request_tags"})
	for i, row := range []struct{ day, agent, tokens, cost string }{
		{"07", "alpha", "100", ""}, {"07", "beta", "100", "1"},
		{"08", "alpha", "900", "9"}, {"08", "beta", "100", "2"},
		{"08", "newcomer", "50", "0.50"},
	} {
		start := "2026-10-" + row.day + "T12:00:00Z"
		_ = w.Write([]string{string(rune('a' + i)), "completion", start, start, "success", "private-model", row.tokens, "0", row.tokens, row.cost, row.agent, "[]"})
	}
	w.Flush()
	path := filepath.Join(t.TempDir(), "unavailable.csv")
	if err := os.WriteFile(path, input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"investigate", "--litellm-spend", path, "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--group-by", "agent", "--agent-source", "agent_id", "--rank-by", "recorded-spend-change", "--top", "1", "--hash-secret-env", "AGENT_CLI_SECRET"}
	for _, disclose := range []bool{false, true} {
		for _, format := range []string{"text", "json"} {
			a := append(append([]string{}, args...), "--format", format)
			if disclose {
				a = append(a, "--show-labels", "--acknowledge-sensitive-labels")
			}
			code, out, errs := databaseRun(a, nil)
			if code != 0 || errs != "" || strings.Contains(out, "alpha") != disclose {
				t.Fatal("unavailable reproduction failed or leaked", code, errs)
			}
			if format == "json" {
				var r spend.Report
				if json.Unmarshal([]byte(out), &r) != nil || len(r.Agents.Contributors) != 1 || r.Agents.Contributors[0].Item != "agent-2" || len(r.Agents.UnavailableCosts) != 1 || r.Agents.UnavailableCosts[0].Item != "agent-1" || r.Agents.UnavailableCosts[0].After.Tokens-r.Agents.UnavailableCosts[0].Before.Tokens != 800 || r.Agents.UndisplayedAfter.Tokens != 50 {
					t.Fatal("unavailable agent hidden in JSON")
				}
			} else if !strings.Contains(out, "USD change unavailable (not ranked; shown regardless of --top)") || !strings.Contains(out, "agent-1: token change +800; recorded USD change unavailable") {
				t.Fatal("unavailable agent hidden in text")
			}
		}
	}
}

func TestAgentNonStringTagFormats(t *testing.T) {
	t.Setenv("AGENT_CLI_SECRET", "synthetic-agent-secret-for-tests-at-least-32-bytes")
	for _, raw := range []string{`["agent:PRIVATE_INVALID_LABEL",null]`, `[1,false,{},[]]`} {
		rows := []map[string]string{}
		for _, day := range []string{"07", "08"} {
			rows = append(rows, map[string]string{"request_id": day, "call_type": "completion", "startTime": "2026-10-" + day + "T12:00:00Z", "endTime": "2026-10-" + day + "T12:00:01Z", "status": "success", "model": "private-model", "prompt_tokens": "100", "completion_tokens": "0", "total_tokens": "100", "spend": "1", "request_tags": raw})
		}
		var csvData, jsonl bytes.Buffer
		writer := csv.NewWriter(&csvData)
		keys := []string{"request_id", "call_type", "startTime", "endTime", "status", "model", "prompt_tokens", "completion_tokens", "total_tokens", "spend", "request_tags"}
		_ = writer.Write(keys)
		for _, row := range rows {
			values := []string{}
			for _, k := range keys {
				values = append(values, row[k])
			}
			_ = writer.Write(values)
			_ = json.NewEncoder(&jsonl).Encode(row)
		}
		writer.Flush()
		array, _ := json.Marshal(rows)
		for ext, data := range map[string][]byte{"csv": csvData.Bytes(), "json": array, "jsonl": jsonl.Bytes()} {
			path := filepath.Join(t.TempDir(), "non-string."+ext)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			for _, format := range []string{"json", "text"} {
				code, out, errs := databaseRun(append(agentCLIArgs(path), "--format", format, "--show-labels", "--acknowledge-sensitive-labels"), nil)
				if code != 0 || errs != "" || strings.Contains(out, "PRIVATE_INVALID_LABEL") {
					t.Fatal("non-string tag rejected or label salvaged", code, errs)
				}
				if format == "json" {
					var r spend.Report
					if json.Unmarshal([]byte(out), &r) != nil || r.Agents.After.Coverage.NonStringTagRows != 1 || r.Agents.After.Unknown.Tokens != 100 || r.After.RecordedSpend != "1" {
						t.Fatal("non-string coverage or totals wrong")
					}
				} else if !strings.Contains(out, "Non-string tag elements: 1 rows counted as unknown identity") {
					t.Fatal("non-string coverage not explained")
				}
			}
		}
	}
}

func TestAgentCLIRejectsBeforeDatabaseDispatch(t *testing.T) {
	for _, flags := range [][]string{
		{"--group-by", "agent"},
		{"--group-by", "agent", "--agent-source", "request-tag"},
		{"--group-by", "agent", "--agent-source", "end-user"},
		{"--group-by", "agent", "--agent-source", "agent_id", "--agent-tag-prefix", "PRIVATE_PREFIX"},
		{"--group-by", "agent", "--agent-source", "agent_id", "--rank-by", "recorded-spend"},
		{"--group-by", "agent", "--agent-source", "request-tag", "--agent-tag-prefix", "agent:", "--request-tags=false"},
		{"--show-labels"}, {"--acknowledge-sensitive-labels"},
		{"--show-labels", "--acknowledge-sensitive-labels", "--group-by", "end-user"},
	} {
		called := false
		code, out, errs := databaseRun(append([]string{"investigate", "--litellm-db"}, flags...), func(context.Context, exportdata.Options) (exportdata.Result, error) {
			called = true
			return exportdata.Result{}, nil
		})
		if code != 2 || called || out != "" || strings.Contains(errs, "PRIVATE_PREFIX") {
			t.Fatal("invalid selector reached DB or leaked", code, errs)
		}
	}
}

func TestAgentTextEscapesSelectedLabel(t *testing.T) {
	var b bytes.Buffer
	renderAgents(&b, &spend.AgentReport{Contributors: []spend.AgentContributor{{Item: "agent-1", Label: "name\n\x1b[31m\u202eend"}}})
	if !strings.Contains(b.String(), `"name\n\x1b[31m\u202eend"`) || strings.ContainsAny(b.String(), "\x1b\u202e") {
		t.Fatal("label changed terminal structure")
	}
}

func TestAgentIDTextPolishAndEscapedAnswer(t *testing.T) {
	t.Setenv("AGENT_CLI_SECRET", "synthetic-agent-secret-for-tests-at-least-32-bytes")
	label := "alpha\n\x1b[31m\u202e"
	var input bytes.Buffer
	w := csv.NewWriter(&input)
	_ = w.Write([]string{"request_id", "call_type", "startTime", "endTime", "status", "model", "prompt_tokens", "completion_tokens", "total_tokens", "spend", "agent_id", "request_tags"})
	for i, row := range []struct{ day, agent, tokens, cost string }{
		{"07", label, "100", "1"}, {"07", "beta", "100", "1"},
		{"08", label, "300", "3"}, {"08", "beta", "100", "100"}, {"08", "", "50", "0.5"},
	} {
		start := "2026-10-" + row.day + "T12:00:00Z"
		_ = w.Write([]string{string(rune('a' + i)), "completion", start, start, "success", "private-model", row.tokens, "0", row.tokens, row.cost, row.agent, `["agent:x","agent:y"]`})
	}
	w.Flush()
	path := filepath.Join(t.TempDir(), "agents.csv")
	if err := os.WriteFile(path, input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"investigate", "--litellm-spend", path, "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--group-by", "agent", "--agent-source", "agent_id", "--rank-by", "recorded-spend-change", "--top", "1", "--hash-secret-env", "AGENT_CLI_SECRET", "--show-labels", "--acknowledge-sensitive-labels"}
	code, out, errs := databaseRun(args, nil)
	if code != 0 || errs != "" {
		t.Fatal(code, errs)
	}
	for _, want := range []string{`agent-1 "alpha\n\x1b[31m\u202e" accounts for`, "Agent identity unknown or ambiguous: 0 -> 1.", "All displayed agents have usable recorded USD in both periods."} {
		if !strings.Contains(out, want) {
			t.Fatal("agent polish missing", want, out)
		}
	}
	for _, forbidden := range []string{"\x1b", "\u202e", "without a matching tag", "Repeated agent tags", "empty suffixes", "Ambiguous (not ranked)", "; ambiguous:", "Group identities missing", "Missing rows", "Zero-origin-unknown rows"} {
		if strings.Contains(out, forbidden) {
			t.Fatal("agent_id text includes inapplicable coverage or unsafe label", forbidden)
		}
	}
	code, data, errs := databaseRun(append(args, "--format", "json"), nil)
	var r spend.Report
	if code != 0 || errs != "" || json.Unmarshal([]byte(data), &r) != nil {
		t.Fatal("JSON failed", code, errs)
	}
	for _, field := range []string{`"ambiguous_rows": 0`, `"no_matching_tag_rows": 0`, `"empty_suffix_rows": 0`} {
		if !strings.Contains(data, field) {
			t.Fatal("JSON coverage was removed", field)
		}
	}
	if r.Agents.Contributors[0].Item != "agent-2" {
		t.Fatal("test must keep the token leader outside the displayed cost ranking")
	}
}

func TestAgentIncreaseNamesPositiveMoverWithOffsets(t *testing.T) {
	t.Setenv("AGENT_CLI_SECRET", "synthetic-agent-secret-for-tests-at-least-32-bytes")
	var input bytes.Buffer
	w := csv.NewWriter(&input)
	_ = w.Write([]string{"request_id", "call_type", "startTime", "endTime", "status", "model", "prompt_tokens", "completion_tokens", "total_tokens", "spend", "agent_id"})
	for i, row := range []struct{ day, agent, tokens string }{
		{"07", "alpha", "1000"}, {"07", "beta", "0"},
		{"08", "alpha", "300"}, {"08", "beta", "600"}, {"08", "", "200"},
	} {
		start := "2026-10-" + row.day + "T12:00:00Z"
		_ = w.Write([]string{string(rune('a' + i)), "completion", start, start, "success", "private-model", row.tokens, "0", row.tokens, "0", row.agent})
	}
	w.Flush()
	path := filepath.Join(t.TempDir(), "agents.csv")
	if err := os.WriteFile(path, input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, labels := range []bool{false, true} {
		args := []string{"investigate", "--litellm-spend", path, "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--group-by", "agent", "--agent-source", "agent_id", "--hash-secret-env", "AGENT_CLI_SECRET"}
		name := "agent-2"
		if labels {
			args = append(args, "--show-labels", "--acknowledge-sensitive-labels")
			name += ` "beta"`
		}
		code, out, errs := databaseRun(args, nil)
		want := name + " added 600 tokens while others fell by 500 (net +100)."
		if code != 0 || errs != "" || !strings.Contains(out, want) || strings.Contains(out, "600%") {
			t.Fatal("wrong increase attribution", code, errs, out)
		}
		code, data, errs := databaseRun(append(args, "--format", "json"), nil)
		var r spend.Report
		if code != 0 || errs != "" || json.Unmarshal([]byte(data), &r) != nil {
			t.Fatal("JSON failed", code, errs)
		}
		if r.Before.Tokens != 1000 || r.After.Tokens != 1100 || r.Increase.Count != 1 || r.Increase.Delta.Lower != 600 || r.Increase.Delta.Upper != 600 || r.Increase.Share.Lower != 6 || r.Rankings.Movers[0].Item != "agent-1" || r.Rankings.Movers[0].Delta.Lower != -700 {
			t.Fatal("text correction changed JSON accounting or rankings", data)
		}
	}
}

func TestAgentCostColumnsOnlyForDisplayedIncompleteCosts(t *testing.T) {
	usd := "1"
	value := spend.AgentValues{RecordedUSD: &usd, CostCoverage: spend.AgentCostCoverage{Usable: 1}}
	for _, kind := range []string{"complete", "missing", "invalid", "zero-origin", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			c := spend.AgentContributor{Item: "agent-1", Before: value, After: value, Delta: &usd}
			r := &spend.AgentReport{Source: "agent_id", Contributors: []spend.AgentContributor{c}}
			r.UndisplayedAfter.CostCoverage.Missing = 99
			r.After.Unknown.Quality.SpendMissing = 10
			switch kind {
			case "missing":
				r.Contributors[0].Before.CostCoverage.Missing = 1
			case "invalid":
				r.Contributors[0].After.CostCoverage.Invalid = 1
			case "zero-origin":
				r.Contributors[0].After.CostCoverage.ZeroUnknown = 1
			case "unavailable":
				c.Item, c.Delta, c.Before.RecordedUSD = "agent-2", nil, nil
				c.Before.CostCoverage.Missing = 1
				r.UnavailableCosts = []spend.AgentContributor{c}
			}
			var out bytes.Buffer
			renderAgents(&out, r)
			for _, column := range []string{"Usable rows", "Missing rows", "Invalid rows", "Zero-origin-unknown rows"} {
				if strings.Contains(out.String(), column) != (kind != "complete") {
					t.Fatal("coverage columns did not follow displayed rows", column, out.String())
				}
			}
			if strings.Contains(out.String(), "All displayed agents have usable recorded USD in both periods.") != (kind == "complete") {
				t.Fatal("complete-cost line mismatch")
			}
		})
	}
}

func TestAgentTextProjectAliases(t *testing.T) {
	var out bytes.Buffer
	renderAgents(&out, &spend.AgentReport{Source: "request-tag", LabelsIncluded: true,
		Contributors: []spend.AgentContributor{
			{Item: "agent-1", Label: "alpha", ProjectAlias: "project-1"},
			{Item: "agent-2", Label: "alpha", ProjectAlias: "project-2"},
		}})
	for _, want := range []string{`agent-1 "alpha" (project-1)`, `agent-2 "alpha" (project-2)`} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("identical project-scoped labels are ambiguous", out.String())
		}
	}
}
