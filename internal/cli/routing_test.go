// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func routingCLIInput(t *testing.T, details bool) string {
	t.Helper()
	var rows []map[string]any
	for _, day := range []string{"2026-10-01", "2026-10-02"} {
		for i := 0; i < 6; i++ {
			r := map[string]any{
				"request_id": day + string(rune('a'+i)), "call_type": "acompletion",
				"startTime": day + "T12:00:00Z", "api_key": "ROUTING_PRIVATE_KEY",
				"user": "ROUTING_PRIVATE_USER", "session_id": "ROUTING_PRIVATE_SESSION",
				"agent_id": "ROUTING_PRIVATE_AGENT", "model": "ROUTING_PRIVATE_MODEL_A",
				"model_id": "ROUTING_PRIVATE_DEPLOYMENT", "prompt_tokens": 100,
				"completion_tokens": 20, "total_tokens": 120, "spend": "0.03", "status": "success",
			}
			if i == 2 {
				r["model"] = "ROUTING_PRIVATE_MODEL_B"
				r["model_group"] = "ROUTING_PRIVATE_RECOVERY_GROUP"
			}
			if i == 4 {
				r["call_type"], r["agent_id"] = "aresponses", nil
			}
			if i == 5 {
				r["call_type"] = "pass_through_endpoint"
			}
			if details {
				r["original_model_group"] = "ROUTING_PRIVATE_ROUTE"
				r["attempted_fallbacks"] = 0
				r["internal_call_origin"] = nil
				if i == 2 {
					r["attempted_fallbacks"] = 1
				}
				if i == 3 {
					r["original_model_group"] = "ROUTING_PRIVATE_OTHER_ROUTE"
				}
				if i == 4 {
					r["original_model_group"], r["attempted_fallbacks"] = nil, nil
				}
				if i == 5 {
					r["internal_call_origin"] = "autorouter_classifier"
				}
			}
			rows = append(rows, r)
		}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ROUTING_PRIVATE_PATH.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func routingCLIArgs(path string) []string {
	return []string{"investigate", "--litellm-spend", path, "--before-period", "2026-10-01", "--after-period", "2026-10-02", "--hash-secret-env", "ROUTING_CLI_SECRET"}
}

func TestRoutingDefaultBytesUnchanged(t *testing.T) {
	t.Setenv("ROUTING_CLI_SECRET", "synthetic-routing-comparison-secret-32-bytes")
	without, with := routingCLIInput(t, false), routingCLIInput(t, true)
	for _, format := range []string{"text", "json"} {
		var baseline, candidate, errs bytes.Buffer
		if code := Run(append(routingCLIArgs(without), "--format", format), &baseline, &errs); code != 0 {
			t.Fatal("baseline failed", code, errs.String())
		}
		if code := Run(append(routingCLIArgs(with), "--format", format), &candidate, &errs); code != 0 {
			t.Fatal("additive export failed", code, errs.String())
		}
		if !bytes.Equal(baseline.Bytes(), candidate.Bytes()) || errs.Len() != 0 {
			t.Fatal("routing columns changed unselected legacy output")
		}
	}
}

func TestRoutingCLIPrivacy(t *testing.T) {
	t.Setenv("ROUTING_CLI_SECRET", "synthetic-routing-comparison-secret-32-bytes")
	path := routingCLIInput(t, true)
	for _, mode := range [][]string{
		{"--routing-audit"},
		{"--routing-audit", "--routing-by", "route-agent"},
		{"--scope-route", "ROUTING_PRIVATE_ROUTE"},
		{"--scope-agent", "ROUTING_PRIVATE_AGENT", "--routing-audit"},
		{"--scope-agent", "ROUTING_PRIVATE_NO_MATCH", "--routing-audit"},
	} {
		for _, format := range []string{"text", "json"} {
			for _, show := range []bool{false, true} {
				args := append(routingCLIArgs(path), mode...)
				args = append(args, "--format", format)
				if show {
					args = append(args, "--show-hashes")
				}
				var out, errout bytes.Buffer
				if code := Run(args, &out, &errout); code != 0 {
					t.Fatal("routing report failed", code, errout.String())
				}
				if strings.Contains(out.String()+errout.String(), "ROUTING_PRIVATE") || strings.Contains(out.String()+errout.String(), os.Getenv("ROUTING_CLI_SECRET")) {
					t.Fatal("routing sentinel escaped report")
				}
				if format == "json" {
					var doc map[string]json.RawMessage
					if json.Unmarshal(out.Bytes(), &doc) != nil {
						t.Fatal("invalid report JSON")
					}
					key := "scope_coverage"
					if mode[0] == "--routing-audit" {
						key = "routing_audit"
					}
					if len(doc[key]) == 0 || string(doc[key]) == "null" {
						t.Fatal("requested routing evidence missing")
					}
				} else if !strings.Contains(out.String(), "Routing") && !strings.Contains(out.String(), "Scope") {
					t.Fatal("routing or scope section missing")
				}
			}
		}
	}
}

func TestRoutingCLIFlagIsolation(t *testing.T) {
	t.Setenv("ROUTING_CLI_SECRET", "synthetic-routing-comparison-secret-32-bytes")
	path := routingCLIInput(t, true)
	for _, extra := range [][]string{
		{"--routing-by", "route"},
		{"--routing-audit", "--routing-by", "ROUTING_PRIVATE_MODE"},
		{"--routing-audit", "--routing-by", ""},
		{"--scope-route", ""},
		{"--scope-agent", " "},
		{"--scope-route", "ROUTING_PRIVATE_ROUTE", "--scope-route", "ROUTING_PRIVATE_ROUTE"},
		{"--scope-agent", "ROUTING_PRIVATE_AGENT", "--scope-agent", "ROUTING_PRIVATE_AGENT"},
		{"--scope-route", "ROUTING_PRIVATE_ROUTE", "--scope-agent", "ROUTING_PRIVATE_AGENT"},
		{"--scope-route", "ROUTING_PRIVATE_ROUTE", "--litellm-daily-spend", "ROUTING_PRIVATE_DAILY.csv"},
		{"--scope-agent", "ROUTING_PRIVATE_AGENT", "--group-by", "session"},
		{"--routing-audit", "--period", "2026-10-01"},
		{"--routing-audit", "--session-budget-usd", "1"},
	} {
		var out, errs bytes.Buffer
		if code := Run(append(routingCLIArgs(path), extra...), &out, &errs); code != 2 || out.Len() != 0 || strings.Contains(errs.String(), "ROUTING_PRIVATE") {
			t.Fatal("invalid routing options were accepted or exposed input")
		}
	}
	for _, args := range [][]string{
		{"investigate", "--litellm-spend", path, "--scope-route", "ROUTING_PRIVATE_ROUTE"},
		{"investigate", "--before", "ROUTING_PRIVATE_BEFORE", "--after", "ROUTING_PRIVATE_AFTER", "--expected", "p", "--routing-audit"},
		{"compare", "--litellm-spend", path, "--routing-audit"},
	} {
		var out, errs bytes.Buffer
		if code := Run(args, &out, &errs); code != 2 || out.Len() != 0 || strings.Contains(errs.String(), "ROUTING_PRIVATE") {
			t.Fatal("routing crossed input or mode boundary")
		}
	}
}

func TestRoutingGuideExample(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "routing", "synthetic.csv")
	var out, errs bytes.Buffer
	args := []string{"investigate", "--litellm-spend", path, "--before-period", "2026-10-01", "--after-period", "2026-10-02", "--routing-audit"}
	if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
		t.Fatal("guide invocation failed", code, errs.String())
	}
	if !strings.HasPrefix(out.String(), "Routing audit:") {
		t.Fatal("opt-in routing question must lead its report")
	}
	guide, err := os.ReadFile(filepath.Join("..", "..", "docs", "ROUTING.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"  Model-request rows: 3 -> 3 requests; 360 -> 360 recorded tokens; recorded USD 0.0017 -> 0.0016.",
		"    claude-sonnet-5-5: requests 2 -> 1; tokens 240 -> 120; recorded USD 0.0012 -> 0.0006.",
		"      Cohort shares: requests 100% -> 50%; tokens 100% -> 50%; known recorded spend 100% -> 54.55%.",
		"  Classifier routing overhead: 1 -> 1 rows; raw recorded total tokens 21 -> 21; recorded USD 0.00005 -> 0.00005.",
	} {
		if !strings.Contains(out.String(), line+"\n") || !strings.Contains(string(guide), line+"\n") {
			t.Fatal("checked guide excerpt changed")
		}
	}
	for _, raw := range []string{"coding-default", "strategy-default", "recovery-route", "deployment-a", "deployment-b", "agent-a", "session-a", "key-a"} {
		if strings.Contains(out.String(), raw) {
			t.Fatal("sample raw identity escaped output")
		}
	}
}

func TestRoutingSharesDistinguishZeroFromUnknown(t *testing.T) {
	if routingShare(nil) != "unknown" {
		t.Fatal("missing denominator became a zero share")
	}
	for _, tc := range []struct{ value, want string }{
		{"0.000000000000", "0%"}, {"1.000000000000", "100%"},
		{"0.545454545455", "54.55%"}, {"0.500000000000", "50%"},
	} {
		if got := routingShare(&tc.value); got != tc.want {
			t.Fatalf("share %s: got %s, want %s", tc.value, got, tc.want)
		}
	}
}
