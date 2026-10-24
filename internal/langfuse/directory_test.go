// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func options() spend.Options {
	return spend.Options{BeforePeriod: "2026-10-10", AfterPeriod: "2026-10-17", Top: 20, Now: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)}
}

func rowJSON(id, project, day string) string {
	return `{"id":"` + id + `","project_id":"` + project + `","type":"GENERATION","start_time":"` + day + ` 12:00:00.000000","end_time":"` + day + ` 12:00:01.000000","provided_model_name":"PRIVATE_MODEL","user_id":"PRIVATE_USER","session_id":"PRIVATE_SESSION","environment":"PRIVATE_ENV","release":"PRIVATE_RELEASE","trace_name":"PRIVATE_TRACE","prompt_name":"PRIVATE_PROMPT","tags":["PRIVATE_TAG"],"total_cost":0.01,"usage_details":{"input":60,"input_cached_tokens":30,"input_cache_creation":10,"output":35,"output_reasoning_tokens":5,"total":140}}` + "\n"
}

func put(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(name))
	if os.MkdirAll(filepath.Dir(p), 0700) != nil || os.WriteFile(p, data, 0600) != nil {
		t.Fatal("write fixture")
	}
}

func addRun(t *testing.T, root, project, run, start, end, body string, count int64) string {
	t.Helper()
	f := descriptor("JSONL", false, []byte(body), count)
	f.Key = "prefix/" + project + "/observations_v2/" + run + ".jsonl"
	name := project + "/observations_v2/" + run + ".jsonl"
	put(t, root, name, []byte(body))
	m := syntheticManifest(t, "JSONL", false)
	m["projectId"] = project
	m["window"] = map[string]string{"minTimestamp": start, "maxTimestamp": end}
	m["maxTimestamp"] = end
	m["files"] = []any{map[string]any{"key": f.Key, "table": f.Table, "fileType": f.FileType, "format": f.Format, "compressed": f.Compressed, "contentType": f.ContentType, "sizeBytes": f.SizeBytes, "rowCount": f.RowCount}}
	b, _ := json.Marshal(m)
	put(t, root, project+"/manifests/"+run+".json", b)
	return name
}

func standard(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	addRun(t, root, "project", "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", rowJSON("one", "project", "2026-10-10")+rowJSON("two", "project", "2026-10-17"), 2)
	return root
}

func TestDirectoryCompositionAndGrouping(t *testing.T) {
	root := standard(t)
	a, err := Investigate(root, "user_id", "", options())
	b, otherErr := Investigate(filepath.Join(root, "project"), "user_id", "", options())
	if err != nil || otherErr != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("project-root and bucket-root binding disagree")
	}
	for _, group := range []string{"user_id", "session_id", "provided_model_name", "environment", "release", "trace_name", "prompt_name", "tag"} {
		t.Run(group, func(t *testing.T) {
			tag := ""
			if group == "tag" {
				tag = "PRIVATE_TAG"
			}
			r, err := Investigate(root, group, tag, options())
			if err != nil {
				t.Fatal(err)
			}
			if r.Coverage.Incomplete || r.Coverage.UniqueRows != 2 || r.Before.Tokens != 140 || r.After.Tokens != 140 || r.Before.Requests != 1 || r.Before.RecordedSpend != "0.01" {
				t.Fatalf("incorrect composition: %+v", r.Coverage)
			}
			if r.Before.Counters[accounting.Prefix+"input_tokens_total"] != 100 || r.Before.Counters[accounting.Prefix+"output_tokens_total"] != 40 || r.Before.Counters[accounting.Prefix+"cache_read_input_tokens_total"] != 30 || r.Before.Counters[accounting.Prefix+"reasoning_output_tokens_total"] != 5 {
				t.Fatal("disjoint components not recomposed exactly once")
			}
			b, _ := json.Marshal(r)
			if bytes.Contains(b, []byte("PRIVATE_")) {
				t.Fatal("raw label leaked")
			}
			if r.GroupBy != group || len(r.Rankings.Movers) != 1 || !strings.HasPrefix(r.Rankings.Movers[0].Item, group+"-") {
				t.Fatal("group not composed")
			}
		})
	}
	if _, err := Investigate(root, "tag", "", options()); err == nil {
		t.Fatal("empty tag accepted")
	}
	if _, err := Investigate(root, "user_id", "PRIVATE_TAG", options()); err == nil {
		t.Fatal("unused tag accepted")
	}
}

