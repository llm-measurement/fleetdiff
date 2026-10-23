// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bytes"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func mappedCapturedVariant(t *testing.T, details map[string]any) (spend.Row, [32]byte, [32]byte, error) {
	t.Helper()
	var row spend.Row
	var id, fingerprint [32]byte
	var mappedErr error
	err := readJSON(bytes.NewReader(capturedUsageVariant(t, details)), false, func(p projection) error {
		row, id, fingerprint, mappedErr = mappedRow(p, "user_id", "")
		return mappedErr
	})
	if mappedErr != nil {
		return row, id, fingerprint, mappedErr
	}
	return row, id, fingerprint, err
}

func TestMappedCapturedBaseAndOptionalUsage(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		details              map[string]any
		input, output, total string
		columns              map[string]string
	}{
		{"base", map[string]any{"input": 60, "output": 35, "total": 95}, "60", "35", "95", nil},
		{"null", map[string]any{"input": 60, "output": 35, "total": 95, "input_cached_tokens": nil, "input_cache_creation": nil, "output_reasoning_tokens": nil}, "60", "35", "95", nil},
		{"read", map[string]any{"input": 60, "output": 35, "total": 125, "input_cached_tokens": 30}, "90", "35", "125", map[string]string{"cache_read_input_tokens": "30"}},
		{"write", map[string]any{"input": 60, "output": 35, "total": 105, "input_cache_creation": 10}, "70", "35", "105", map[string]string{"cache_write_input_tokens": "10"}},
		{"reasoning", map[string]any{"input": 60, "output": 35, "total": 100, "output_reasoning_tokens": 5}, "60", "40", "100", map[string]string{"reasoning_output_tokens": "5"}},
		{"partial_set", map[string]any{"input": 60, "output": 35, "total": 130, "input_cached_tokens": 30, "output_reasoning_tokens": 5}, "90", "40", "130", map[string]string{"cache_read_input_tokens": "30", "reasoning_output_tokens": "5"}},
		{"all", map[string]any{"input": 60, "output": 35, "total": 140, "input_cached_tokens": 30, "input_cache_creation": 10, "output_reasoning_tokens": 5}, "100", "40", "140", map[string]string{"cache_read_input_tokens": "30", "cache_write_input_tokens": "10", "reasoning_output_tokens": "5"}},
		{"zero_detail", map[string]any{"input": 60, "output": 35, "total": 95, "input_cached_tokens": 0}, "60", "35", "95", map[string]string{"cache_read_input_tokens": "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, _, _, err := mappedCapturedVariant(t, tc.details)
			if err != nil {
				t.Fatal(err)
			}
			if row.Values["prompt_tokens"] != tc.input || row.Values["completion_tokens"] != tc.output || row.Values["total_tokens"] != tc.total {
				t.Fatal("mapped base/detail totals are incorrect")
			}
			for _, column := range []string{"cache_read_input_tokens", "cache_write_input_tokens", "reasoning_output_tokens"} {
				got, present := row.Values[column]
				want, reported := tc.columns[column]
				if present != reported || got != want {
					t.Fatal("optional detail invented or lost", column)
				}
			}
			r, err := spend.InvestigateRows(func(visit func(spend.Row) error) error { return visit(row) }, options())
			if err != nil {
				t.Fatal(err)
			}
			total, err := strconv.ParseUint(tc.total, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			if r.Before.Tokens != total || r.Before.Requests != 1 || r.Before.Quality.Missing != 0 || r.Before.Quality.Invalid != 0 || r.Before.Quality.TotalMismatch != 0 {
				t.Fatal("existing spend composition lost valid base usage")
			}
			for column, reported := range map[string]uint64{"cache_read_input_tokens": r.Before.Cache.Read.Reported, "cache_write_input_tokens": r.Before.Cache.Write.Reported} {
				_, present := tc.columns[column]
				if (reported == 1) != present {
					t.Fatal("absent cache detail reported as measured zero", column)
				}
			}
		})
	}
}

func TestMappedUsageMissingInvalidAndOverflow(t *testing.T) {
	for _, tc := range []struct {
		name          string
		details       map[string]any
		input, output string
	}{
		{"missing_input", map[string]any{"output": 35, "input_cached_tokens": 30}, "", "35"},
		{"missing_output", map[string]any{"input": 60, "output_reasoning_tokens": 5}, "60", ""},
		{"null_base", map[string]any{"input": nil, "output": 35, "input_cached_tokens": 30}, "", "35"},
		{"invalid_input", map[string]any{"input": -1, "output": 35}, "invalid", "35"},
		{"invalid_optional", map[string]any{"input": 60, "output": 35, "input_cached_tokens": -1}, "invalid", "35"},
		{"invalid_reasoning", map[string]any{"input": 60, "output": 35, "output_reasoning_tokens": -1}, "60", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, _, _, err := mappedCapturedVariant(t, tc.details)
			if err != nil {
				t.Fatal(err)
			}
			if row.Values["prompt_tokens"] != tc.input || row.Values["completion_tokens"] != tc.output {
				t.Fatal("missing or invalid base/detail became zero or known usage")
			}
		})
	}
	for _, details := range []map[string]any{
		{"input": uint64(math.MaxInt64), "output": 0, "input_cached_tokens": 1},
		{"input": 0, "output": uint64(math.MaxInt64), "output_reasoning_tokens": 1},
	} {
		if _, _, _, err := mappedCapturedVariant(t, details); !errors.Is(err, ErrAccountingLimit) {
			t.Fatal("component addition overflow not fatal", err)
		}
	}
	data := capturedUsageVariant(t, map[string]any{"input": 60, "output": 35})
	if err := readJSON(bytes.NewReader(data), false, func(p projection) error {
		p.badUsage = true
		row, _, _, err := mappedRow(p, "user_id", "")
		if err != nil {
			return err
		}
		if row.Values["prompt_tokens"] != "invalid" || row.Values["completion_tokens"] != "invalid" {
			t.Fatal("bad usage marker was lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMappedUsageDedupDistinguishesAbsentFromZero(t *testing.T) {
	_, id, absent, err := mappedCapturedVariant(t, map[string]any{"input": 60, "output": 35, "total": 95})
	if err != nil {
		t.Fatal(err)
	}
	_, nullID, null, err := mappedCapturedVariant(t, map[string]any{"input": 60, "output": 35, "total": 95, "input_cached_tokens": nil})
	if err != nil {
		t.Fatal(err)
	}
	_, zeroID, zero, err := mappedCapturedVariant(t, map[string]any{"input": 60, "output": 35, "total": 95, "input_cached_tokens": 0})
	if err != nil {
		t.Fatal(err)
	}
	if id != nullID || id != zeroID || absent != null || absent == zero {
		t.Fatal("normalized dedup lost absence versus measured zero")
	}
}
