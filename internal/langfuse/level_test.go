// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func withLevel(row, raw string) string {
	if raw == "" {
		return row
	}
	return strings.TrimSuffix(strings.TrimSpace(row), "}") + `,"level":` + raw + "}\n"
}

func levelEncoding(t *testing.T, rows []string, kind string) []byte {
	t.Helper()
	if kind == "JSONL" {
		return []byte(strings.Join(rows, ""))
	}
	if kind == "JSON" {
		return []byte("[" + strings.Join(strings.FieldsFunc(strings.TrimSpace(strings.Join(rows, "")), func(r rune) bool { return r == '\n' }), ",") + "]")
	}
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	header := []string{"id", "project_id", "type", "start_time", "end_time", "provided_model_name", "user_id", "session_id", "environment", "release", "trace_name", "prompt_name", "tags", "usage_details", "total_cost", "level", "input", "output", "metadata", "tool_calls", "status_message"}
	if err := w.Write(header); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(row), &fields); err != nil {
			t.Fatal(err)
		}
		values := make([]string, len(header))
		for i, key := range header {
			if raw := fields[key]; raw != nil {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					values[i] = s
				} else {
					values[i] = string(raw)
				}
			}
		}
		if err := w.Write(values); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestLevelEnumSixFormatsAndPrivacy(t *testing.T) {
	raw := []string{`"DEFAULT"`, `"DEBUG"`, `"WARNING"`, `"ERROR"`, `"\u0045RROR"`, `"\u0057\u0041\u0052\u004e\u0049\u004e\u0047"`, "", `null`, `false`, `123`, `"error"`, `" ERROR"`, `"INFO"`, `"SUCCESS"`, `"PRIVATE_LEVEL"`, `{"private":"PRIVATE_LEVEL"}`, `"` + strings.Repeat("PRIVATE_LEVEL", 1000) + `"`, ` "ERROR"`}
	want := []string{"DEFAULT", "DEBUG", "WARNING", "ERROR", "ERROR", "WARNING", "unknown", "unknown", "unknown", "unknown", "unknown", "unknown", "unknown", "unknown", "unknown", "unknown", "unknown", "ERROR"}
	var rows []string
	for i, level := range raw {
		row := strings.TrimSpace(withLevel(rowJSON(fmt.Sprint(i), "project", "2026-10-10"), level))
		row = strings.TrimSuffix(row, "}") + `,"input":"PRIVATE_INPUT","output":{"x":"PRIVATE_OUTPUT"},"metadata":["PRIVATE_METADATA"],"tool_calls":"PRIVATE_TOOLS","status_message":"PRIVATE_STATUS"}` + "\n"
		rows = append(rows, row)
	}
	var fingerprints [][32]byte
	for _, kind := range []string{"JSON", "JSONL", "CSV"} {
		for _, zip := range []bool{false, true} {
			for _, chunk := range []int{1, 7, 31} {
				t.Run(fmt.Sprintf("%s/gzip=%v/chunk=%d", kind, zip, chunk), func(t *testing.T) {
					data := levelEncoding(t, rows, kind)
					if zip {
						data = compressed(t, data)
					}
					i := 0
					report, err := readFile(chunkReader{bytes.NewReader(data), chunk}, descriptor(kind, zip, data, int64(len(rows))), func(p projection) error {
						row, _, fingerprint, err := mappedRow(p, "user_id", "")
						if err != nil || row.ObservationLevel != want[i] || row.Values["status"] != "" {
							t.Fatal("enum projection or call-status boundary changed")
						}
						if kind == "JSON" && !zip && chunk == 1 {
							fingerprints = append(fingerprints, fingerprint)
						} else if fingerprints[i] != fingerprint {
							t.Fatal("normalized duplicate fingerprint differs across formats")
						}
						for _, key := range []string{"level", "input", "output", "metadata", "tool_calls", "status_message"} {
							if _, ok := p.fields[key]; ok {
								t.Fatal("raw enum or forbidden payload retained")
							}
						}
						encoded, _ := json.Marshal(row)
						for _, sentinel := range []string{"PRIVATE_LEVEL", "PRIVATE_INPUT", "PRIVATE_OUTPUT", "PRIVATE_METADATA", "PRIVATE_TOOLS", "PRIVATE_STATUS"} {
							if bytes.Contains(encoded, []byte(sentinel)) {
								t.Fatal("private value leaked")
							}
						}
						i++
						return nil
					})
					if err != nil || i != len(rows) || report.InclusiveInput != uint64(len(rows)*100) || report.InclusiveOutput != uint64(len(rows)*40) || report.RecordedCost != "0.18" {
						t.Fatal("level changed accounting or bounded reader", err)
					}
				})
			}
		}
	}
}

