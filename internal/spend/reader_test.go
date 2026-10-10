// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const readerHeader = "request_id,call_type,startTime,prompt_tokens,completion_tokens,total_tokens"
const readerObject = `{"request_id":"req","call_type":"completion","startTime":"2026-10-08T00:00:00Z","prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`
const readerNullObject = `{"request_id":null,"call_type":null,"startTime":null,"prompt_tokens":null,"completion_tokens":null,"total_tokens":null}`

func readerWrite(t *testing.T, ext, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "PRIVATE_PATH_SENTINEL"+ext)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readerCollect(t *testing.T, ext, content string) ([]Row, error) {
	t.Helper()
	var rows []Row
	err := Read(readerWrite(t, ext, content), func(row Row) error {
		rows = append(rows, row)
		return nil
	})
	return rows, err
}

func readerExtra(fields string) string {
	return strings.TrimSuffix(readerObject, "}") + "," + fields + "}"
}

func TestReaderFormatsAndSourcePreserved(t *testing.T) {
	jsonRow := readerExtra(`"spend":0.0001000,"api_key":"synthetic-key","model":"synthetic-model","status":"success","session_id":"","team_id":null,"cache_read_input_tokens":0,"metadata":{"usage_object":{"cache_write_input_tokens":99}},"messages":[{"content":"PRIVATE_CONTENT_SENTINEL"}],"unknown":false`)
	csvRow := readerHeader + ",spend,api_key,model,status,session_id,team_id,cache_read_input_tokens,metadata,messages,unknown\r\n" +
		"req,completion,2026-10-08T00:00:00Z,10,2,12,0.0001000,synthetic-key,synthetic-model,success,,,0,ignored,PRIVATE_CONTENT_SENTINEL,false\r\n"
	want := Row{Number: 1, Values: map[string]string{
		"request_id": "req", "call_type": "completion", "startTime": "2026-10-08T00:00:00Z",
		"prompt_tokens": "10", "completion_tokens": "2", "total_tokens": "12", "spend": "0.0001000",
		"api_key": "synthetic-key", "model": "synthetic-model", "status": "success", "cache_read_input_tokens": "0",
	}}
	for ext, body := range map[string]string{".csv": csvRow, ".JSON": "[\n" + jsonRow + "\n]\n", ".jsonl": "\r\n" + jsonRow + "\r\n\n"} {
		t.Run(ext, func(t *testing.T) {
			path := readerWrite(t, ext, body)
			var rows []Row
			if err := Read(path, func(row Row) error { rows = append(rows, row); return nil }); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(rows, []Row{want}) {
				t.Fatalf("rows = %#v", rows)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != body {
				t.Fatalf("source changed: %v", err)
			}
		})
	}
}

func TestReaderNumbersMissingAndAllowedColumns(t *testing.T) {
	body := strings.Replace(readerNullObject, `"prompt_tokens":null`, `"prompt_tokens":-0`, 1)
	body = strings.Replace(body, `"completion_tokens":null`, `"completion_tokens":9007199254740993`, 1)
	body = strings.TrimSuffix(body, "}") + `,"spend":1.2300e-007,"model":"","cache_read_input_tokens":0,"cache_write_input_tokens":"0","reasoning_output_tokens":0}`
	for _, ext := range []string{".json", ".jsonl"} {
		input := body
		if ext == ".json" {
			input = "[" + input + "]"
		}
		rows, err := readerCollect(t, ext, input)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"prompt_tokens": "-0", "completion_tokens": "9007199254740993", "spend": "1.2300e-007", "cache_read_input_tokens": "0", "cache_write_input_tokens": "0", "reasoning_output_tokens": "0"}
		if len(rows) != 1 || !reflect.DeepEqual(rows[0].Values, want) {
			t.Fatalf("%s: %#v", ext, rows)
		}
	}
	columns := []string{"request_id", "call_type", "api_key", "user", "team_id", "organization_id", "end_user", "model", "model_id", "model_group", "custom_llm_provider", "prompt_tokens", "completion_tokens", "total_tokens", "spend", "status", "session_id", "startTime", "endTime", "cache_read_input_tokens", "cache_write_input_tokens", "reasoning_output_tokens"}
	parts := make([]string, len(columns))
	want := make(map[string]string)
	for i, key := range columns {
		parts[i] = strconv.Quote(key) + `:"value"`
		want[key] = "value"
	}
	rows, err := readerCollect(t, ".jsonl", "{"+strings.Join(parts, ",")+"}")
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0].Values, want) {
		t.Fatalf("allowlist: %#v, %v", rows, err)
	}
}

