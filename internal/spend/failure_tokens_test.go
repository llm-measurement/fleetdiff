// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFailureTokensAreRecordedSubset(t *testing.T) {
	for _, callType := range []string{"completion", "acompletion", "text_completion", "atext_completion", "anthropic_messages", "aanthropic_messages", "responses", "aresponses"} {
		t.Run(callType, func(t *testing.T) {
			rows := []map[string]string{sample("before", "2026-09-01", "key", 19, 3), sample("chat", "2026-09-02", "key", 19, 3), sample("responses", "2026-09-02", "key", 14, 0), sample("zero", "2026-09-02", "key", 0, 0)}
			for _, row := range rows {
				row["call_type"] = callType
			}
			for _, row := range rows[1:] {
				row["status"] = "failure"
			}
			r, err := Investigate(writeRows(t, rows), options(t))
			if err != nil {
				t.Fatal(err)
			}
			want := FailureTokens{Records: 2, Input: 33, Output: 3, Total: 36, Finality: "unknown"}
			if r.Before.FailureTokens != nil || r.After.FailureTokens == nil || *r.After.FailureTokens != want {
				t.Fatalf("subset: %+v", r.After.FailureTokens)
			}
			if r.After.Tokens != 36 || r.After.AttributedTokens != 36 || r.After.Requests != 3 || r.After.Quality.Failed != 3 || r.After.RecordedSpend != "0.037037036703" || r.Volume != nil {
				t.Fatalf("existing totals changed: %+v", r.After)
			}
			if len(r.Models) != 1 || *r.Models[0].After.FailureTokens != want {
				t.Fatal("model subset missing")
			}
		})
	}
}

func TestNoFailureTokensOmitsAddition(t *testing.T) {
	rows := []map[string]string{sample("before", "2026-09-01", "key", 19, 3), sample("after", "2026-09-02", "key", 14, 0), sample("zero", "2026-09-02", "key", 0, 0)}
	rows[2]["status"] = "failure"
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"failure_tokens"`) {
		t.Fatal("unaffected JSON changed")
	}
}

func TestFailureTokensDoNotTurnInvalidOrMissingIntoUsage(t *testing.T) {
	rows := []map[string]string{sample("before", "2026-09-01", "key", 1, 1), sample("partial", "2026-09-02", "key", 19, 3), sample("invalid", "2026-09-02", "key", 0, 0)}
	rows[1]["status"], rows[2]["status"] = "failure", "failure"
	rows[1]["completion_tokens"] = ""
	rows[2]["prompt_tokens"], rows[2]["completion_tokens"] = "-1", "9223372036854775808"
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.After.Tokens != 19 || r.After.FailureTokens == nil || r.After.FailureTokens.Total != 19 || r.After.Quality.Missing != 2 || r.After.Quality.Invalid != 1 {
		t.Fatalf("partial accounting: %+v", r.After)
	}
}
