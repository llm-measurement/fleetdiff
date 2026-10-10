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
	"time"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestSpendCLIPrivacyAndErrors(t *testing.T) {
	t.Setenv("CLI_SPEND_KEY", "synthetic-cli-spend-test-key-32-bytes")
	values := []string{"PLANTED-virtual-key-917", "PLANTED-owner@example.invalid", "PLANTED-team-917", "PLANTED-end-user-917", "PLANTED-session-917", "PLANTED-model-917", "func PLANTED_CODE() {}", "/private/PLANTED_REPO/source.go"}
	rows := []map[string]any{}
	for i, day := range []string{"2026-09-01", "2026-09-02"} {
		rows = append(rows, map[string]any{"request_id": string(rune('a' + i)), "call_type": "completion", "api_key": values[0], "user": values[1], "team_id": values[2], "end_user": values[3], "session_id": values[4], "model": values[5], "messages": values[6], "response": values[7], "prompt_tokens": 100 + i*200, "completion_tokens": 20, "total_tokens": 120 + i*200, "spend": "0.03", "status": "success", "startTime": day + "T12:00:00Z", "endTime": day + "T12:00:01Z"})
	}
	data, _ := json.Marshal(rows)
	path := filepath.Join(t.TempDir(), "PLANTED-PATH.json")
	os.WriteFile(path, data, 0600)
	args := []string{"investigate", "--litellm-spend", path, "--before-period", "2026-09-01", "--after-period", "2026-09-02", "--hash-secret-env", "CLI_SPEND_KEY"}
	for _, format := range []string{"text", "json"} {
		for _, group := range []string{"key", "team", "user", "end-user", "session"} {
			for _, show := range []bool{false, true} {
				a := append(append([]string{}, args...), "--format", format, "--group-by", group)
				if show {
					a = append(a, "--show-hashes")
				}
				var out, errout bytes.Buffer
				if code := Run(a, &out, &errout); code != 0 {
					t.Fatalf("code=%d %s", code, errout.String())
				}
				for _, sentinel := range append(values, path, os.Getenv("CLI_SPEND_KEY")) {
					if strings.Contains(out.String()+errout.String(), sentinel) {
						t.Fatal("sentinel leaked")
					}
				}
				if format == "text" {
					for _, want := range []string{"Tue 2026-09-01", "Wed 2026-09-02", "Who drove the increase?", "100%", "No other usage problems found."} {
						if !strings.Contains(out.String(), want) {
							t.Fatal(want, out.String())
						}
					}
				}
			}
		}
	}
	// Input errors must never leak a raw number, path or malformed record.
	rows[1]["startTime"] = values[7]
	bad, _ := json.Marshal(rows)
	os.WriteFile(path, bad, 0600)
	var out, errout bytes.Buffer
	if code := Run(args, &out, &errout); code == 0 || out.Len() != 0 {
		t.Fatal("invalid input emitted partial report")
	}
	for _, sentinel := range append(values, path) {
		if strings.Contains(errout.String(), sentinel) {
			t.Fatal("error leaked sentinel")
		}
	}
}

func TestSpendFractionalPeriodDisplay(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 100_000_000, time.UTC)
	middle, end := start.Add(100*time.Millisecond), start.Add(200*time.Millisecond)
	r := spend.Report{
		Before: spend.Window{Period: spend.Period{Start: start, End: middle}},
		After:  spend.Window{Period: spend.Period{Start: middle, End: end}},
	}
	var out bytes.Buffer
	renderSpend(&out, r)
	for _, want := range []string{
		"Before: Tue 2026-09-01 00:00:00.1 UTC to Tue 2026-09-01 00:00:00.2 UTC",
		"After: Tue 2026-09-01 00:00:00.2 UTC to Tue 2026-09-01 00:00:00.3 UTC",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("period lost fractional seconds", out.String())
		}
	}
}

func TestSpendFlagIsolationAndHelp(t *testing.T) {
	for _, args := range [][]string{
		{"compare", "--litellm-spend", "sentinel.csv"},
		{"investigate", "--litellm-spend", "sentinel.csv", "--before", "sentinel"},
		{"investigate", "--group-by", "key"},
		{"investigate", "--litellm-spend", "sentinel.csv", "--allow-partial"},
		{"investigate", "--litellm-spend", "sentinel.csv", "--format", "sentinel"},
		{"investigate", "--litellm-spend", "sentinel.csv", "--top", "101"},
	} {
		var out, errout bytes.Buffer
		if code := Run(args, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "sentinel") {
			t.Fatal(args, code, out.String(), errout.String())
		}
	}
	var out, errout bytes.Buffer
	if code := Run([]string{"investigate", "--help"}, &out, &errout); code != 0 || !strings.Contains(out.String(), "--litellm-spend") {
		t.Fatal(out.String(), errout.String())
	}
}