func TestReaderCSVMultilineAndRowLifetime(t *testing.T) {
	var input bytes.Buffer
	w := csv.NewWriter(&input)
	_ = w.Write(strings.Split(readerHeader+",model,messages", ","))
	_ = w.Write([]string{"one", "completion", "now", "0", "", "0", "line1\nline2, \"quoted\"", "PRIVATE_CONTENT_SENTINEL"})
	_ = w.Write([]string{"two", "embedding", "later", "7", "0", "7", "new-model", "other-ignored-content"})
	w.Flush()
	rows, err := readerCollect(t, ".csv", input.String())
	if err != nil || len(rows) != 2 {
		t.Fatalf("%#v, %v", rows, err)
	}
	if rows[0].Number != 1 || rows[1].Number != 2 || rows[0].Values["request_id"] != "one" || rows[0].Values["model"] != "line1\nline2, \"quoted\"" {
		t.Fatalf("record overwritten or misframed: %#v", rows)
	}
	if _, present := rows[0].Values["completion_tokens"]; present {
		t.Fatal("empty CSV value was retained")
	}
	rows[1].Values["request_id"] = "changed"
	if rows[0].Values["request_id"] != "one" {
		t.Fatal("visitor rows share maps")
	}
}

func TestReaderMalformedAndPrivateErrors(t *testing.T) {
	tests := []struct {
		name, ext, body string
		want            error
	}{
		{"missing CSV header", ".csv", "req,completion,now,1,2,3\n", errSpendColumns},
		{"empty CSV", ".csv", "\n", errSpendColumns},
		{"missing CSV column", ".csv", "request_id,call_type,startTime,prompt_tokens,completion_tokens\n", errSpendColumns},
		{"duplicate CSV header", ".csv", readerHeader + ",PRIVATE_HEADER_SENTINEL,PRIVATE_HEADER_SENTINEL\n", errSpendDuplicate},
		{"duplicate ignored CSV header", ".csv", readerHeader + ",metadata,metadata\n", errSpendDuplicate},
		{"CSV UI", ".csv", "Date,Spend ($),Total Tokens\n2026-10-08,1.0000,100\n", errSpendAggregate},
		{"JSON UI", ".json", `{"metadata":{"PRIVATE_CONTENT_SENTINEL":1},"data":[]}`, errSpendAggregate},
		{"JSON API page", ".json", `{"data":[],"page":1,"total":0}`, errSpendAggregate},
		{"JSONL UI", ".jsonl", `{"metadata":{},"data":[]}`, errSpendAggregate},
		{"JSONL API page", ".jsonl", `{"data":[],"page":1}`, errSpendAggregate},
		{"array API page", ".json", `[{"data":[],"page":1}]`, errSpendAggregate},
		{"unclosed CSV quote", ".csv", readerHeader + "\n\"PRIVATE_CONTENT_SENTINEL\n", errSpendCSV},
		{"bare CSV quote", ".csv", readerHeader + "\nPRIVATE_CONTENT_SENTINEL\"x,completion,now,1,2,3\n", errSpendCSV},
		{"CSV row width", ".csv", readerHeader + "\nPRIVATE_CONTENT_SENTINEL,1\n", errSpendCSV},
		{"empty JSON", ".json", "", errSpendJSON},
		{"truncated JSON", ".json", "[" + readerObject, errSpendJSON},
		{"truncated row", ".json", "[{\"request_id\":", errSpendJSON},
		{"array trailing comma", ".json", "[" + readerObject + ",]", errSpendJSON},
		{"row trailing comma", ".json", "[" + strings.TrimSuffix(readerObject, "}") + ",}]", errSpendJSON},
		{"array trailing junk", ".json", "[" + readerObject + "]PRIVATE_CONTENT_SENTINEL", errSpendJSON},
		{"array trailing array", ".json", "[" + readerObject + "][]", errSpendJSON},
		{"empty array trailing junk", ".json", "[]x", errSpendJSON},
		{"missing array comma", ".json", "[" + readerObject + readerObject + "]", errSpendJSON},
		{"array nonobject", ".json", "[null]", errSpendJSON},
		{"missing JSON fields", ".jsonl", `{"request_id":"PRIVATE_CONTENT_SENTINEL"}`, errSpendColumns},
		{"invalid JSON number", ".jsonl", readerExtra(`"spend":01`), errSpendJSON},
		{"JSONL array", ".jsonl", "[" + readerObject + "]", errSpendJSON},
		{"JSONL multiline", ".jsonl", strings.Replace(readerObject, ",", ",\n", 1), errSpendJSON},
		{"JSONL same line objects", ".jsonl", readerObject + readerObject, errSpendJSON},
		{"JSONL trailing junk", ".jsonl", readerObject + " PRIVATE_CONTENT_SENTINEL", errSpendJSON},
		{"duplicate JSON key", ".jsonl", readerExtra(`"request_id":"PRIVATE_CONTENT_SENTINEL"`), errSpendDuplicate},
		{"duplicate escaped JSON key", ".jsonl", readerExtra(`"\u0072equest_id":"PRIVATE_CONTENT_SENTINEL"`), errSpendDuplicate},
		{"duplicate ignored JSON key", ".jsonl", readerExtra(`"messages":null,"messages":[]`), errSpendDuplicate},
		{"duplicate nested JSON key", ".jsonl", readerExtra(`"metadata":{"nested":[{"PRIVATE_HEADER_SENTINEL":1,"PRIVATE_HEADER_SENTINEL":2}]}`), errSpendDuplicate},
		{"allowed boolean", ".jsonl", readerExtra(`"model":true`), errSpendScalar},
		{"allowed array", ".jsonl", readerExtra(`"model":[]`), errSpendScalar},
		{"allowed object", ".jsonl", readerExtra(`"model":{}`), errSpendScalar},
		{"CSV invalid UTF8 header", ".csv", readerHeader + ",\xff\n", errSpendUTF8},
		{"CSV invalid UTF8 identity", ".csv", readerHeader + "\n\xff,completion,now,1,2,3\n", errSpendUTF8},
		{"CSV invalid UTF8 ignored", ".csv", readerHeader + ",messages\nreq,completion,now,1,2,3,\xff\n", errSpendUTF8},
		{"JSON invalid UTF8 identity", ".jsonl", readerExtra("\"model\":\"\xff\""), errSpendUTF8},
		{"JSON invalid UTF8 ignored", ".jsonl", readerExtra("\"metadata\":\"\xff\""), errSpendUTF8},
		{"JSON lone high surrogate", ".jsonl", readerExtra(`"model":"\ud800"`), errSpendUTF8},
		{"JSON lone low surrogate ignored", ".jsonl", readerExtra(`"metadata":"\udc00"`), errSpendUTF8},
		{"JSON bad escape", ".jsonl", readerExtra(`"model":"\x00"`), errSpendJSON},
		{"JSON bad unicode escape", ".jsonl", readerExtra(`"model":"\uZZZZ"`), errSpendJSON},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readerCollect(t, tc.ext, tc.body)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			for _, sentinel := range []string{"PRIVATE_CONTENT_SENTINEL", "PRIVATE_HEADER_SENTINEL", "PRIVATE_PATH_SENTINEL"} {
				if strings.Contains(err.Error(), sentinel) {
					t.Fatalf("error leaks input: %v", err)
				}
			}
		})
	}
}

