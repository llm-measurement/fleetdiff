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

func TestSessionCLIResultFirstAndPrivacy(t *testing.T) {
	rows := []map[string]any{}
	for i, id := range []string{"session-secret-a", "session-secret-a", "session-secret-b", "session-secret-c", "session-secret-d"} {
		rows = append(rows, map[string]any{"request_id": string(rune('a' + i)), "call_type": "completion", "startTime": "2026-09-02T12:00:00Z", "endTime": "2026-09-02T12:00:01Z", "prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120, "spend": []string{"0.8", "0.4", "2", "3", "0.5"}[i], "status": "success", "session_id": id, "api_key": "sentinel-key", "agent_id": "sentinel-agent-private", "team_id": "sentinel-team", "rejection_kind": nil})
	}
	path := filepath.Join(t.TempDir(), "sentinel-file.json")
	b, _ := json.Marshal(rows)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"investigate", "--litellm-spend", path, "--group-by", "session", "--period", "2026-09-02", "--session-budget-usd", "1"}
	for _, format := range []string{"text", "json"} {
		for _, show := range []bool{false, true} {
			args := append(append([]string{}, base...), "--format", format)
			if show {
				args = append(args, "--show-hashes")
			}
			var out, errout bytes.Buffer
			if code := Run(args, &out, &errout); code != 0 {
				t.Fatalf("%d %s", code, errout.String())
			}
			if format == "text" {
				if !strings.HasPrefix(out.String(), "3 of 4 sessions with complete recorded spend exceed the supplied USD 1.00 cap.\nGrouped by session ID alone") {
					t.Fatal(out.String())
				}
				if strings.Contains(out.String(), "clipped") {
					t.Fatal(out.String())
				}
				for _, s := range []string{"Wed 2026-09-02", "Thu 2026-09-03", "p50 1.20; p95 3.00; max 3.00 (n=4", "Above-cap sessions with no recorded session-budget rejection: 3", "failure logging completeness is unknown"} {
					if !strings.Contains(out.String(), s) {
						t.Fatal(s, out.String())
					}
				}
			}
			for _, sentinel := range []string{"session-secret", "sentinel-key", "sentinel-agent-private", "sentinel-team", path} {
				if strings.Contains(out.String()+errout.String(), sentinel) {
					t.Fatal("private identity leaked")
				}
			}
		}
	}
	rows[0]["rejection_kind"] = "sentinel-agent-private"
	b, _ = json.Marshal(rows)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errout bytes.Buffer
	if code := Run(base, &out, &errout); code == 0 || out.Len() != 0 || strings.Contains(errout.String(), "sentinel") {
		t.Fatal(code, out.String(), errout.String())
	}
}

func TestSessionFlagIsolation(t *testing.T) {
	for _, extra := range [][]string{
		{"--group-by", "key", "--period", "2026-09-02"},
		{"--group-by", "user", "--session-budget-usd", "1"},
		{"--group-by", "session", "--session-counter-window", "5s"},
		{"--group-by", "session", "--period", ""},
		{"--group-by", "session", "--session-budget-usd", ""},
		{"--group-by", "session", "--period", "2026-09-02", "--before-period", "2026-09-01"},
		{"--group-by", "session", "--period", "2026-09-02", "--after-period", "2026-09-03"},
		{"--group-by", "session", "--period", "2026-09-02", "--session-counter-window", "0s"},
	} {
		args := append([]string{"investigate", "--litellm-spend", "sentinel.csv"}, extra...)
		var out, errout bytes.Buffer
		if code := Run(args, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "sentinel") {
			t.Fatal(args, code, errout.String())
		}
	}
}

func TestSessionImplicitGroupingAndDefaultSpan(t *testing.T) {
	path := "../../examples/litellm-spend/session-budget.csv"
	for _, options := range [][]string{{"--session-budget-usd", "1"}, {"--period", "2026-10-03"}} {
		base := append([]string{"investigate", "--litellm-spend", path}, options...)
		var implicit, explicit, errout bytes.Buffer
		if code := Run(base, &implicit, &errout); code != 0 {
			t.Fatal(code, errout.String())
		}
		if code := Run(append(base, "--group-by", "session"), &explicit, &errout); code != 0 {
			t.Fatal(code, errout.String())
		}
		if implicit.String() != explicit.String() {
			t.Fatal("explicit grouping changed output")
		}
		if options[0] == "--session-budget-usd" && (!strings.Contains(implicit.String(), "file's observed span") || !strings.Contains(implicit.String(), "Sat 2026-10-03 13:06:40.000000001 UTC")) {
			t.Fatal(implicit.String())
		}
	}
}
