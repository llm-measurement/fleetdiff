// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSpendCoverageOverflowCLI(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported-%t", supported), func(t *testing.T) {
			kind := "completion"
			if !supported {
				kind = "SENTINEL-private-call-type"
			}
			rows := []map[string]any{}
			for i := range 3 {
				day := "2026-09-02"
				call := kind
				if i == 0 {
					day, call = "2026-09-01", "completion"
				}
				row := map[string]any{
					"request_id": fmt.Sprint(i), "call_type": call, "model": "SENTINEL-model", "api_key": "SENTINEL-key",
					"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120, "status": "success", "spend": "0",
					"startTime": day + "T12:00:00Z", "endTime": day + "T12:00:01Z",
				}
				if i == 1 {
					row["total_tokens"] = uint64(math.MaxInt64)
					if !supported {
						row["prompt_tokens"], row["completion_tokens"] = uint64(math.MaxInt64), uint64(math.MaxInt64)
					}
				}
				rows = append(rows, row)
			}
			data, err := json.Marshal(rows)
			if err != nil {
				t.Fatal("could not encode synthetic rows")
			}
			path := filepath.Join(t.TempDir(), "coverage.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal("could not create synthetic export")
			}
			args := []string{"investigate", "--litellm-spend", path, "--before-period", "2026-09-01", "--after-period", "2026-09-02"}
			for _, format := range []string{"text", "json"} {
				var out, errout bytes.Buffer
				if code := Run(append(args, "--format", format), &out, &errout); code != 0 || errout.Len() != 0 {
					t.Fatal("coverage overflow failed the CLI")
				}
				if strings.Contains(out.String(), "SENTINEL") || strings.Contains(out.String(), path) {
					t.Fatal("coverage overflow exposed source values")
				}
				if format == "text" {
					if supported {
						if !strings.Contains(out.String(), "completion raw total_tokens column sum: 120 -> exceeds report range.") {
							t.Fatal("supported raw overflow was not identified")
						}
					} else {
						for _, want := range []string{
							"Comparison incomplete:", "Prompt tokens (unanalyzed): 0 -> exceeds report range",
							"completion tokens (unanalyzed): 0 -> exceeds report range", "total tokens (unanalyzed): 0 -> exceeds report range",
						} {
							if !strings.Contains(out.String(), want) {
								t.Fatal("unsupported overflow was not marked as unanalyzed")
							}
						}
					}
					continue
				}
				var report struct {
					Incomplete bool `json:"comparison_incomplete"`
					After      struct {
						Tokens uint64 `json:"recorded_tokens"`
					} `json:"after"`
					CallTypes []struct {
						After map[string]json.RawMessage `json:"after"`
					} `json:"call_types"`
				}
				if err := json.Unmarshal(out.Bytes(), &report); err != nil {
					t.Fatal("invalid CLI JSON")
				}
				index, tokens := 1, uint64(0)
				if supported {
					index, tokens = 0, 240
				}
				if report.Incomplete == supported || report.After.Tokens != tokens || len(report.CallTypes) != index+1 {
					t.Fatal("raw overflow changed analyzed totals or coverage scope")
				}
				counts := report.CallTypes[index].After
				if counts["total_tokens"] != nil || string(counts["total_tokens_overflow"]) != "true" {
					t.Fatal("CLI exposed an unavailable raw total")
				}
			}
		})
	}
}
