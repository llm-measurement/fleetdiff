// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// Derive these variants only from the checked-in selective safe projection.
// They are test transformations, not claims of additional native captures.
func capturedUsageVariant(t *testing.T, details map[string]any) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/fresh-r3/2026-10-17T12-00-00.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	if hex.EncodeToString(h[:]) != "1a99ca9d2588724f3762b7a07e639d5cc78316c98977c6d54d13c5462452cded" {
		t.Fatal("reviewed safe projection changed")
	}
	var row map[string]json.RawMessage
	if err = json.Unmarshal(bytes.Split(data, []byte("\n"))[1], &row); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"input", "output", "metadata", "tool_calls", "status_message"} {
		if _, found := row[forbidden]; found {
			t.Fatal("fixture is not a selective safe projection")
		}
	}
	usage, err := json.Marshal(details)
	if err != nil {
		t.Fatal(err)
	}
	row["usage_details"] = usage
	out, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func TestCapturedBaseAndPartialUsageComponents(t *testing.T) {
	cases := []struct {
		name          string
		detail        map[string]any
		input, output uint64
		reported      []string
	}{
		{"base_only", map[string]any{"input": 60, "output": 35, "total": 95}, 60, 35, nil},
		{"no_declared_total", map[string]any{"input": 60, "output": 35}, 60, 35, nil},
		{"cache_read", map[string]any{"input": 60, "output": 35, "input_cached_tokens": 30, "total": 125}, 90, 35, []string{"cache_read"}},
		{"cache_write", map[string]any{"input": 60, "output": 35, "input_cache_creation": 10, "total": 105}, 70, 35, []string{"cache_write"}},
		{"reasoning", map[string]any{"input": 60, "output": 35, "output_reasoning_tokens": 5, "total": 100}, 60, 40, []string{"reasoning"}},
		{"read_reasoning", map[string]any{"input": 60, "output": 35, "input_cached_tokens": 30, "output_reasoning_tokens": 5, "total": 130}, 90, 40, []string{"cache_read", "reasoning"}},
		{"read_write", map[string]any{"input": 60, "output": 35, "input_cached_tokens": 30, "input_cache_creation": 10, "total": 135}, 100, 35, []string{"cache_read", "cache_write"}},
		{"null_detail", map[string]any{"input": 60, "output": 35, "input_cached_tokens": nil, "output_reasoning_tokens": nil, "total": 95}, 60, 35, nil},
		{"zero_detail", map[string]any{"input": 60, "output": 35, "input_cached_tokens": 0, "total": 95}, 60, 35, []string{"cache_read"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := capturedUsageVariant(t, tc.detail)
			for _, kind := range []string{"JSONL", "JSON", "CSV"} {
				for _, zip := range []bool{false, true} {
					encoded := safeEncoding(t, data, kind)
					if zip {
						encoded = compressed(t, encoded)
					}
					r, err := ReadFile(bytes.NewReader(encoded), descriptor(kind, zip, encoded, 1))
					if err != nil || r.InclusiveInput != tc.input || r.InclusiveOutput != tc.output || r.InclusiveInputRows != 1 || r.InclusiveOutputRows != 1 {
						t.Fatal("base/component reconstruction", kind, zip, r, err)
					}
					if r.PartialUsageRows != 0 || r.MissingUsageRows != 0 || r.InvalidUsageRows != 0 || r.TotalMismatchRows != 0 {
						t.Fatal("unreported detail became missing total", r)
					}
					for _, name := range []string{"cache_read", "cache_write", "reasoning"} {
						want := uint64(0)
						for _, reported := range tc.reported {
							if name == reported {
								want = 1
							}
						}
						if r.ReportedFields[name] != want {
							t.Fatal("absent detail became reported zero", name, r)
						}
					}
				}
			}
		})
	}
}

func TestOptionalUsageDoesNotInventMissingBase(t *testing.T) {
	for _, usage := range []string{
		`{"input_cached_tokens":30,"output":35,"total":65}`,
		`{"input":60,"output_reasoning_tokens":5,"total":65}`,
		`{"input":60,"output":35,"input_cached_tokens":-1,"total":95}`,
		`{"input":60,"output":35,"output_reasoning_tokens":true,"total":95}`,
	} {
		data := oneRow(usage, "0.0003")
		r, err := ReadFile(bytes.NewReader(data), descriptor("JSONL", false, data, 1))
		if err != nil || r.PartialUsageRows != 1 {
			t.Fatal("missing/invalid base or component became complete", r, err)
		}
	}
}
