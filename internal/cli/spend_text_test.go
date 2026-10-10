// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestSpendTextSyntheticAnswersAndRawJSON(t *testing.T) {
	t.Setenv("SPEND_TEXT_KEY", "synthetic-spend-text-test-key-32-bytes")
	args := []string{"investigate", "--litellm-spend", "../../examples/litellm-spend/synthetic.csv", "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--hash-secret-env", "SPEND_TEXT_KEY"}
	var out, errout bytes.Buffer
	if code := Run(args, &out, &errout); code != 0 {
		t.Fatalf("code=%d: %s", code, &errout)
	}
	text := out.String()
	if !strings.HasPrefix(text, "Recorded tokens doubled (8,000 -> 16,000).\nmodel-1 accounts for 100% of the net recorded increase (+8,000 tokens).") {
		t.Fatal(text)
	}
	for _, want := range []string{
		"model-1: 4,000 -> 12,000 recorded tokens; 4 -> 6 requests.",
		"1,000.00 -> 2,000.00 recorded tokens per request; +3,000 from request count, +5,000 from request size.",
		"2 leading tracked keys account for 100% of the net recorded increase.",
		"Zero-only records, origin unknown: 2 -> 4.",
		"Failed records (overlap usage categories): 1 -> 2.",
		"No other usage problems found.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"\nNotes", "100.00%", ": 0 -> 0.", "Group identities missing:", "Missing token fields:", "Total field absent:"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("unexpected %q:\n%s", unwanted, text)
		}
	}
	if caveat := strings.Index(text, "Whole-period per-request split"); caveat < strings.Index(text, "Who drove the increase?") {
		t.Fatal("whole-period caveat precedes answers", text)
	}
	out.Reset()
	if code := Run(append(args, "--format", "json"), &out, &errout); code != 0 {
		t.Fatalf("code=%d: %s", code, &errout)
	}
	var report spend.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Before.Tokens != 8000 || report.After.Tokens != 16000 || len(report.Notes) == 0 || report.Increase == nil || report.Increase.Share.Lower != 1 {
		t.Fatal("JSON evidence changed", out.String())
	}
	if !strings.Contains(out.String(), `"missing_token_fields": 0`) || strings.Contains(out.String(), "8,000") {
		t.Fatal("JSON lost raw numeric evidence", out.String())
	}
}

func TestSpendTextHeadlineDirections(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after uint64
		want          string
	}{
		{"doubled", 8000, 16000, "Recorded tokens doubled (8,000 -> 16,000)."},
		{"rose", 8000, 10000, "Recorded tokens rose by 25% (8,000 -> 10,000)."},
		{"small rise", 100000, 100001, "Recorded tokens rose by <0.01% (100,000 -> 100,001)."},
		{"fell", 8000, 4000, "Recorded tokens fell by 50% (8,000 -> 4,000)."},
		{"unchanged", 8000, 8000, "Recorded tokens were unchanged (8,000 -> 8,000)."},
		{"zero baseline", 0, 1000, "Recorded tokens rose from zero (0 -> 1,000)."},
		{"zero after", 1000, 0, "Recorded tokens fell by 100% (1,000 -> 0)."},
		{"both zero", 0, 0, "Recorded tokens were unchanged (0 -> 0)."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := spend.Report{Before: spend.Window{Tokens: tc.before}, After: spend.Window{Tokens: tc.after}}
			var out bytes.Buffer
			renderSpend(&out, r)
			if !strings.HasPrefix(out.String(), tc.want+"\n") {
				t.Fatal(out.String())
			}
			if tc.after <= tc.before && (!strings.Contains(out.String(), "Who drove the change?") || strings.Contains(out.String(), "Who drove the increase?") || strings.Contains(out.String(), "Concentration of increase")) {
				t.Fatal("non-increase described as an increase", out.String())
			}
		})
	}
}