func TestReaderFieldLimitsBeforeDecode(t *testing.T) {
	for _, length := range []int{MaxFieldBytes, MaxFieldBytes + 1} {
		want := error(nil)
		if length > MaxFieldBytes {
			want = errSpendField
		}
		for _, key := range []string{"model", "messages"} {
			for _, encoded := range []string{strings.Repeat("a", length), strings.Repeat(`\u0061`, length)} {
				_, err := readerCollect(t, ".jsonl", readerExtra(strconv.Quote(key)+`:"`+encoded+`"`))
				if !errors.Is(err, want) {
					t.Fatalf("JSON %s length %d: %v", key, length, err)
				}
			}
			for _, encoded := range []string{strings.Repeat("a", length), `"` + strings.Repeat(`""`, length) + `"`, `"` + strings.Repeat("\r\n", length) + `"`} {
				_, err := readerCollect(t, ".csv", readerHeader+","+key+"\nreq,completion,now,1,2,3,"+encoded+"\n")
				if !errors.Is(err, want) {
					t.Fatalf("CSV %s length %d: %v", key, length, err)
				}
			}
		}
		_, err := readerCollect(t, ".jsonl", readerExtra(`"spend":`+strings.Repeat("1", length)))
		if !errors.Is(err, want) {
			t.Fatalf("numeric length %d: %v", length, err)
		}
	}
	for _, n := range []int{MaxFieldBytes / 4, MaxFieldBytes/4 + 1} {
		_, err := readerCollect(t, ".jsonl", readerExtra(`"model":"`+strings.Repeat(`\ud83d\ude00`, n)+`"`))
		if (n*4 <= MaxFieldBytes && err != nil) || (n*4 > MaxFieldBytes && !errors.Is(err, errSpendField)) {
			t.Fatalf("surrogate field size %d: %v", n*4, err)
		}
	}
	for _, ext := range []string{".csv", ".jsonl"} {
		key := strings.Repeat("k", MaxFieldBytes+1)
		body := readerExtra(strconv.Quote(key) + `:null`)
		if ext == ".csv" {
			body = readerHeader + "," + key + "\n"
		}
		_, err := readerCollect(t, ext, body)
		if !errors.Is(err, errSpendField) {
			t.Fatalf("long header %s: %v", ext, err)
		}
	}
}

