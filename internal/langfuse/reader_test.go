// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func synthetic(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/SYNTHETIC.jsonl")
	if err != nil {
		t.Fatal("synthetic fixture unavailable")
	}
	return b
}

func descriptor(kind string, compressed bool, data []byte, rows int64) File {
	ext := strings.ToLower(kind)
	format := ext + "-raw"
	media := map[string]string{"JSON": "application/json; charset=utf-8", "JSONL": "application/x-ndjson; charset=utf-8", "CSV": "text/csv; charset=utf-8"}[kind]
	if compressed {
		format = ext + "-gzip"
		ext += ".gz"
		media = "application/gzip"
	}
	return File{Key: "prefix/synthetic-project/observations_v2/data." + ext, Table: "observations_v2", FileType: kind, Format: format, Compressed: compressed, ContentType: media, SizeBytes: int64(len(data)), RowCount: rows}
}

func encodeSynthetic(t *testing.T, kind string) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(synthetic(t)), []byte("\n"))
	if kind == "JSONL" {
		return synthetic(t)
	}
	if kind == "JSON" {
		return append(append([]byte("["), bytes.Join(lines, []byte(","))...), ']')
	}
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	header := []string{"id", "project_id", "type", "start_time", "usage_details", "total_cost", "input", "output", "metadata", "tool_calls", "status_message"}
	if w.Write(header) != nil {
		t.Fatal("CSV header")
	}
	for _, line := range lines {
		var row map[string]json.RawMessage
		if json.Unmarshal(line, &row) != nil {
			t.Fatal("synthetic encoding")
		}
		values := make([]string, len(header))
		for i, key := range header {
			raw := row[key]
			if len(raw) == 0 {
				continue
			}
			var text string
			if json.Unmarshal(raw, &text) == nil {
				values[i] = text
			} else {
				values[i] = string(raw)
			}
		}
		if w.Write(values) != nil {
			t.Fatal("CSV record")
		}
	}
	w.Flush()
	if w.Error() != nil {
		t.Fatal("CSV flush")
	}
	return out.Bytes()
}

func compressed(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	w.Name = "PRIVATE_GZIP_NAME_SENTINEL"
	w.Comment = "PRIVATE_GZIP_COMMENT_SENTINEL"
	if _, err := w.Write(data); err != nil {
		t.Fatal("gzip encode")
	}
	if w.Close() != nil {
		t.Fatal("gzip close")
	}
	return out.Bytes()
}

func TestSyntheticSixFormatsDisjointAccounting(t *testing.T) {
	var baseline FileReport
	for _, kind := range []string{"JSONL", "JSON", "CSV"} {
		for _, zip := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-plain", true: "-gzip"}[zip], func(t *testing.T) {
				data := encodeSynthetic(t, kind)
				if zip {
					data = compressed(t, data)
				}
				got, err := ReadFile(bytes.NewReader(data), descriptor(kind, zip, data, 3))
				if err != nil {
					t.Fatal("synthetic format rejected", err)
				}
				if got.PhysicalRows != 3 || got.GenerationRows != 2 || got.OtherRows != 1 || got.OtherTypes["EMBEDDING"] != 1 {
					t.Fatal("observation classification")
				}
				if got.InclusiveInput != 100 || got.InclusiveOutput != 40 || got.Components["input"] != 60 || got.Components["output"] != 35 {
					t.Fatal("disjoint components were not reconstructed")
				}
				if got.RecordedCost != "0.0003" || got.UnknownUsageCategories != 1 || got.MissingUsageRows != 1 || got.PartialUsageRows != 1 {
					t.Fatal("quality or recorded cost")
				}
				if got.TotalMismatchRows != 0 || got.InvalidUsageRows != 0 {
					t.Fatal("valid totals flagged")
				}
				encoded, _ := json.Marshal(got)
				if bytes.Contains(encoded, []byte("PRIVATE_")) || bytes.Contains(encoded, []byte("synthetic-generation")) {
					t.Fatal("private data in report")
				}
				if baseline.OtherTypes == nil {
					baseline = got
				} else if !reflect.DeepEqual(baseline, got) {
					t.Fatal("format projections differ")
				}
			})
		}
	}
}

func oneRow(usage, cost string) []byte {
	return []byte(`{"id":"synthetic","project_id":"project","type":"GENERATION","start_time":"2026-10-10 12:00:00.000000","usage_details":` + usage + `,"total_cost":` + cost + `}` + "\n")
}

