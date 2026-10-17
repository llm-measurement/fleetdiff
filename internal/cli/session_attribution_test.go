// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sessionAttributionCLI(t *testing.T, rows []map[string]any, format string, hashes bool) string {
	t.Helper()
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "private-session-export.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"investigate", "--litellm-spend", path, "--period", "2026-09-02", "--session-budget-usd", "0.5", "--format", format}
	if hashes {
		args = append(args, "--show-hashes")
	}
	var out, errout bytes.Buffer
	if code := Run(args, &out, &errout); code != 0 || errout.Len() != 0 {
		t.Fatalf("exit=%d stderr=%s", code, &errout)
	}
	for _, sentinel := range []string{"private-", "sentinel-", path} {
		if strings.Contains(out.String()+errout.String(), sentinel) {
			t.Fatalf("private value leaked: %s %s", &out, &errout)
		}
	}
	return out.String()
}

func sessionAttributionCLIRows(agents []any) []map[string]any {
	rows := make([]map[string]any, len(agents))
	for i, agent := range agents {
		rows[i] = map[string]any{
			"request_id": fmt.Sprintf("private-request-%d", i), "session_id": "private-session", "agent_id": agent,
			"api_key": "private-key", "team_id": "private-team", "call_type": "aresponses",
			"startTime": "2026-09-02T12:00:00Z", "endTime": "2026-09-02T12:00:01Z",
			"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12,
			"spend": "0.25", "status": "success", "rejection_kind": nil,
		}
	}
	return rows
}

func TestSessionAttributionCLIUnknownAndLowerBounds(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		agents                  []any
		known, distinct, shared int
	}{
		{"all-known", []any{"private-a", "private-a", "private-a", "private-a"}, 4, 1, 0},
		{"all-known-sharing", []any{"private-a", "private-b", "private-a", "private-b"}, 4, 2, 1},
		{"null", []any{nil, nil, nil, nil}, 0, 0, 0},
		{"empty", []any{"", "", "", ""}, 0, 0, 0},
		{"missing", []any{nil, nil, nil, nil}, 0, 0, 0},
		{"partial-one", []any{"private-a", "private-a", nil, ""}, 2, 1, 0},
		{"partial-sharing", []any{"private-a", "private-b", nil, nil}, 2, 2, 1},
		{"keys", []any{nil, nil, nil, nil}, 0, 0, 1},
		{"teams", []any{nil, nil, nil, nil}, 0, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := sessionAttributionCLIRows(tc.agents)
			if tc.name == "missing" {
				for _, row := range rows {
					delete(row, "agent_id")
				}
			}
			if tc.name == "keys" {
				rows[1]["api_key"] = "private-second-key"
			}
			if tc.name == "teams" {
				rows[1]["team_id"] = "private-second-team"
			}
			for _, hashes := range []bool{false, true} {
				text := sessionAttributionCLI(t, rows, "text", hashes)
				if !strings.HasPrefix(text, "1 of 1 sessions with complete recorded spend exceed the supplied USD 0.50 cap.\n") {
					t.Fatal(text)
				}
				if strings.Contains(text, "Session IDs observed under multiple keys, teams or agents: 1.") != (tc.shared == 1) {
					t.Fatalf("sharing evidence changed: %s", text)
				}
				if tc.known == 4 {
					for _, addition := range []string{"Agent attribution", "agent attribution", "\n    agents:", "lower bound", "Missing agent IDs"} {
						if strings.Contains(text, addition) {
							t.Fatalf("fully attributed text gained %q: %s", addition, text)
						}
					}
				} else {
					label := "unknown"
					if tc.distinct != 0 {
						label = fmt.Sprintf("%d (observed lower bound)", tc.distinct)
					}
					for _, want := range []string{
						"agents: " + label,
						fmt.Sprintf("Agent attribution: %d known, %d missing of 4 selected rows.", tc.known, 4-tc.known),
						fmt.Sprintf("aresponses: agent attribution %d known, %d missing rows.", tc.known, 4-tc.known),
						"Missing agent IDs cannot rule out sharing; known distinct identities still establish observed sharing.",
					} {
						if !strings.Contains(text, want) {
							t.Fatalf("missing %q: %s", want, text)
						}
					}
					if tc.shared == 1 && !strings.Contains(text, "agents="+label+".") {
						t.Fatalf("shared counter list overstated coverage: %s", text)
					}
				}
				var report map[string]any
				if err := json.Unmarshal([]byte(sessionAttributionCLI(t, rows, "json", hashes)), &report); err != nil {
					t.Fatal(err)
				}
				if _, ok := report["agent_attribution"]; ok != (tc.known != 4) {
					t.Fatal("report coverage presence does not match missing attribution")
				}
				for _, name := range []string{"largest_sessions", "cap_review", "shared_counter_review"} {
					for _, value := range report[name].([]any) {
						session := value.(map[string]any)
						if session["agents"] != float64(tc.distinct) {
							t.Fatalf("numeric agents changed: %+v", session)
						}
						if _, ok := session["agent_attribution"]; ok != (tc.known != 4) {
							t.Fatalf("session coverage presence: %+v", session)
						}
					}
				}
			}
		})
	}
}

func TestSessionAttributionCLICallTypePrivacyAndMissingOnlyLines(t *testing.T) {
	rows := sessionAttributionCLIRows([]any{"private-agent", nil, nil, nil})
	rows[0]["call_type"] = "completion"
	rows[1]["call_type"] = "responses"
	rows[3]["call_type"] = "private-call-type-sentinel"
	for _, hashes := range []bool{false, true} {
		text := sessionAttributionCLI(t, rows, "text", hashes)
		for _, name := range []string{"aresponses", "responses", "unknown"} {
			if !strings.Contains(text, name+": agent attribution 0 known, 1 missing rows.") {
				t.Fatalf("missing call-type coverage: %s", text)
			}
		}
		if strings.Contains(text, "completion: agent attribution") {
			t.Fatalf("fully attributed call type gained a text line: %s", text)
		}
		var report map[string]any
		if err := json.Unmarshal([]byte(sessionAttributionCLI(t, rows, "json", hashes)), &report); err != nil {
			t.Fatal(err)
		}
		coverage := report["agent_attribution_by_call_type"].(map[string]any)
		if len(coverage) != 4 || coverage["completion"].(map[string]any)["known_rows"] != float64(1) {
			t.Fatalf("complete call-type context missing from JSON: %+v", coverage)
		}
	}
}

func TestSessionAttributionCLICounterWindowReview(t *testing.T) {
	rows := sessionAttributionCLIRows([]any{nil, nil})
	rows[1]["startTime"], rows[1]["endTime"] = "2026-09-02T14:00:00Z", "2026-09-02T14:00:01Z"
	text := sessionAttributionCLI(t, rows, "text", false)
	if !strings.Contains(text, "  session-1: 7200 seconds.\n    agents: unknown; agent attribution: 0 known, 2 missing rows.\n") {
		t.Fatalf("counter-window listing hid missing attribution: %s", text)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(sessionAttributionCLI(t, rows, "json", false)), &report); err != nil {
		t.Fatal(err)
	}
	session := report["counter_window_review"].([]any)[0].(map[string]any)
	if session["agents"] != float64(0) || session["agent_attribution"].(map[string]any)["missing_rows"] != float64(2) {
		t.Fatalf("counter-window JSON attribution: %+v", session)
	}
}
