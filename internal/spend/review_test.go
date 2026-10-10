// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestExactDuplicateBlocks(t *testing.T) {
	var d requestDigests
	if d.duplicated() {
		t.Fatal("empty set")
	}
	for i := 0; i < digestBlock*2+13; i++ {
		d.add(sha256.Sum256([]byte(fmt.Sprint(i))))
	}
	if d.Len() != digestBlock*2+13 || d.duplicated() {
		t.Fatal("distinct IDs collided")
	}
	d.add(sha256.Sum256([]byte("3")))
	if !d.duplicated() {
		t.Fatal("missed duplicate across blocks")
	}
}

func TestAgentRoutesDoNotCreateFalseDrop(t *testing.T) {
	for _, call := range []string{"anthropic_messages", "aanthropic_messages", "responses", "aresponses"} {
		t.Run(call, func(t *testing.T) {
			for _, count := range []int{6, 12} {
				var rows []map[string]string
				for side, day := range []string{"2026-09-01", "2026-09-02"} {
					for i := 0; i < 12; i++ {
						r := sample(fmt.Sprintf("%d-%d", side, i), day, "a", 200, 10)
						if side == 1 && i < count {
							r["call_type"] = call
						}
						r["cache_read_input_tokens"] = "80"
						r["cache_write_input_tokens"] = "20"
						rows = append(rows, r)
					}
				}
				r, err := Investigate(writeRows(t, rows), options(t))
				if err != nil || r.Before.Tokens != 2520 || r.After.Tokens != 2520 || r.Incomplete || r.After.Quality.Invalid != 0 {
					t.Fatalf("agent normalization: %+v, %v", r, err)
				}
			}
		})
	}
}

func TestUnsupportedCoverageAndFutureRows(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "a", 100, 20)}
	rows[1]["call_type"] = "generate_content"
	rows[1]["endTime"] = "2099-01-01T00:00:00Z"
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil || !r.Incomplete || r.After.Requests != 0 || r.Volume != nil || r.Increase != nil {
		t.Fatalf("%+v, %v", r, err)
	}
	c := r.CallTypes[1]
	if c.Name != "generate_content" || c.Analyzed || c.After.Requests != 1 || c.After.PromptTokens != 100 || c.After.CompletionTokens != 20 || c.After.TotalTokens != 120 {
		t.Fatal(c)
	}
	rows[0]["call_type"] = "SENTINEL-private-call-type"
	_, err = Investigate(writeRows(t, rows), options(t))
	if err == nil || !strings.Contains(err.Error(), "generate_content") || !strings.Contains(err.Error(), "unknown-1") || strings.Contains(err.Error(), "SENTINEL") {
		t.Fatal(err)
	}
	rows[0]["call_type"] = "completion"
	rows[1]["call_type"] = "completion"
	rows[1]["endTime"] = "2026-09-02T12:00:01Z"
	outside := sample("3", "2026-08-01", "a", 100, 20)
	outside["endTime"] = "2099-01-01T00:00:00Z"
	rows = append(rows, outside)
	if _, err = Investigate(writeRows(t, rows), options(t)); err != nil {
		t.Fatal(err)
	}
	rows[1]["endTime"] = "2099-01-01T00:00:00Z"
	if _, err = Investigate(writeRows(t, rows), options(t)); err == nil {
		t.Fatal("accepted unfinished selected supported row")
	}
}

func TestCallTypeCoverageUsesExactSchemaValue(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "a", 100, 20)}
	rows[1]["call_type"] = " completion "
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil || !r.Incomplete || len(r.CallTypes) != 2 || r.CallTypes[1].Analyzed || r.CallTypes[1].After.Requests != 1 {
		t.Fatalf("%+v, %v", r, err)
	}
}

func TestConcentrationIndependentOfTop(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "c", 200, 0), sample("2", "2026-09-02", "a", 100, 0), sample("3", "2026-09-02", "b", 150, 0)}
	for _, top := range []int{1, 2, 3, 100} {
		o := options(t)
		o.Top = top
		r, err := Investigate(writeRows(t, rows), o)
		if err != nil || r.Increase == nil || r.Increase.Count != 2 || r.Increase.Share.Lower != 5 || r.Increase.Share.Upper != 5 {
			t.Fatalf("top %d: %+v, %v", top, r.Increase, err)
		}
		if len(r.Rankings.Movers) != min(top, 3) {
			t.Fatal("display limit ignored")
		}
	}
}

func TestAliasTieOrderIgnoresHashSecret(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "first", 10, 0), sample("2", "2026-09-01", "second", 20, 0), sample("3", "2026-09-02", "first", 20, 0), sample("4", "2026-09-02", "second", 30, 0)}
	rows[1]["model"] = "second-model"
	rows[3]["model"] = "second-model"
	o := options(t)
	o.SecretEnv = ""
	var prior Report
	for i := 0; i < 12; i++ {
		r, err := Investigate(writeRows(t, rows), o)
		if err != nil {
			t.Fatal(err)
		}
		if r.Models[0].Before.Tokens != 10 || r.Rankings.Movers[0].Before.Lower != 10 {
			t.Fatal("tie not ordered by first appearance")
		}
		if i > 0 && !reflect.DeepEqual(prior, r) {
			t.Fatal("ephemeral hashing changed displayed aliases")
		}
		prior = r
	}
	b, _ := json.Marshal(prior.Models)
	for _, field := range []string{"attributed_tokens", "distinct_groups_estimate", "distinct_nominal_rse"} {
		if strings.Contains(string(b), field) {
			t.Fatal("uncomputed model measurement exposed")
		}
	}
}

func TestTokenRangesAreRowQuality(t *testing.T) {
	for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "reasoning_output_tokens"} {
		for _, value := range []string{"-1", "9223372036854775808", "18446744073709551616", "1e999"} {
			rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "a", 100, 20)}
			rows[1][field] = value
			r, err := Investigate(writeRows(t, rows), options(t))
			if err != nil || r.After.Quality.Invalid != 1 || r.Volume != nil {
				t.Fatalf("%s: invalid=%d, err=%v", field, r.After.Quality.Invalid, err)
			}
		}
	}
	rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "a", math.MaxInt64, 1)}
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil || r.After.Quality.Invalid != 1 {
		t.Fatalf("row total overflow: %+v, %v", r.After, err)
	}
}
