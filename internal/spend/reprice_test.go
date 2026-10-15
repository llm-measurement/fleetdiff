// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

const repriceDay = "2020-01-02"

func repriceRow(id string) map[string]string {
	r := sample(id, repriceDay, "private-key-sentinel", 100, 20)
	r["team_id"] = "private-team-sentinel"
	r["cache_read_input_tokens"], r["cache_write_input_tokens"] = "30", "10"
	return r
}

func TestReadRepriceSupportedUsage(t *testing.T) {
	for _, callType := range []string{"completion", "acompletion", "anthropic_messages", "aanthropic_messages", "responses", "aresponses", "text_completion", "atext_completion"} {
		t.Run(callType, func(t *testing.T) {
			row := repriceRow("private-request-sentinel")
			row["call_type"] = callType
			path := writeRows(t, []map[string]string{row})
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			visits := 0
			source, err := ReadReprice(path, repriceDay, func(r reprice.Record) error {
				visits++
				if r.Count != 1 || r.Issue != "" || r.Model != row["model"] || r.Key != row["api_key"] || r.Team != row["team_id"] {
					t.Fatal("record identity, count, or quality changed")
				}
				for i, p := range []*uint64{r.Input, r.Output, r.CacheRead, r.CacheWrite} {
					if p == nil || *p != []uint64{100, 20, 30, 10}[i] {
						t.Fatal("inclusive components changed or were double-counted")
					}
				}
				return nil
			})
			if err != nil || visits != 1 || source.Rows != 1 || source.Outside != 0 || source.Kind != "litellm-spend" || len(source.Gaps) != 0 {
				t.Fatalf("source: %+v, visits: %d, error: %v", source, visits, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("source was modified")
			}
			encoded, _ := json.Marshal(source)
			for _, value := range []string{row["model"], row["api_key"], row["team_id"], row["request_id"], path} {
				if strings.Contains(string(encoded), value) {
					t.Fatal("source metadata leaked an input identity")
				}
			}
		})
	}
}

func TestReadRepriceUnknownAndInvalidComponents(t *testing.T) {
	tests := []struct {
		name   string
		change map[string]string
		issue  string
		values [4]*uint64
	}{
		{"missing-cache", map[string]string{"cache_read_input_tokens": "", "cache_write_input_tokens": ""}, "", [4]*uint64{new(uint64(100)), new(uint64(20)), nil, nil}},
		{"explicit-zero-cache", map[string]string{"cache_read_input_tokens": "0", "cache_write_input_tokens": "0"}, "", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(0)), new(uint64(0))}},
		{"missing-input", map[string]string{"prompt_tokens": "", "total_tokens": "20"}, "missing_usage", [4]*uint64{nil, new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"missing-output", map[string]string{"completion_tokens": "", "total_tokens": "100"}, "missing_usage", [4]*uint64{new(uint64(100)), nil, new(uint64(30)), new(uint64(10))}},
		{"invalid-input", map[string]string{"prompt_tokens": "private-value-sentinel"}, "invalid_usage", [4]*uint64{nil, new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"invalid-cache", map[string]string{"cache_read_input_tokens": "-1"}, "invalid_usage", [4]*uint64{new(uint64(100)), new(uint64(20)), nil, new(uint64(10))}},
		{"read-subset", map[string]string{"cache_read_input_tokens": "101"}, "invalid_usage", [4]*uint64{new(uint64(100)), new(uint64(20)), nil, new(uint64(10))}},
		{"write-subset", map[string]string{"cache_write_input_tokens": "101"}, "invalid_usage", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(30)), nil}},
		{"combined-subsets", map[string]string{"cache_read_input_tokens": "70", "cache_write_input_tokens": "40"}, "invalid_usage", [4]*uint64{new(uint64(100)), new(uint64(20)), nil, nil}},
		{"reasoning-subset", map[string]string{"reasoning_output_tokens": "21"}, "invalid_usage", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"zero-origin", map[string]string{"prompt_tokens": "0", "completion_tokens": "0", "total_tokens": "0", "cache_read_input_tokens": "0", "cache_write_input_tokens": "0"}, "zero_only_origin_unknown", [4]*uint64{new(uint64(0)), new(uint64(0)), new(uint64(0)), new(uint64(0))}},
		{"failure", map[string]string{"status": "failure"}, "", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"unknown-status", map[string]string{"status": "private-status-sentinel"}, "unknown_status", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"missing-end", map[string]string{"endTime": ""}, "missing_end_time", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"missing-total", map[string]string{"total_tokens": ""}, "missing_total_field", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"total-mismatch", map[string]string{"total_tokens": "121"}, "component_total_mismatch", [4]*uint64{new(uint64(100)), new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"overflow", map[string]string{"prompt_tokens": "9223372036854775808"}, "invalid_usage", [4]*uint64{nil, new(uint64(20)), new(uint64(30)), new(uint64(10))}},
		{"total-overflow", map[string]string{"prompt_tokens": "9223372036854775807"}, "invalid_usage", [4]*uint64{nil, nil, new(uint64(30)), new(uint64(10))}},
	}
	for _, tt := range tests {
		for _, callType := range []string{"completion", "anthropic_messages", "responses"} {
			t.Run(callType+"/"+tt.name, func(t *testing.T) {
				row := repriceRow("request")
				row["call_type"] = callType
				for k, v := range tt.change {
					row[k] = v
				}
				visits := 0
				_, err := ReadReprice(writeRows(t, []map[string]string{row}), repriceDay, func(r reprice.Record) error {
					visits++
					if r.Issue != tt.issue || r.Count != 1 || !reflect.DeepEqual([4]*uint64{r.Input, r.Output, r.CacheRead, r.CacheWrite}, tt.values) {
						t.Fatalf("unexpected components or issue: %+v", r)
					}
					return nil
				})
				if err != nil || visits != 1 {
					t.Fatalf("visits %d: %v", visits, err)
				}
			})
		}
	}
}

