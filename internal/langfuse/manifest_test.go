// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestCountCheckedConversion(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		max, want   int64
		err         error
	}{
		{"negative_limit", "0", -1, 0, ErrInvalid},
		{"max_int64", "9223372036854775807", math.MaxInt64, math.MaxInt64, nil},
		{"above_int64", "9223372036854775808", math.MaxInt64, 0, ErrFileLimit},
		{"above_uint64", "18446744073709551616", math.MaxInt64, 0, ErrFileLimit},
		{"negative_one", "-1", math.MaxInt64, 0, ErrInvalid},
		{"negative_zero", "-0", math.MaxInt64, 0, ErrInvalid},
		{"above_limit", "1", 0, 0, ErrFileLimit},
		{"zero", "0", 0, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := count(json.Number(tc.value), tc.max)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("count(%q, %d) = %d, %v; want %d, %v", tc.value, tc.max, got, err, tc.want, tc.err)
			}
		})
	}
}

func syntheticManifest(t *testing.T, kind string, compressed bool) map[string]any {
	t.Helper()
	data := synthetic(t)
	f := descriptor(kind, compressed, data, 3)
	return map[string]any{"version": 1, "projectId": "synthetic-project", "exportSource": "EVENTS", "window": map[string]any{"minTimestamp": "2026-10-10T12:00:00.000Z", "maxTimestamp": "2026-10-17T12:00:00.000Z"}, "maxTimestamp": "2026-10-17T12:00:00.000Z", "createdAt": "2026-10-21T17:41:37.874Z", "tables": []string{"scores", "observations_v2"}, "files": []any{map[string]any{"key": f.Key, "table": f.Table, "fileType": f.FileType, "format": f.Format, "compressed": f.Compressed, "contentType": f.ContentType, "sizeBytes": f.SizeBytes, "rowCount": f.RowCount}}}
}

func TestSyntheticManifestContract(t *testing.T) {
	for _, kind := range []string{"JSONL", "JSON", "CSV"} {
		for _, zip := range []bool{false, true} {
			m := syntheticManifest(t, kind, zip)
			b, _ := json.Marshal(m)
			got, err := ParseManifest(bytes.NewReader(b))
			if err != nil || got.ProjectID != "synthetic-project" || len(got.Files) != 1 || got.Start.Format("2006-01-02") != "2026-10-10" || got.End.Format("2006-01-02") != "2026-10-17" {
				t.Fatal("completion manifest rejected")
			}
			if got.Files[0].Key != "prefix/synthetic-project/observations_v2/data."+strings.ToLower(kind)+map[bool]string{false: "", true: ".gz"}[zip] {
				t.Fatal("bucket-root key was rewritten")
			}
		}
	}
}

func TestManifestRejectsInvalidIndexes(t *testing.T) {
	cases := []struct {
		name   string
		change func(map[string]any)
	}{
		{"version", func(m map[string]any) { m["version"] = 2 }},
		{"source", func(m map[string]any) { m["exportSource"] = "OBSERVATIONS_V2" }},
		{"missing-files", func(m map[string]any) { delete(m, "files") }},
		{"bad-window", func(m map[string]any) { m["maxTimestamp"] = "2026-10-18T00:00:00Z" }},
		{"reverse-window", func(m map[string]any) { m["window"].(map[string]any)["minTimestamp"] = "2026-10-18T00:00:00Z" }},
		{"duplicate-file", func(m map[string]any) { f := m["files"].([]any); m["files"] = append(f, f[0]) }},
		{"negative-size", func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["sizeBytes"] = -1 }},
		{"fractional-count", func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["rowCount"] = 1.5 }},
		{"large-count", func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["rowCount"] = MaxRows + 1 }},
		{"missing-size", func(m map[string]any) { delete(m["files"].([]any)[0].(map[string]any), "sizeBytes") }},
		{"wrong-table", func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["table"] = "traces" }},
		{"compression-contract", func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["compressed"] = true }},
		{"parquet", func(m map[string]any) { m["files"].([]any)[0].(map[string]any)["fileType"] = "PARQUET" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := syntheticManifest(t, "JSONL", false)
			c.change(m)
			b, _ := json.Marshal(m)
			if _, err := ParseManifest(bytes.NewReader(b)); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	for _, key := range []string{"/observations_v2/a.jsonl", "../observations_v2/a.jsonl", "x/../observations_v2/a.jsonl", "s3://bucket/observations_v2/a.jsonl", `x\observations_v2\a.jsonl`, "x//observations_v2/a.jsonl", "x/observations_v2/a.jsonl\x00"} {
		m := syntheticManifest(t, "JSONL", false)
		m["files"].([]any)[0].(map[string]any)["key"] = key
		b, _ := json.Marshal(m)
		if _, err := ParseManifest(bytes.NewReader(b)); err == nil {
			t.Fatal("noncanonical object key accepted")
		}
	}
	m := syntheticManifest(t, "JSONL", false)
	b, _ := json.Marshal(m)
	b = bytes.Replace(b, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
	if _, err := ParseManifest(bytes.NewReader(b)); err == nil {
		t.Fatal("duplicate manifest key accepted")
	}
	if _, err := ParseManifest(strings.NewReader(strings.Repeat(" ", MaxManifestBytes+1))); err == nil {
		t.Fatal("oversized manifest accepted")
	}
}

func TestScorePayloadNotParsed(t *testing.T) {
	b := []byte("PRIVATE_SCORE_SENTINEL")
	f := descriptor("JSONL", false, b, 1)
	f.Table = "scores"
	if _, err := ReadFile(bytes.NewReader(b), f); err != ErrUnsupported {
		t.Fatal("score payload accepted")
	}
}

func TestManifestLimitClassification(t *testing.T) {
	if MaxManifestBytes != 1<<20 || MaxFileBytes != 16<<20 || MaxRows != 100000 {
		t.Fatal("approved limits changed")
	}
	for _, tc := range []struct {
		name, field string
		value       any
		limit       bool
	}{
		{"stored", "sizeBytes", MaxFileBytes + 1, true},
		{"rows", "rowCount", MaxRows + 1, true},
		{"huge_stored", "sizeBytes", json.Number("18446744073709551616"), true},
		{"huge_rows", "rowCount", json.Number("18446744073709551616"), true},
		{"negative_stored", "sizeBytes", -1, false},
		{"negative_rows", "rowCount", -1, false},
		{"fractional", "rowCount", 1.5, false},
		{"nonnumeric", "sizeBytes", "PRIVATE_LIMIT_SENTINEL", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := syntheticManifest(t, "JSONL", false)
			m["files"].([]any)[0].(map[string]any)[tc.field] = tc.value
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseManifest(bytes.NewReader(data))
			if tc.limit {
				if !errors.Is(err, ErrFileLimit) {
					t.Fatal("limit was not actionable", err)
				}
			} else if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrFileLimit) {
				t.Fatal("malformed count changed semantics", err)
			}
			if err != nil && strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("unsafe limit diagnostic")
			}
		})
	}
	m := syntheticManifest(t, "JSONL", false)
	f := m["files"].([]any)[0].(map[string]any)
	f["sizeBytes"], f["rowCount"] = MaxFileBytes, MaxRows
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, bytes.Repeat([]byte(" "), MaxManifestBytes-len(data))...)
	if _, err := ParseManifest(bytes.NewReader(data)); err != nil {
		t.Fatal("inclusive bounds rejected", err)
	}
	if _, err := ParseManifest(bytes.NewReader(append(data, ' '))); !errors.Is(err, ErrFileLimit) {
		t.Fatal("manifest byte limit was generic", err)
	}
}
