// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestReleaseBinarySessionBudget(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test a packaged executable")
	}
	fixture := "../../examples/litellm-spend/session-budget.csv"
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, period := range []string{"", "2026-10-03"} {
		args := []string{"investigate", "--litellm-spend", fixture, "--session-budget-usd", "1.00"}
		if period != "" {
			args = append(args, "--period", period)
		}
		for _, format := range []string{"text", "json"} {
			output, err := exec.Command(binary, append(args, "--format", format)...).Output()
			if err != nil {
				t.Fatalf("packaged session review (%s, %s): %v", period, format, err)
			}
			for column, field := range rows[0] {
				if field != "request_id" && field != "session_id" && field != "api_key" && field != "team_id" && field != "agent_id" {
					continue
				}
				for _, row := range rows[1:] {
					if row[column] != "" && strings.Contains(string(output), row[column]) {
						t.Fatal("packaged session report exposed a raw identity")
					}
				}
			}
			if format == "text" {
				if !strings.HasPrefix(string(output), "3 of 4 sessions with complete recorded spend exceed the supplied USD 1.00 cap.\n") ||
					!strings.Contains(string(output), "Recorded USD: p50 1.10; p95 1.60; max 1.60 (n=4, exact nearest-rank).") ||
					(strings.Contains(string(output), "file's observed span") != (period == "")) ||
					(period != "" && strings.Contains(string(output), "clipped")) {
					t.Fatal("packaged session headline, distribution or period changed")
				}
				continue
			}
			var report spend.SessionReport
			if err := json.Unmarshal(output, &report); err != nil {
				t.Fatal(err)
			}
			if report.Schema != "fleetdiff-litellm-session-budget/v1" || report.Grouping != "session_id_only" ||
				report.Sessions != 4 || report.ModelCalls != 12 || report.Spend.N != 4 || report.Exceeding != 3 ||
				report.NoRecordedRejection == nil || *report.NoRecordedRejection != 2 ||
				report.SharedCounters != 1 || report.LongSpans != 1 || report.Clipped != 0 ||
				report.ObservedSpan != (period == "") || (period != "" && report.PossiblyClipped != 0) {
				t.Fatalf("packaged session accounting changed: %+v", report)
			}
		}
	}
}