func TestReadRepricePeriodAndUnsupported(t *testing.T) {
	rows := []map[string]string{repriceRow("before"), repriceRow("start"), repriceRow("end"), repriceRow("unsupported")}
	rows[0]["startTime"] = "2020-01-01T23:59:59Z"
	rows[1]["startTime"] = "2020-01-01T19:00:00-05:00"
	rows[2]["startTime"] = "2020-01-03T00:00:00Z"
	rows[3]["call_type"] = "private-call-type-sentinel"
	rows[3]["endTime"] = "private-unsupported-end-sentinel"
	for _, period := range []string{repriceDay, "2020-01-01T19:00:00-05:00/2020-01-02T19:00:00-05:00"} {
		visits, exclusions := 0, 0
		source, err := ReadReprice(writeRows(t, rows), period, func(r reprice.Record) error {
			visits++
			if r.Issue == "unsupported_call_type" {
				exclusions++
				if r.Count != 1 || r.Input != nil || r.Output != nil || r.CacheRead != nil || r.CacheWrite != nil || r.Model != rows[3]["model"] {
					t.Fatal("unsupported usage was invented or its identity lost")
				}
			}
			return nil
		})
		if err != nil || visits != 2 || exclusions != 1 || source.Rows != 2 || source.Outside != 2 || source.Start.Format("2006-01-02") != repriceDay || len(source.Gaps) != 1 {
			t.Fatalf("selection: %+v, visits %d: %v", source, visits, err)
		}
		if strings.Contains(strings.Join(source.Gaps, " "), "sentinel") {
			t.Fatal("unsupported call type leaked")
		}
	}
}