func TestReaderRowFieldCountAndDepthLimits(t *testing.T) {
	for _, count := range []int{MaxFields, MaxFields + 1} {
		header := readerHeader
		object := strings.TrimSuffix(readerObject, "}")
		for i := 6; i < count; i++ {
			header += fmt.Sprintf(",ignored%d", i)
			object += fmt.Sprintf(",\"ignored%d\":null", i)
		}
		for ext, body := range map[string]string{".csv": header + "\n", ".jsonl": object + "}"} {
			_, err := readerCollect(t, ext, body)
			if (count == MaxFields && err != nil) || (count > MaxFields && !errors.Is(err, errSpendFields)) {
				t.Fatalf("%s field count %d: %v", ext, count, err)
			}
		}
		_, err := readerCollect(t, ".jsonl", readerExtra(`"metadata":`+object+"}"))
		if (count == MaxFields && err != nil) || (count > MaxFields && !errors.Is(err, errSpendFields)) {
			t.Fatalf("nested field count %d: %v", count, err)
		}
	}
	for _, ext := range []string{".json", ".jsonl"} {
		for _, depth := range []int{MaxDepth, MaxDepth + 1, 2000} {
			nesting := depth - 1
			if ext == ".json" {
				nesting--
			}
			body := readerExtra(`"metadata":` + strings.Repeat("[", nesting) + "0" + strings.Repeat("]", nesting))
			if ext == ".json" {
				body = "[" + body + "]"
			}
			_, err := readerCollect(t, ext, body)
			if (depth == MaxDepth && err != nil) || (depth > MaxDepth && !errors.Is(err, errSpendDepth)) {
				t.Fatalf("%s depth %d: %v", ext, depth, err)
			}
		}
	}
	for _, size := range []int{MaxRowBytes, MaxRowBytes + 1} {
		base := strings.TrimSuffix(readerObject, "}")
		jsonBody := base + strings.Repeat(" ", size-len(base)-1) + "}"
		csvBase := ",,,,," + strings.Repeat(","+strings.Repeat("a", MaxFieldBytes), 7) + ","
		csvBody := csvBase + strings.Repeat("a", size-len(csvBase)-1) + "\n"
		for ext, body := range map[string]string{".json": "[" + jsonBody + "]", ".jsonl": jsonBody, ".csv": readerHeader + ",a,b,c,d,e,f,g,h\n" + csvBody} {
			_, err := readerCollect(t, ext, body)
			if (size == MaxRowBytes && err != nil) || (size > MaxRowBytes && !errors.Is(err, errSpendRow)) {
				t.Fatalf("%s row size %d: %v", ext, size, err)
			}
		}
	}
	_, err := readerCollect(t, ".csv", readerHeader+"\n\""+strings.Repeat("a\n", MaxRowBytes))
	if !errors.Is(err, errSpendRow) {
		t.Fatalf("multiline row limit: %v", err)
	}
}