func TestUsageQualityAndExactCost(t *testing.T) {
	cases := []struct {
		name, usage, cost                                                            string
		missing, partial, invalid, unknown, mismatch, zero, missingCost, invalidCost uint64
	}{
		{"missing", "null", "null", 1, 1, 0, 0, 0, 0, 1, 0},
		{"empty", "{}", "0", 1, 1, 0, 0, 0, 0, 0, 0},
		{"zero", `{"input":0,"output":0,"input_cached_tokens":0,"input_cache_creation":0,"output_reasoning_tokens":0,"total":0}`, "0", 0, 0, 0, 0, 0, 1, 0, 0},
		{"unknown", `{"private_category":{"input":"PRIVATE_NESTED_SENTINEL"}}`, "0", 1, 1, 0, 1, 0, 0, 0, 0},
		{"invalid", `{"input":-1,"output":1.2,"input_cached_tokens":true,"input_cache_creation":"PRIVATE_NUMBER_SENTINEL","output_reasoning_tokens":null}`, "-1", 0, 1, 1, 0, 0, 0, 0, 1},
		{"overflow", `{"input":9223372036854775808}`, "0.00000000000000000001", 0, 1, 1, 0, 0, 0, 0, 0},
		{"mismatch", `{"input":1,"output":2,"input_cached_tokens":3,"input_cache_creation":4,"output_reasoning_tokens":5,"total":99}`, "0", 0, 0, 0, 0, 1, 1, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := oneRow(c.usage, c.cost)
			got, err := ReadFile(bytes.NewReader(data), descriptor("JSONL", false, data, 1))
			if err != nil {
				t.Fatal("quality row rejected", err)
			}
			if got.MissingUsageRows != c.missing || got.PartialUsageRows != c.partial || got.InvalidUsageRows != c.invalid || got.UnknownUsageCategories != c.unknown || got.TotalMismatchRows != c.mismatch || got.UsageWithZeroCostRows != c.zero || got.MissingCostRows != c.missingCost || got.InvalidCostRows != c.invalidCost {
				t.Fatal("quality classification differs")
			}
			encoded, _ := json.Marshal(got)
			if bytes.Contains(encoded, []byte("PRIVATE_")) {
				t.Fatal("discarded accounting value leaked")
			}
		})
	}
}

type chunkReader struct {
	r io.Reader
	n int
}

func (r chunkReader) Read(p []byte) (int, error) {
	if len(p) > r.n {
		p = p[:r.n]
	}
	return r.r.Read(p)
}

func TestSelectivePrivacyAndChunkBoundaries(t *testing.T) {
	for _, kind := range []string{"JSONL", "JSON", "CSV"} {
		for _, n := range []int{1, 2, 7, 31} {
			data := encodeSynthetic(t, kind)
			if _, err := ReadFile(chunkReader{bytes.NewReader(data), n}, descriptor(kind, false, data, 3)); err != nil {
				t.Fatal("chunked synthetic input rejected", kind, n)
			}
		}
	}
	base := strings.TrimSpace(string(oneRow("{}", "0")))
	data := []byte(base[:len(base)-1] + `,"in\u0070ut":{"nested":["` + strings.Repeat("PRIVATE_", 2000) + `"]},"output":null,"metadata":false,"tool_calls":123,"status_message":"discard"}`)
	var captured projection
	if err := readJSON(bytes.NewReader(data), false, func(p projection) error { captured = p; return nil }); err != nil {
		t.Fatal("large discarded value rejected")
	}
	for _, key := range []string{"input", "output", "metadata", "tool_calls", "status_message"} {
		if _, ok := captured.fields[key]; ok {
			t.Fatal("forbidden field retained")
		}
	}
	encoded, _ := json.Marshal(captured.fields)
	if bytes.Contains(encoded, []byte("PRIVATE_")) {
		t.Fatal("discarded bytes retained in projection")
	}
}

func TestMalformedOrLateInputReturnsNoPartialReport(t *testing.T) {
	valid := oneRow("{}", "0")
	bad := []string{
		`{"id":"x","id":"y"}`,
		`{"metadata":"PRIVATE_\q"}`,
		`{"metadata":"\ud800"}`,
		`{"metadata":01}`,
		`{"metadata":[1,]}`,
		`{"metadata":truex}`,
		`{"metadata":{"x":1 "y":2}}`,
		`{"metadata":"unterminated}`,
		`{"input":0,"in\u0070ut":1}`,
		`{"usage_details":{"input":1,"in\u0070ut":2}}`,
	}
	for _, tail := range bad {
		data := append(append([]byte{}, valid...), []byte(tail)...)
		got, err := ReadFile(bytes.NewReader(data), descriptor("JSONL", false, data, 2))
		if err == nil || !reflect.DeepEqual(got, FileReport{}) {
			t.Fatal("bad late input left partial totals")
		}
		if strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("input value in error")
		}
	}
	for _, tail := range []string{",]", `]junk`, "][]"} {
		data := append([]byte("["+strings.TrimSpace(string(valid))), []byte(tail)...)
		if _, err := ReadFile(bytes.NewReader(data), descriptor("JSON", false, data, 1)); err == nil {
			t.Fatal("JSON framing accepted")
		}
	}
}