func TestDirectoryDedupProjectAndConflict(t *testing.T) {
	root := standard(t)
	body := rowJSON("one", "project", "2026-10-10") + rowJSON("two", "project", "2026-10-17")
	addRun(t, root, "project", "overlap", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 2)
	r, err := Investigate(root, "user_id", "", options())
	if err != nil || r.Coverage.Duplicates != 2 || r.Coverage.UniqueRows != 2 {
		t.Fatal("overlap not deduplicated", err)
	}
	addRun(t, root, "other", "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", rowJSON("one", "other", "2026-10-10")+rowJSON("two", "other", "2026-10-17"), 2)
	r, err = Investigate(root, "user_id", "", options())
	if err != nil || r.Coverage.UniqueRows != 4 || r.Before.Tokens != 280 || r.Coverage.Projects != 2 {
		t.Fatal("identity not project scoped", err)
	}
	body = strings.ReplaceAll(body, `"input":60`, `"input":61`)
	addRun(t, root, "project", "conflict", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 2)
	if _, err = Investigate(root, "user_id", "", options()); err == nil || strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("conflicting projection accepted or leaked")
	}
}

func TestDirectoryCoverageFailures(t *testing.T) {
	for _, mode := range []string{"missing", "size", "rows", "orphan", "gap", "no-manifest", "project-mismatch", "symlink", "root-link", "fifo"} {
		t.Run(mode, func(t *testing.T) {
			root := standard(t)
			body := rowJSON("third", "project", "2026-10-17")
			name := addRun(t, root, "project", "bad", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 1)
			switch mode {
			case "missing":
				os.Remove(filepath.Join(root, name))
			case "size":
				put(t, root, name, []byte(body+" "))
			case "rows":
				addRun(t, root, "project", "bad", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 2)
			case "orphan":
				put(t, root, "project/observations_v2/orphan.parquet", []byte("NOT_DECODED"))
			case "gap":
				os.Remove(filepath.Join(root, "project/manifests/run.json"))
				addRun(t, root, "project", "bad", "2026-10-17T00:00:00Z", "2026-10-18T00:00:00Z", body, 1)
				addRun(t, root, "project", "early", "2026-10-10T00:00:00Z", "2026-10-11T00:00:00Z", rowJSON("early", "project", "2026-10-10"), 1)
			case "no-manifest":
				os.RemoveAll(filepath.Join(root, "project/manifests"))
			case "project-mismatch":
				addRun(t, root, "project", "bad", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", rowJSON("bad", "wrong", "2026-10-17"), 1)
			case "symlink":
				os.Remove(filepath.Join(root, name))
				os.Symlink(filepath.Join(root, "project/observations_v2/run.jsonl"), filepath.Join(root, name))
			case "root-link":
				link := filepath.Join(t.TempDir(), "link")
				os.Symlink(root, link)
				root = link
			case "fifo":
				makeFIFO(t, filepath.Join(root, "fifo"))
			}
			r, err := Investigate(root, "user_id", "", options())
			fatal := mode == "no-manifest" || mode == "symlink" || mode == "root-link" || mode == "fifo"
			if fatal {
				if err == nil {
					t.Fatal("unsafe/incomplete root accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !r.Coverage.Incomplete || !r.Incomplete || r.Volume != nil || r.Increase != nil || r.Cache.Rankings != nil || !strings.Contains(r.TotalsScope, "partial") {
				t.Fatal("incomplete coverage treated as zero/complete")
			}
		})
	}
}

func TestNormalizedDuplicateProjection(t *testing.T) {
	var ids [][32]byte
	var fingerprints [][32]byte
	for _, body := range []string{rowJSON("id", "project", "2026-10-10"), strings.ReplaceAll(rowJSON("id", "project", "2026-10-10"), `"total_cost":0.01`, `"total_cost":1e-2`)} {
		err := readJSON(strings.NewReader(body), false, func(p projection) error {
			_, id, fingerprint, err := mappedRow(p, "user_id", "")
			ids = append(ids, id)
			fingerprints = append(fingerprints, fingerprint)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if ids[0] != ids[1] || fingerprints[0] != fingerprints[1] {
		t.Fatal("equivalent decimal projections conflict")
	}
}

func TestMappedMissingInvalidAndPrivacy(t *testing.T) {
	root := t.TempDir()
	good := rowJSON("good", "project", "2026-10-10")
	partial := strings.ReplaceAll(rowJSON("partial", "project", "2026-10-17"), `"input_cache_creation":10,`, "")
	missing := strings.ReplaceAll(rowJSON("missing", "project", "2026-10-17"), `{"input":60,"input_cached_tokens":30,"input_cache_creation":10,"output":35,"output_reasoning_tokens":5,"total":140}`, `{}`)
	invalid := strings.ReplaceAll(rowJSON("invalid", "project", "2026-10-17"), `"input":60`, `"input":-1`)
	unknown := strings.ReplaceAll(rowJSON("unknown", "project", "2026-10-17"), `"input":60`, `"experimental_extra":{"secret":"FORBIDDEN_SENTINEL"},"input":60`)
	zero := strings.ReplaceAll(rowJSON("zero", "project", "2026-10-17"), `"total_cost":0.01`, `"total_cost":0`)
	body := good + partial + missing + invalid + unknown + zero
	body = strings.ReplaceAll(body, `"id":`, `"input":{"secret":"FORBIDDEN_SENTINEL"},"output":["FORBIDDEN_SENTINEL"],"metadata":{"s":"FORBIDDEN_SENTINEL"},"tool_calls":[{"s":"FORBIDDEN_SENTINEL"}],"status_message":"FORBIDDEN_SENTINEL","id":`)
	addRun(t, root, "project", "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 6)
	r, err := Investigate(root, "user_id", "", options())
	if err != nil {
		t.Fatal(err)
	}
	if r.Coverage.MissingUsage != 1 || r.Coverage.PartialUsage != 2 || r.Coverage.InvalidUsage != 1 || r.Coverage.UnknownCategories != 1 || r.Coverage.UsageZeroCost != 1 {
		t.Fatalf("missing/partial/invalid coverage lost: %+v", r.Coverage)
	}
	if r.After.Quality.Missing == 0 || r.After.Quality.Invalid == 0 {
		t.Fatal("missing/invalid became zero")
	}
	b, _ := json.Marshal(r)
	if bytes.Contains(b, []byte("FORBIDDEN_SENTINEL")) || bytes.Contains(b, []byte("PRIVATE_")) {
		t.Fatal("privacy sentinel leaked")
	}
}

func TestDirectoryAutomaticPeriodsAndUTC(t *testing.T) {
	root := t.TempDir()
	addRun(t, root, "project", "run", "2026-10-01T00:00:00Z", "2026-10-23T00:00:00Z", rowJSON("first", "project", "2026-10-01")+rowJSON("mid", "project", "2026-10-10")+rowJSON("last", "project", "2026-10-22"), 3)
	opts := options()
	opts.BeforePeriod = ""
	opts.AfterPeriod = ""
	r, err := Investigate(root, "user_id", "", opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.Before.Period.Start.Format("2006-01-02") != "2026-10-08" || r.After.Period.End.Format("2006-01-02") != "2026-10-22" {
		t.Fatal("automatic UTC periods diverged")
	}
	opts = options()
	opts.BeforePeriod = "2026-10-10T02:00:00+02:00/2026-10-11T02:00:00+02:00"
	opts.AfterPeriod = "2026-10-17T00:00:00Z/2026-10-18T00:00:00Z"
	r, err = Investigate(root, "user_id", "", opts)
	if err != nil || r.Before.Period.Start.Hour() != 0 || r.Before.Requests != 1 {
		t.Fatal("UTC explicit boundary failed", err)
	}
}

func TestDirectoryBoundedTotalsAndCostQuality(t *testing.T) {
	for _, mode := range []string{"token-overflow", "aggregate-cost", "missing-cost", "invalid-cost"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			body := rowJSON("one", "project", "2026-10-10") + rowJSON("two", "project", "2026-10-17")
			switch mode {
			case "token-overflow":
				body = strings.ReplaceAll(body, `"input":60`, `"input":9223372036854775807`)
			case "aggregate-cost":
				body = strings.ReplaceAll(body, `"total_cost":0.01`, `"total_cost":1000000000000000000`)
			case "missing-cost":
				body = strings.ReplaceAll(body, `"total_cost":0.01,`, "")
			case "invalid-cost":
				body = strings.ReplaceAll(body, `"total_cost":0.01`, `"total_cost":-1`)
			}
			addRun(t, root, "project", "run", "2026-10-10T00:00:00Z", "2026-10-18T00:00:00Z", body, 2)
			r, err := Investigate(root, "user_id", "", options())
			if mode == "token-overflow" || mode == "aggregate-cost" {
				if err == nil {
					t.Fatal("over-limit aggregate accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "missing-cost" && r.Before.Quality.SpendMissing != 1 || mode == "invalid-cost" && r.Before.Quality.SpendInvalid != 1 || r.Before.Quality.SpendZeroUnknown != 0 {
				t.Fatal("missing/invalid cost mislabeled zero")
			}
		})
	}
}

func TestDirectoryAllMissingIsCoverageGap(t *testing.T) {
	root := standard(t)
	if os.Remove(filepath.Join(root, "project/observations_v2/run.jsonl")) != nil {
		t.Fatal("remove fixture")
	}
	r, err := Investigate(root, "user_id", "", options())
	if err != nil || !r.Incomplete || r.AnalysisGap == "" || r.Coverage.MissingFiles != 1 || !r.Coverage.Incomplete || r.Coverage.UncoveredPeriods != 2 {
		t.Fatal("all-missing export did not retain explicit coverage gap", err)
	}
}

// Native exports are read only through the selective lexer. Seals are hashed as
// bytes; their payload values are never unmarshaled. This test is opt-in so the
// normal suite needs neither private paths nor Docker.
func TestSealedNativeCapture(t *testing.T) {
	root := os.Getenv("FLEETDIFF_LANGFUSE_SEALED_CAPTURE")
	if root == "" {
		t.Skip("private sealed capture not configured")
	}
	b, err := os.ReadFile(filepath.Join(root, "SEAL.json"))
	if err != nil {
		t.Fatal(err)
	}
	if h := sha256.Sum256(b); hex.EncodeToString(h[:]) != "80ace59badf56837d3cf430551a4d8399b4d71b86003da5966165b660447154e" {
		t.Fatal("fresh r3 seal differs")
	}
	var seal struct {
		Success bool `json:"success"`
		Stopped bool `json:"all_writers_stopped"`
		Files   map[string]struct {
			Bytes int64  `json:"bytes"`
			SHA   string `json:"sha256"`
		} `json:"files"`
	}
	if json.Unmarshal(b, &seal) != nil || !seal.Success || !seal.Stopped {
		t.Fatal("capture not sealed successful")
	}
	for name, want := range seal.Files {
		f, err := os.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal("sealed payload absent")
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil || n != want.Bytes || hex.EncodeToString(h.Sum(nil)) != want.SHA {
			t.Fatal("sealed payload integrity failed")
		}
	}
	var baseline Report
	nativeOpts := options()
	nativeOpts.BeforePeriod = "2026-10-10T12:00:00Z/2026-10-11T12:00:00Z"
	nativeOpts.AfterPeriod = "2026-10-17T12:00:00Z/2026-10-18T12:00:00Z"
	for i, variant := range []string{"jsonl-plain-confirmed", "jsonl-gzip", "json-plain", "json-gzip", "csv-plain", "csv-gzip"} {
		r, err := Investigate(filepath.Join(root, "exports", variant), "user_id", "", nativeOpts)
		if err != nil {
			t.Fatalf("native %s: %v", variant, err)
		}
		if r.Coverage.Manifests != 2 || r.Coverage.UniqueRows != 28 || r.Coverage.GenerationRows != 10 || r.Coverage.PhysicalRows != 28 || r.Coverage.Incomplete || len(r.Coverage.OtherTypes) != 9 {
			t.Fatalf("native %s coverage: %+v", variant, r.Coverage)
		}
		if i == 0 {
			baseline = r
		} else if !reflect.DeepEqual(baseline, r) {
			t.Fatalf("native %s report differs across encodings", variant)
		}
	}
	all, err := Investigate(filepath.Join(root, "exports"), "user_id", "", nativeOpts)
	if err != nil {
		t.Fatal(err)
	}
	if all.Coverage.UniqueRows != 28 || all.Coverage.PhysicalRows != 168 || all.Coverage.Duplicates != 140 || all.Before.Tokens != baseline.Before.Tokens {
		t.Fatal("cross-format canonical dedup failed")
	}
	t.Logf("seal verified: %d payloads; six native formats, 12 manifests, 168 physical / 28 unique rows / 140 duplicate copies", len(seal.Files))
	if dest := os.Getenv("FLEETDIFF_LANGFUSE_SAFE_FIXTURES"); dest != "" {
		writeSafeFixtures(t, root, dest)
	}
}

func writeSafeFixtures(t *testing.T, root, dest string) {
	t.Helper()
	base := filepath.Join(root, "exports", "jsonl-plain-confirmed")
	err := filepath.WalkDir(base, func(name string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(name, ".jsonl") || !strings.Contains(name, "observations_v2") {
			return nil
		}
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
		var out bytes.Buffer
		err = readJSON(f, false, func(p projection) error {
			row := map[string]json.RawMessage{}
			for k, v := range p.fields {
				row[k] = v
			}
			usage := map[string]json.RawMessage{}
			for native, normalized := range usageKeys {
				if v := p.usage[normalized]; len(v) > 0 {
					usage[native] = v
				}
			}
			// Retain only the count of unmapped categories in an explicitly
			// transformed fixture; never retain their names or values.
			if p.unknown > 0 {
				usage["unmapped_fixture_category"] = json.RawMessage(`0`)
			}
			b, _ := json.Marshal(usage)
			row["usage_details"] = b
			return json.NewEncoder(&out).Encode(row)
		})
		if err != nil {
			return err
		}
		put(t, dest, filepath.Base(name), out.Bytes())
		return nil
	})
	if err != nil {
		t.Fatal("safe fixture projection failed", err)
	}
}

func safeEncoding(t *testing.T, data []byte, kind string) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if kind == "JSONL" {
		return data
	}
	if kind == "JSON" {
		return append(append([]byte("["), bytes.Join(lines, []byte(","))...), ']')
	}
	header := []string{"id", "project_id", "type", "start_time", "end_time", "usage_details", "total_cost", "provided_model_name", "user_id", "session_id", "environment", "release", "trace_name", "prompt_name", "tags"}
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	if w.Write(header) != nil {
		t.Fatal("CSV header")
	}
	for _, line := range lines {
		var row map[string]json.RawMessage
		if json.Unmarshal(line, &row) != nil {
			t.Fatal("safe projection")
		}
		for key := range row {
			if !rowKeys[key] {
				t.Fatal("unexpected field in safe fixture")
			}
		}
		values := make([]string, len(header))
		for i, key := range header {
			raw := row[key]
			if len(raw) == 0 || string(raw) == "null" {
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) == nil {
				values[i] = s
			} else {
				values[i] = string(raw)
			}
		}
		if w.Write(values) != nil {
			t.Fatal("CSV fixture")
		}
	}
	w.Flush()
	if w.Error() != nil {
		t.Fatal("CSV flush")
	}
	return out.Bytes()
}

func freshProjectionRoot(t *testing.T, kind string, zip bool) string {
	t.Helper()
	root := t.TempDir()
	paths, err := filepath.Glob("testdata/fresh-r3/*.jsonl")
	if err != nil || len(paths) != 2 {
		t.Fatal("fresh safe projections missing")
	}
	for _, name := range paths {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		data = safeEncoding(t, data, kind)
		if zip {
			data = compressed(t, data)
		}
		f := descriptor(kind, zip, data, 14)
		stem := strings.TrimSuffix(filepath.Base(name), ".jsonl")
		ext := strings.ToLower(kind)
		if zip {
			ext += ".gz"
		}
		f.Key = "captures/synthetic-capture-project/observations_v2/" + stem + "." + ext
		put(t, root, "synthetic-capture-project/observations_v2/"+stem+"."+ext, data)
		start, end := "2026-10-10T12:00:00Z", "2026-10-17T12:00:00Z"
		if strings.HasPrefix(stem, "2026-10-23") {
			start, end = "2026-10-17T12:00:00Z", "2026-10-23T13:19:25.482Z"
		}
		m := syntheticManifest(t, kind, zip)
		m["projectId"] = "synthetic-capture-project"
		m["window"] = map[string]string{"minTimestamp": start, "maxTimestamp": end}
		m["maxTimestamp"] = end
		m["files"] = []any{map[string]any{"key": f.Key, "table": f.Table, "fileType": f.FileType, "format": f.Format, "compressed": f.Compressed, "contentType": f.ContentType, "sizeBytes": f.SizeBytes, "rowCount": f.RowCount}}
		b, _ := json.Marshal(m)
		put(t, root, "synthetic-capture-project/manifests/"+stem+".json", b)
	}
	return root
}

func TestFreshSafeProjectionSixFormatsGolden(t *testing.T) {
	opts := options()
	opts.BeforePeriod = "2026-10-10T12:00:00Z/2026-10-11T12:00:00Z"
	opts.AfterPeriod = "2026-10-17T12:00:00Z/2026-10-18T12:00:00Z"
	for _, kind := range []string{"JSONL", "JSON", "CSV"} {
		for _, zip := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "-plain", true: "-gzip"}[zip], func(t *testing.T) {
				r, err := Investigate(freshProjectionRoot(t, kind, zip), "user_id", "", opts)
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.MarshalIndent(r, "", "  ")
				b = append(b, '\n')
				golden := "testdata/fresh-r3/report.json"
				if os.Getenv("FLEETDIFF_UPDATE_LANGFUSE_GOLDEN") == "1" {
					if os.WriteFile(golden, b, 0600) != nil {
						t.Fatal("golden write")
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil || !bytes.Equal(want, b) {
					t.Fatal("fresh safe-projection report differs from golden")
				}
			})
		}
	}
}