func TestReaderRegularFilesAndSparseLimit(t *testing.T) {
	visit := func(Row) error { return nil }
	dir := t.TempDir()
	file := readerWrite(t, ".json", "[]")
	symlink := filepath.Join(dir, "PRIVATE_PATH_SENTINEL.json")
	if err := os.Symlink(file, symlink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo.csv")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, symlink, fifo, filepath.Join(dir, "missing.csv")} {
		if err := Read(path, visit); !errors.Is(err, errSpendFile) {
			t.Fatalf("nonregular input: %v", err)
		}
	}
	if err := Read(readerWrite(t, ".txt", "[]"), visit); !errors.Is(err, errSpendType) {
		t.Fatalf("extension: %v", err)
	}
	if err := Read(file, nil); err == nil {
		t.Fatal("nil visitor accepted")
	}
	sparse, err := os.Create(filepath.Join(dir, "sparse.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sparse.Truncate(MaxFileBytes + 1); err != nil {
		sparse.Close()
		t.Fatal(err)
	}
	if err := sparse.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Read(sparse.Name(), visit); !errors.Is(err, errSpendSize) {
		t.Fatalf("sparse size limit: %v", err)
	}
}

type readerCountingSource struct {
	r              io.Reader
	bytes, largest int
}

func (r *readerCountingSource) Read(p []byte) (int, error) {
	r.largest = max(r.largest, len(p))
	n, err := r.r.Read(p)
	r.bytes += n
	return n, err
}

type readerErrorSource struct{}

func (readerErrorSource) Read([]byte) (int, error) {
	return 0, errors.New("PRIVATE_PATH_SENTINEL: PRIVATE_CONTENT_SENTINEL")
}

func TestReaderByteBudgetGrowthAndVisitorStop(t *testing.T) {
	for _, extra := range []string{"", " "} {
		source := &readerCountingSource{r: strings.NewReader("[]" + extra)}
		err := readSpendStream(source, ".json", 2, func(Row) error { return nil })
		if (extra == "" && err != nil) || (extra != "" && !errors.Is(err, errSpendSize)) {
			t.Fatalf("budget: %v", err)
		}
		if source.bytes > 3 || source.largest > 2 {
			t.Fatalf("over-read: %#v", source)
		}
	}
	path := readerWrite(t, ".jsonl", readerObject+"\n")
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = readSpendStream(file, ".jsonl", int64(len(readerObject)+1), func(Row) error {
		appender, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = appender.WriteString(readerObject + "\n")
		closeErr := appender.Close()
		return errors.Join(err, closeErr)
	})
	if !errors.Is(err, errSpendSize) {
		t.Fatalf("file growth: %v", err)
	}
	if err := readSpendStream(readerErrorSource{}, ".json", MaxFileBytes, func(Row) error { return nil }); !errors.Is(err, errSpendRead) {
		t.Fatalf("unsafe I/O error: %v", err)
	}
	stop := errors.New("visitor stopped")
	for ext, body := range map[string]string{
		".csv":   readerHeader + "\n" + strings.Repeat("req,completion,now,1,2,3\n", 10000),
		".json":  "[" + strings.Repeat(readerObject+",", 10000) + readerObject + "]",
		".jsonl": strings.Repeat(readerObject+"\n", 10000),
	} {
		source := &readerCountingSource{r: strings.NewReader(body)}
		visits := 0
		err := readSpendStream(source, ext, MaxFileBytes, func(Row) error { visits++; return stop })
		if !errors.Is(err, stop) || visits != 1 || source.bytes > 4096 || source.largest > 4096 {
			t.Fatalf("%s did not stream/stop: visits %d, read %d, error %v", ext, visits, source.bytes, err)
		}
	}
}

type readerRepeatingSource struct {
	text     string
	position int
}

func (r *readerRepeatingSource) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.text[r.position]
		r.position = (r.position + 1) % len(r.text)
	}
	return len(p), nil
}

func TestReaderMillionRowLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("million-row boundary")
	}
	for _, count := range []int{MaxRows, MaxRows + 1} {
		body := io.LimitReader(&readerRepeatingSource{text: ",,,,,\n"}, int64(count*6))
		input := io.MultiReader(strings.NewReader(readerHeader+"\n"), body)
		visits := 0
		err := readSpendStream(input, ".csv", MaxFileBytes, func(row Row) error {
			visits++
			if row.Number != visits || len(row.Values) != 0 {
				t.Fatalf("invalid record %d", visits)
			}
			return nil
		})
		if visits != MaxRows || (count == MaxRows && err != nil) || (count > MaxRows && !errors.Is(err, errSpendRows)) {
			t.Fatalf("count %d: visits %d, error %v", count, visits, err)
		}
	}
}

func BenchmarkReaderCSVTwoPassMillion(b *testing.B) {
	path := filepath.Join(b.TempDir(), "spend.csv")
	f, err := os.Create(path)
	if err != nil {
		b.Fatal(err)
	}
	header := readerHeader + ",api_key,model,user,team_id,organization_id,end_user,spend,status,session_id,cache_read_input_tokens,cache_write_input_tokens,reasoning_output_tokens,messages\n"
	row := "request-000000001,completion,2026-10-08T12:34:56Z,1000,250,1250,synthetic-key,synthetic-model,synthetic-user,synthetic-team,synthetic-org,synthetic-end-user,0.012300,success,synthetic-session,100,50,25,ignored-content\n"
	if _, err := f.WriteString(header); err != nil {
		b.Fatal(err)
	}
	chunk := strings.Repeat(row, 1000)
	for i := 0; i < MaxRows/1000; i++ {
		if _, err := f.WriteString(chunk); err != nil {
			b.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(2 * (len(header) + MaxRows*len(row))))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for pass := 0; pass < 2; pass++ {
			visits := 0
			if err := Read(path, func(row Row) error { visits++; return nil }); err != nil {
				b.Fatal(err)
			}
			if visits != MaxRows {
				b.Fatal("incorrect row count")
			}
		}
	}
}
