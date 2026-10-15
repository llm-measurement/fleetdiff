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

func TestRowCoverageCLIAndPrivacy(t *testing.T) {
	t.Setenv("ROW_COVERAGE_KEY", "synthetic-row-coverage-sentinel-secret")
	dir := t.TempDir()
	daily := filepath.Join(dir, "PRIVATE_daily.csv")
	const header = "date,api_requests,successful_requests,failed_requests\n"
	if err := os.WriteFile(daily, []byte(header+"2026-10-07,1000,900,100\n2026-10-08,1000,1000,0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "PRIVATE_spend.jsonl")
	const sentinel = "PRIVATE-identity@example.invalid"
	var source bytes.Buffer
	enc := json.NewEncoder(&source)
	for i, stamp := range []string{"2026-10-07T12:00:00Z", "2026-10-08T12:00:00Z"} {
		row := map[string]any{"request_id": sentinel + string(rune('a'+i)), "call_type": "completion", "startTime": stamp, "endTime": stamp, "prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110, "spend": "0.03", "status": "success"}
		for _, name := range []string{"api_key", "user", "team_id", "end_user", "session_id", "agent_id", "model"} {
			row[name] = sentinel
		}
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, source.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"investigate", "--litellm-spend", path, "--litellm-daily-spend", daily, "--hash-secret-env", "ROW_COVERAGE_KEY"}
	for _, format := range []string{"text", "json"} {
		for _, group := range []string{"key", "team", "user", "end-user", "session", "session-review"} {
			for _, hashes := range []bool{false, true} {
				args := append([]string{}, base...)
				args = append(args, "--format", format)
				if group == "session-review" {
					args = append(args, "--period", "2026-10-07", "--session-budget-usd", "1")
				} else {
					args = append(args, "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--group-by", group)
				}
				if hashes {
					args = append(args, "--show-hashes")
				}
				var out, errout bytes.Buffer
				if code := Run(args, &out, &errout); code != 0 {
					t.Fatal(code, errout.String())
				}
				for _, secret := range []string{sentinel, daily, path, os.Getenv("ROW_COVERAGE_KEY")} {
					if strings.Contains(out.String()+errout.String(), secret) {
						t.Fatal("privacy sentinel escaped")
					}
				}
				if format == "text" {
					if !strings.HasPrefix(out.String(), "Spend-log row coverage") || !strings.Contains(out.String(), "1 exported row; 1,000 daily requests (900 successful, 100 failed); 999 missing rows.") {
						t.Fatal(out.String())
					}
				} else {
					var report struct {
						Coverage struct {
							Days []struct {
								Missing uint64 `json:"missing_rows"`
							} `json:"days"`
						} `json:"row_coverage"`
					}
					if err := json.Unmarshal(out.Bytes(), &report); err != nil || report.Coverage.Days[0].Missing != 999 {
						t.Fatal("missing JSON coverage", err)
					}
				}
			}
		}
	}
	for _, body := range []string{header + sentinel + ",1,1,0\n", header + "2026-10-07," + sentinel + ",1,0\n", "date,api_requests," + sentinel + ",failed_requests\n"} {
		if err := os.WriteFile(daily, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		var out, errout bytes.Buffer
		args := append(append([]string{}, base...), "--before-period", "2026-10-07", "--after-period", "2026-10-08")
		if code := Run(args, &out, &errout); code == 0 || out.Len() != 0 || strings.Contains(errout.String(), sentinel) || strings.Contains(errout.String(), daily) {
			t.Fatal("unsafe error or partial report")
		}
	}
}

func TestRowCoverageFlagIsolation(t *testing.T) {
	for _, args := range [][]string{
		{"investigate", "--litellm-daily-spend", "PRIVATE.csv"},
		{"compare", "--litellm-spend", "PRIVATE.csv", "--litellm-daily-spend", "PRIVATE.csv"},
		{"investigate", "--litellm-spend", "PRIVATE.csv", "--litellm-daily-spend", ""},
		{"investigate", "--before", "PRIVATE", "--after", "PRIVATE", "--litellm-daily-spend", "PRIVATE.csv"},
	} {
		var out, errout bytes.Buffer
		if code := Run(args, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "PRIVATE") {
			t.Fatal(code, errout.String())
		}
	}
}
