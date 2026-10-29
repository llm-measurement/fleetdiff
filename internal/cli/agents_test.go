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
					for _, want := range []string{
						"agent-1 accounts for 62.5% of the net recorded increase (+200 of +320 tokens)",
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
