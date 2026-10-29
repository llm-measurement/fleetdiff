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