func TestSpendTextModelContributionUsesDirectionAndNet(t *testing.T) {
	r := spend.Report{
		Before: spend.Window{Tokens: 16000}, After: spend.Window{Tokens: 8000},
		Models: []spend.Model{
			{Item: "model-1", Before: spend.Window{Tokens: 1000}, After: spend.Window{Tokens: 3000}},
			{Item: "model-2", Before: spend.Window{Tokens: 15000}, After: spend.Window{Tokens: 5000}},
		},
	}
	var out bytes.Buffer
	renderSpend(&out, r)
	if !strings.Contains(out.String(), "model-2 accounts for 125% of the net recorded decrease (-10,000 tokens).") {
		t.Fatal("wrong model or net denominator", out.String())
	}
}

func TestSpendTextIncompleteCoverage(t *testing.T) {
	r := spend.Report{
		Incomplete: true,
		Before:     spend.Window{Tokens: 8000, Requests: 1000},
		After:      spend.Window{Tokens: 0},
		CallTypes: []spend.CallType{
			{Name: "completion", Analyzed: true, Before: spend.CallTypeCounts{Requests: 1000}},
			{Name: "embedding", After: spend.CallTypeCounts{Requests: 2000, PromptTokens: 16000, TotalTokens: 16000}},
			{Name: "unknown-1", Before: spend.CallTypeCounts{Requests: 1000, InvalidUsage: 1000}, After: spend.CallTypeCounts{Requests: 1000, MissingUsage: 1000}},
		},
		Models: []spend.Model{{Item: "model-1", Before: spend.Window{Tokens: 8000, Requests: 1000}}},
	}
	var out bytes.Buffer
	renderSpend(&out, r)
	text := out.String()
	if !strings.HasPrefix(text, "Comparison incomplete:") {
		t.Fatal(text)
	}
	for _, want := range []string{
		"embedding: 0 -> 2,000 requests (unsupported).",
		"Prompt tokens (unanalyzed): 0 -> 16,000; completion tokens (unanalyzed): 0 -> 0; total tokens (unanalyzed): 0 -> 16,000.",
		"unknown-1: 1,000 -> 1,000 requests (unsupported).",
		"Missing usage fields: 0 -> 1,000 records.",
		"Invalid usage fields: 1,000 -> 0 records.",
		"Analyzed calls only: 8,000 -> 0 recorded tokens; 1,000 -> 0 logged model requests.",
		"Who drove the change? (analyzed calls only)",
		"Next: review unsupported call types",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Recorded tokens fell", "accounts for", "Who drove the increase?", "completion: 1,000"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("unexpected %q:\n%s", unwanted, text)
		}
	}
}

func TestSpendCLIUnsupportedPeriodIsPartialAndPrivate(t *testing.T) {
	t.Setenv("SPEND_TEXT_KEY", "synthetic-spend-text-test-key-32-bytes")
	for _, beforeUnsupported := range []bool{false, true} {
		for _, callType := range []string{"embedding", "PLANTED-private-call-type-917"} {
			t.Run(callType+"/"+map[bool]string{true: "before", false: "after"}[beforeUnsupported], func(t *testing.T) {
				rows := []map[string]any{}
				for i, day := range []string{"2026-09-01", "2026-09-02"} {
					kind := "completion"
					if (i == 0) == beforeUnsupported {
						kind = callType
					}
					rows = append(rows, map[string]any{"request_id": day, "call_type": kind, "api_key": "PLANTED-private-key-917", "model": "PLANTED-private-model-917", "prompt_tokens": 8000, "completion_tokens": 1000, "total_tokens": 9000, "status": "success", "spend": "0.03", "startTime": day + "T12:00:00Z", "endTime": day + "T12:00:01Z"})
				}
				data, err := json.Marshal(rows)
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "partial.json")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				for _, format := range []string{"text", "json"} {
					var out, errout bytes.Buffer
					code := Run([]string{"investigate", "--litellm-spend", path, "--before-period", "2026-09-01", "--after-period", "2026-09-02", "--hash-secret-env", "SPEND_TEXT_KEY", "--format", format}, &out, &errout)
					if code != 0 || errout.Len() != 0 {
						t.Fatalf("code=%d: %s", code, &errout)
					}
					if strings.Contains(out.String(), "PLANTED") {
						t.Fatal("private source value leaked")
					}
					if format == "text" {
						if !strings.HasPrefix(out.String(), "Comparison incomplete:") || !strings.Contains(out.String(), "tokens (unanalyzed)") {
							t.Fatal(out.String())
						}
					} else {
						var r spend.Report
						if err := json.Unmarshal(out.Bytes(), &r); err != nil {
							t.Fatal(err)
						}
						if !r.Incomplete || len(r.CallTypes) != 2 || r.Before.Tokens+r.After.Tokens != 9000 || len(r.Notes) == 0 {
							t.Fatal("partial evidence lost", out.String())
						}
					}
				}
			})
		}
	}
}