func TestCSVSkippedMultilineAndMalformed(t *testing.T) {
	data := []byte("id,project_id,type,start_time,metadata,usage_details,total_cost\r\nx,p,GENERATION,2026-10-10 12:00:00,\"PRIVATE_,\"\"quote\"\"\r\nsecond line\",{},0\r\n")
	if _, err := ReadFile(bytes.NewReader(data), descriptor("CSV", false, data, 1)); err != nil {
		t.Fatal("quoted multiline discarded field rejected")
	}
	for _, bad := range []string{"id,id\na,b\n", "id,metadata\nx,\"unterminated", "id,metadata\nx,un\"quoted\n", "id,metadata\nx,\"done\"junk\n", "id\nx,extra\n", "id,metadata\nx\n"} {
		b := []byte(bad)
		if _, err := ReadFile(bytes.NewReader(b), descriptor("CSV", false, b, 1)); err == nil {
			t.Fatal("malformed CSV accepted")
		}
	}
}

func TestSizeCountAndGzipFailClosed(t *testing.T) {
	data := synthetic(t)
	file := descriptor("JSONL", false, data, 3)
	for _, change := range []func(*File){func(f *File) { f.SizeBytes++ }, func(f *File) { f.SizeBytes-- }, func(f *File) { f.RowCount++ }, func(f *File) { f.RowCount-- }} {
		f := file
		change(&f)
		got, err := ReadFile(bytes.NewReader(data), f)
		if !errors.Is(err, ErrIncomplete) || !reflect.DeepEqual(got, FileReport{}) {
			t.Fatal("manifest mismatch accepted")
		}
	}
	zipped := compressed(t, data)
	badCRC := append([]byte{}, zipped...)
	badCRC[len(badCRC)-8] ^= 0xff
	for _, b := range [][]byte{zipped[:len(zipped)-1], badCRC, append(append([]byte{}, zipped...), zipped...), append(append([]byte{}, zipped...), []byte("PRIVATE_TRAILING")...)} {
		got, err := ReadFile(bytes.NewReader(b), descriptor("JSONL", true, b, 3))
		if err == nil || !reflect.DeepEqual(got, FileReport{}) {
			t.Fatal("bad gzip yielded totals")
		}
		if strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("gzip private header or data in error")
		}
	}
}

func TestBoundsAndUnknownTypes(t *testing.T) {
	for _, b := range [][]byte{[]byte(`{"metadata":"` + strings.Repeat("a", MaxRowBytes) + `"}`), []byte(`{"metadata":` + strings.Repeat("[", MaxDepth+2) + "0" + strings.Repeat("]", MaxDepth+2) + "}"), []byte(`{"metadata":[` + strings.Repeat("0,", MaxFields) + "0]}"), []byte("{\"metadata\":\"\xff\"}")} {
		if _, err := ReadFile(bytes.NewReader(b), descriptor("JSONL", false, b, 1)); err == nil {
			t.Fatal("over-limit or invalid Unicode accepted")
		}
	}
	r := byteBudget{r: strings.NewReader("abc"), count: MaxFileBytes - 1}
	var b [3]byte
	if _, err := r.Read(b[:]); err == nil {
		t.Fatal("byte budget not enforced")
	}
	if _, err := r.Read(b[:]); err == nil {
		t.Fatal("exhausted byte budget not enforced")
	}
	data := bytes.Replace(oneRow("{}", "0"), []byte("GENERATION"), []byte("PRIVATE_TYPE_SENTINEL"), 1)
	got, err := ReadFile(bytes.NewReader(data), descriptor("JSONL", false, data, 1))
	if err != nil || got.OtherTypes["__unknown__"] != 1 {
		t.Fatal("unknown type not bounded")
	}
	encoded, _ := json.Marshal(got)
	if bytes.Contains(encoded, []byte("PRIVATE_")) {
		t.Fatal("unknown type leaked")
	}
	// The approved value decoder is not applied to arbitrary nested categories.
	l := lexer{r: bufio.NewReader(strings.NewReader(`{"private":{"status_message":"PRIVATE_CATEGORY_SENTINEL"},"input":60}`))}
	p := projection{}
	if l.usage(&p) != nil || p.unknown != 1 || len(p.usage) != 1 {
		t.Fatal("usage selection")
	}
}
