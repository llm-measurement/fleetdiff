// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestCoverageOverflowIsStickyAndColumnLocal(t *testing.T) {
	columns := []string{"prompt_tokens", "completion_tokens", "total_tokens"}
	for _, column := range columns {
		for side := range 2 {
			t.Run(fmt.Sprintf("%s/side-%d", column, side), func(t *testing.T) {
				var report Report
				index := map[string]int{}
				values := []string{fmt.Sprint(uint64(math.MaxInt64) - 1), "1", "1", "42", "invalid", ""}
				for i, value := range values {
					row := Row{Values: map[string]string{
						"call_type": "embedding", "prompt_tokens": "0", "completion_tokens": "0", "total_tokens": "0",
					}}
					row.Values[column] = value
					if err := recordCallType(&report, index, row, side); err != nil {
						t.Fatal("raw coverage overflow aborted collection")
					}
					counts := report.CallTypes[0].Before
					if side == 1 {
						counts = report.CallTypes[0].After
					}
					data, err := json.Marshal(counts)
					if err != nil {
						t.Fatal("could not encode coverage")
					}
					var fields map[string]json.RawMessage
					if err := json.Unmarshal(data, &fields); err != nil {
						t.Fatal("invalid coverage JSON")
					}
					if i < 2 {
						want := fmt.Sprint(uint64(math.MaxInt64) - 1 + uint64(i))
						if string(fields[column]) != want || fields[column+"_overflow"] != nil {
							t.Fatal("in-range coverage was not preserved exactly")
						}
					} else if fields[column] != nil || string(fields[column+"_overflow"]) != "true" {
						t.Fatal("overflowed sum was exposed or its marker was lost")
					}
					for _, other := range columns {
						if other != column && (string(fields[other]) != "0" || fields[other+"_overflow"] != nil) {
							t.Fatal("overflow hid another column's genuine zero")
						}
					}
					if counts.Requests != uint64(i+1) {
						t.Fatal("overflow lost request coverage")
					}
					if i == len(values)-1 && (counts.InvalidUsage != 1 || counts.MissingUsage != 1) {
						t.Fatal("overflow stopped later scalar-quality checks")
					}
				}
				untouched := report.CallTypes[0].After
				if side == 1 {
					untouched = report.CallTypes[0].Before
				}
				if untouched != (CallTypeCounts{}) || report.CallTypes[0].Analyzed {
					t.Fatal("raw coverage crossed periods or became analyzed")
				}
			})
		}
	}
}

func TestCoverageOverflowDoesNotAbortAnalyzedRows(t *testing.T) {
	for _, componentOverflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("component-overflow-%t", componentOverflow), func(t *testing.T) {
			rows := []map[string]string{
				sample("before", "2026-09-01", "a", 100, 20),
				sample("after-1", "2026-09-02", "a", 100, 20),
				sample("after-2", "2026-09-02", "a", 100, 20),
			}
			rows[1]["total_tokens"] = fmt.Sprint(uint64(math.MaxInt64))
			if componentOverflow {
				for _, row := range rows[1:] {
					row["prompt_tokens"], row["completion_tokens"], row["total_tokens"] = fmt.Sprint(uint64(math.MaxInt64)), "1", "0"
				}
			}
			r, err := Investigate(writeRows(t, rows), options(t))
			if err != nil {
				t.Fatal("raw coverage overflow aborted usable analysis")
			}
			c := r.CallTypes[0].After
			if r.Before.Tokens != 120 || r.After.Requests != 2 || r.Incomplete || r.Volume != nil {
				t.Fatal("raw overflow changed analyzed scope or quality gating")
			}
			if componentOverflow {
				if r.After.Tokens != 0 || r.After.Quality.Invalid != 2 || !c.PromptTokensOverflow || c.PromptTokens != 0 || c.CompletionTokens != 2 || c.TotalTokensOverflow {
					t.Fatal("invalid component rows did not remain quality evidence")
				}
			} else if r.After.Tokens != 240 || r.After.Quality.TotalMismatch != 1 || !c.TotalTokensOverflow || c.TotalTokens != 0 || c.PromptTokens != 200 || c.CompletionTokens != 40 {
				t.Fatal("cross-check overflow changed usable component totals")
			}
		})
	}
}

func TestCoverageMarkersDoNotPermitAnalyzedTotalOverflow(t *testing.T) {
	rows := []map[string]string{
		sample("before", "2026-09-01", "a", 100, 20),
		sample("after-1", "2026-09-02", "a", math.MaxInt64, 0),
		sample("after-2", "2026-09-02", "a", 1, 0),
	}
	r, err := Investigate(writeRows(t, rows), options(t))
	if err == nil || !strings.Contains(err.Error(), "exceeds its supported range") || r.Schema != "" {
		t.Fatal("analyzed total overflow must fail without a report")
	}
}