func TestLevelEscapedKeyAndDuplicateCSVHeader(t *testing.T) {
	data := []byte(strings.Replace(withLevel(rowJSON("one", "project", "2026-10-10"), `"ERROR"`), `"level"`, `"le\u0076el"`, 1))
	if err := readJSON(bytes.NewReader(data), false, func(p projection) error {
		if p.level != "ERROR" {
			t.Fatal("escaped level key was not projected")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data = []byte("id,level,level\none,ERROR,PRIVATE_LEVEL\n")
	if err := readCSV(bytes.NewReader(data), func(projection) error { t.Fatal("duplicate header released a row"); return nil }); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("duplicate level header accepted or leaked")
	}
}

func TestLevelSeverityReviewAndErrorSubset(t *testing.T) {
	root := t.TempDir()
	var body string
	for _, day := range []string{"2026-10-10", "2026-10-17"} {
		for i, level := range []string{`"DEFAULT"`, `"DEBUG"`, `"WARNING"`, `"ERROR"`, "", `"PRIVATE_LEVEL"`} {
			body += withLevel(rowJSON(fmt.Sprintf("%s-%d", day, i), "project", day), level)
		}
	}
	addRun(t, root, "project", "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 12)
	r, err := Investigate(root, "user_id", "", options())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		tokens                           uint64
		cost                             string
		unknown, unclear, failed, errors uint64
	}{
		{r.Before.Tokens, r.Before.RecordedSpend, r.Before.Quality.UnknownStatus, r.Before.Quality.Unclear, r.Before.Quality.Failed, r.Before.Quality.ErrorObservations},
		{r.After.Tokens, r.After.RecordedSpend, r.After.Quality.UnknownStatus, r.After.Quality.Unclear, r.After.Quality.Failed, r.After.Quality.ErrorObservations},
	} {
		if w.tokens != 840 || w.cost != "0.06" || w.unknown != 6 || w.unclear != 3 || w.failed != 0 || w.errors != 1 {
			t.Fatal("severity was treated as success/failure or changed totals/review")
		}
	}
	if r.Before.ErrorObservationTokens == nil || r.Before.ErrorObservationTokens.Total != 140 || r.Before.ErrorObservationTokens.Input != 100 || r.Before.ErrorObservationTokens.Output != 40 || r.Before.ErrorObservationTokens.Finality != "unknown" || r.Before.FailureTokens != nil || r.Volume != nil || len(r.Models) != 1 || r.Models[0].Volume != nil || r.Models[0].Before.ErrorObservationTokens.Total != 140 {
		t.Fatal("ERROR subset or conservative call-status gate differs")
	}
	for _, level := range []string{"DEFAULT", "DEBUG", "WARNING", "ERROR"} {
		if r.Before.Quality.ObservationLevels[level] != 1 || r.After.Quality.ObservationLevels[level] != 1 {
			t.Fatal("known severity counter differs")
		}
	}
	if r.Before.Quality.ObservationLevels["unknown"] != 2 {
		t.Fatal("unknown severity not counted")
	}
	b, _ := json.Marshal(r)
	if bytes.Contains(b, []byte("PRIVATE_")) {
		t.Fatal("private severity or identity leaked")
	}
}

func TestLevelDedupAndConflict(t *testing.T) {
	for _, other := range []string{`"\u0045RROR"`, `"DEFAULT"`, "", `"PRIVATE_LEVEL"`} {
		t.Run(other, func(t *testing.T) {
			root := t.TempDir()
			row := rowJSON("one", "project", "2026-10-10")
			addRun(t, root, "project", "one", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", withLevel(row, `"ERROR"`), 1)
			addRun(t, root, "project", "two", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", withLevel(row, other), 1)
			r, err := Investigate(root, "user_id", "", options())
			if other == `"\u0045RROR"` {
				if err != nil || r.Coverage.Duplicates != 1 || r.Before.Quality.ErrorObservations != 1 {
					t.Fatal("equivalent enum not deduplicated", err)
				}
			} else if err == nil || strings.Contains(err.Error(), "PRIVATE_") || r.Before.Requests != 0 {
				t.Fatal("conflict accepted, leaked or returned partial report")
			}
		})
	}
}

func TestMalformedLevelDoesNotReleasePrefix(t *testing.T) {
	for _, suffix := range []string{`,"level":"ERROR","le\u0076el":"DEFAULT"}`, `,"level":"\uD800"}`, `,"level":{"x":"PRIVATE_LEVEL"}`} {
		row := strings.TrimSuffix(strings.TrimSpace(rowJSON("two", "project", "2026-10-17")), "}") + suffix
		if strings.Contains(suffix, "PRIVATE_LEVEL") {
			row = strings.TrimSuffix(row, "}") // malformed nested value
		}
		data := []byte(withLevel(rowJSON("one", "project", "2026-10-10"), `"DEFAULT"`) + row)
		r, err := ReadFile(bytes.NewReader(data), descriptor("JSONL", false, data, 2))
		if err == nil || r.PhysicalRows != 0 || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("malformed level released aggregate prefix or private diagnostics")
		}
	}
}