func TestSpendTextNumbersAndBounds(t *testing.T) {
	for _, tc := range []struct{ value, want string }{
		{"0", "0"}, {"999", "999"}, {"1000", "1,000"}, {"1000000", "1,000,000"},
		{"+1000", "+1,000"}, {"-1000", "-1,000"}, {"1000.00123400", "1,000.00123400"},
		{"0.00123400", "0.00123400"}, {"-9223372036854775808", "-9,223,372,036,854,775,808"},
	} {
		if got := spendNumber(tc.value); got != tc.want {
			t.Errorf("spendNumber(%q)=%q; want %q", tc.value, got, tc.want)
		}
	}
	if got := spendInterval(compare.Interval{Lower: math.MaxInt64 - 1, Upper: math.MaxInt64}); got != "[9,223,372,036,854,775,806, 9,223,372,036,854,775,807]" {
		t.Fatal("integer uncertainty lost", got)
	}
	if got := signedInterval(compare.Interval{Lower: -2000, Upper: 1000}); got != "[-2,000, +1,000]" {
		t.Fatal(got)
	}
	r := spend.Report{
		Before: spend.Window{Tokens: 10000}, After: spend.Window{Tokens: 20000}, GroupBy: "key",
		Increase: &spend.IncreaseShare{Count: 2, Delta: compare.Interval{Lower: 12000, Upper: 15000}, Share: compare.Share{Lower: 1.2, Upper: 1.5}},
	}
	var out bytes.Buffer
	renderSpend(&out, r)
	if !strings.Contains(out.String(), "2 leading tracked keys account for [120%, 150%] of the net recorded increase.") {
		t.Fatal("net-change shares clipped", out.String())
	}
	r.Increase = &spend.IncreaseShare{Count: 1, Delta: compare.Interval{Lower: math.MaxInt64 - 1, Upper: math.MaxInt64}, Share: compare.Share{Lower: 1, Upper: 1}}
	out.Reset()
	renderSpend(&out, r)
	if !strings.Contains(out.String(), "[99.99%, 100.01%]") {
		t.Fatal("integer uncertainty lost in percentage", out.String())
	}
}

func TestSpendTextQualityOnlyShowsObservedProblems(t *testing.T) {
	r := spend.Report{
		Before: spend.Window{RecordedSpend: "1000.00123400", Quality: spend.Quality{Missing: 1000, Invalid: 1, TotalMismatch: 2, TotalMissing: 3, UnknownStatus: 4, MissingIdentity: 5, Unclear: 1010, SpendMissing: 1000}},
		After:  spend.Window{RecordedSpend: "2000.00000001", Quality: spend.Quality{Unclear: 2000, SpendInvalid: 2000}},
		Notes:  []string{"Full notes stay in JSON."},
	}
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	renderSpend(&out, r)
	for _, want := range []string{
		"Records needing usage review (categories may overlap): 1,010 -> 2,000.",
		"Missing token fields: 1,000 -> 0.",
		"Invalid usage or cache subsets: 1 -> 0.",
		"Component/total mismatch: 2 -> 0.",
		"Total field absent: 3 -> 0.",
		"Unknown status: 4 -> 0.",
		"Group identities missing: 5 -> 0.",
		"Recorded spend (USD, analyzed calls): 1,000.00123400 -> 2,000.00000001.",
		"Missing/invalid cost: 1,000 -> 2,000.",
		"No other usage problems found.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{"Zero-only records", "Failed records", "Zero-cost origin unknown", "Full notes", "\nNotes"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("unexpected %q:\n%s", unwanted, out.String())
		}
	}
	after, err := json.Marshal(r)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("text rendering mutated raw evidence")
	}
}