func repriceCalculator(t *testing.T) *reprice.Calculator {
	t.Helper()
	p := reprice.Price{ID: "secret-model-alias", Provider: "test", Input: "1", Output: "2", CacheRead: "0.1", CacheWrite: "1.25"}
	target := p
	target.ID = "target"
	c, err := reprice.New(
		reprice.Prices{Version: 1, Date: repriceDay, Models: []reprice.Price{p, target}},
		reprice.Factors{Version: 1, Date: repriceDay},
		reprice.Scenario{Version: 1, Moves: []reprice.Move{{From: p.ID, To: target.ID, Fraction: "0.5"}}},
		reprice.WorkloadMap{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestReadRepriceFailedUsageCoverage(t *testing.T) {
	for _, callType := range []string{"completion", "anthropic_messages", "responses"} {
		for _, tc := range []struct {
			name   string
			change map[string]string
			priced bool
		}{
			{"valid", nil, true},
			{"missing-cache", map[string]string{"cache_write_input_tokens": ""}, false},
			{"missing-end", map[string]string{"endTime": ""}, false},
			{"invalid-subset", map[string]string{"cache_read_input_tokens": "101"}, false},
			{"zero-origin", map[string]string{"prompt_tokens": "0", "completion_tokens": "0", "total_tokens": "0", "cache_read_input_tokens": "0", "cache_write_input_tokens": "0"}, false},
			{"unknown-status", map[string]string{"status": "private-status-sentinel"}, false},
		} {
			t.Run(callType+"/"+tc.name, func(t *testing.T) {
				row := repriceRow("failed-request")
				row["call_type"], row["status"] = callType, "failure"
				for k, v := range tc.change {
					row[k] = v
				}
				c := repriceCalculator(t)
				source, err := ReadReprice(writeRows(t, []map[string]string{row}), repriceDay, c.Add)
				if err != nil {
					t.Fatal(err)
				}
				r := c.Result(source)
				if tc.priced {
					if r.Covered != 1 || r.Before == nil || *r.Before != "0.000115500" || len(r.Excluded) != 0 || len(source.Gaps) != 1 || source.Gaps[0] != "Complete recorded usage on failed requests: 1; billing finality is unknown." {
						t.Fatalf("valid failed usage was not priced: %+v", r)
					}
				} else if r.Covered != 0 || r.Before != nil || len(r.Excluded) != 1 || len(source.Gaps) != 0 {
					t.Fatalf("invalid or incomplete failed usage was priced: %+v", r)
				}
			})
		}
	}
}

func TestReadRepriceFailedUsageCount(t *testing.T) {
	rows := []map[string]string{repriceRow("one"), repriceRow("two"), repriceRow("missing-cache"), repriceRow("success")}
	for _, row := range rows[:3] {
		row["status"] = "failure"
	}
	rows[2]["cache_read_input_tokens"] = ""
	c := repriceCalculator(t)
	source, err := ReadReprice(writeRows(t, rows), repriceDay, c.Add)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Gaps) != 1 || source.Gaps[0] != "Complete recorded usage on failed requests: 2; billing finality is unknown." || c.Result(source).Covered != 3 {
		t.Fatalf("failure coverage count changed: %+v", source)
	}
}

func TestReadRepriceUnsupportedExclusionsReconcile(t *testing.T) {
	rows := []map[string]string{repriceRow("supported"), repriceRow("unsupported-known"), repriceRow("unsupported-unknown")}
	for _, row := range rows[1:] {
		row["call_type"] = "private-call-type-sentinel"
		row["prompt_tokens"], row["endTime"] = "private-value-sentinel", "private-time-sentinel"
	}
	rows[2]["model"] = "private-model-sentinel"
	c := repriceCalculator(t)
	source, err := ReadReprice(writeRows(t, rows), repriceDay, c.Add)
	if err != nil {
		t.Fatal(err)
	}
	r := c.Result(source)
	var excluded uint64
	for _, n := range r.Excluded {
		excluded += n
	}
	if source.Rows != 3 || r.Covered != 1 || excluded != 2 || r.Covered+excluded != source.Rows || len(source.Gaps) != 1 {
		t.Fatalf("selected observations disappeared: %+v", r)
	}
	encoded, _ := json.Marshal(r)
	if strings.Contains(string(encoded), "sentinel") {
		t.Fatal("unsupported row leaked input content")
	}
	sentinel := errors.New("bounded aggregation limit")
	if _, err := ReadReprice(writeRows(t, rows[1:]), repriceDay, func(reprice.Record) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal("unsupported-row callback error was not preserved")
	}
}

func TestReadRepriceValidation(t *testing.T) {
	for _, change := range []map[string]string{
		{"request_id": ""}, {"startTime": "private-time-sentinel"},
		{"endTime": "private-time-sentinel"}, {"endTime": "2020-01-01T00:00:00Z"},
		{"endTime": "2199-01-01T00:00:00Z"},
	} {
		row := repriceRow("request")
		for k, v := range change {
			row[k] = v
		}
		source, err := ReadReprice(writeRows(t, []map[string]string{row}), repriceDay, func(reprice.Record) error { return nil })
		if err == nil || strings.Contains(err.Error(), "sentinel") || !reflect.DeepEqual(source, reprice.Source{}) {
			t.Fatal("expected safe error and empty source")
		}
	}
	path := writeRows(t, []map[string]string{repriceRow("request")})
	for _, period := range []string{"", "private-period-sentinel", "2199-01-01", "2020-01-03", "2020-01-02T00:00:00Z/2020-01-01T00:00:00Z"} {
		if _, err := ReadReprice(path, period, func(reprice.Record) error { return nil }); err == nil || strings.Contains(err.Error(), "sentinel") {
			t.Fatal("period accepted or leaked")
		}
	}
	if _, err := ReadReprice(path, repriceDay, nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
}

func TestReadRepriceExactDuplicates(t *testing.T) {
	for _, outside := range []bool{false, true} {
		rows := []map[string]string{repriceRow("private-duplicate-sentinel"), repriceRow("private-duplicate-sentinel")}
		if outside {
			rows[1]["startTime"] = "2020-01-03T00:00:00Z"
		}
		source, err := ReadReprice(writeRows(t, rows), repriceDay, func(reprice.Record) error { return nil })
		if err == nil || !strings.Contains(err.Error(), "duplicate request_id") || strings.Contains(err.Error(), "sentinel") || !reflect.DeepEqual(source, reprice.Source{}) {
			t.Fatal("duplicate IDs must invalidate the complete source")
		}
	}
	rows := make([]map[string]string, digestBlock+1)
	for i := range rows {
		rows[i] = repriceRow(strconv.Itoa(i))
		rows[i]["startTime"] = "2020-01-03T00:00:00Z"
	}
	rows[0] = repriceRow("cross-block-duplicate")
	rows[len(rows)-1]["request_id"] = "cross-block-duplicate"
	if _, err := ReadReprice(writeRows(t, rows), repriceDay, func(reprice.Record) error { return nil }); err == nil || !strings.Contains(err.Error(), "duplicate request_id") {
		t.Fatal("cross-block duplicate was missed")
	}
}

func TestReadRepriceReadAndVisitorErrors(t *testing.T) {
	sentinel := errors.New("bounded aggregation limit")
	visits := 0
	source, err := ReadReprice(writeRows(t, []map[string]string{repriceRow("one"), repriceRow("two")}), repriceDay, func(reprice.Record) error {
		visits++
		return sentinel
	})
	if !errors.Is(err, sentinel) || visits != 1 || !reflect.DeepEqual(source, reprice.Source{}) {
		t.Fatal("visitor error was not preserved")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "private-filename-sentinel.json")
	row, _ := json.Marshal(repriceRow("one"))
	if err := os.WriteFile(path, append(append([]byte("["), row...), []byte("] private-body-sentinel")...), 0600); err != nil {
		t.Fatal(err)
	}
	visits = 0
	source, err = ReadReprice(path, repriceDay, func(reprice.Record) error { visits++; return nil })
	if !errors.Is(err, errSpendJSON) || visits != 1 || !reflect.DeepEqual(source, reprice.Source{}) {
		t.Fatal("late reader failure did not invalidate the source")
	}
	if _, err := ReadReprice(filepath.Join(dir, "missing-private-sentinel.json"), repriceDay, func(reprice.Record) error { return nil }); !errors.Is(err, errSpendFile) || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("file error changed or leaked a path")
	}
}
