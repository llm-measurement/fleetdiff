// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/cli"
	"github.com/llm-measurement/fleetdiff/internal/langfuse"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func limitDescriptor(data []byte, rows int64, zipped bool) langfuse.File {
	f := langfuse.File{Key: "prefix/p/observations_v2/PRIVATE_LIMIT_SENTINEL.jsonl", Table: "observations_v2", FileType: "JSONL", Format: "jsonl-raw", ContentType: "application/x-ndjson; charset=utf-8", SizeBytes: int64(len(data)), RowCount: rows}
	if zipped {
		f.Key += ".gz"
		f.Compressed = true
		f.Format = "jsonl-gzip"
		f.ContentType = "application/gzip"
	}
	return f
}

func limitDirectory(t *testing.T, data []byte, rows int64, zipped bool, change func(*langfuse.File)) string {
	t.Helper()
	root := t.TempDir()
	f := limitDescriptor(data, rows, zipped)
	if change != nil {
		change(&f)
	}
	body := filepath.Join(root, "p/observations_v2", filepath.Base(f.Key))
	if os.MkdirAll(filepath.Dir(body), 0700) != nil || os.WriteFile(body, data, 0600) != nil {
		t.Fatal("limit body fixture")
	}
	m := map[string]any{"version": 1, "projectId": "p", "exportSource": "EVENTS", "window": map[string]string{"minTimestamp": "2026-10-10T00:00:00Z", "maxTimestamp": "2026-10-18T00:00:00Z"}, "maxTimestamp": "2026-10-18T00:00:00Z", "files": []any{map[string]any{"key": f.Key, "table": f.Table, "fileType": f.FileType, "format": f.Format, "contentType": f.ContentType, "compressed": f.Compressed, "sizeBytes": f.SizeBytes, "rowCount": f.RowCount}}}
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "p/manifests/PRIVATE_LIMIT_SENTINEL.json")
	if os.MkdirAll(filepath.Dir(p), 0700) != nil || os.WriteFile(p, manifest, 0600) != nil {
		t.Fatal("limit manifest fixture")
	}
	return root
}

func requireLimit(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, langfuse.ErrFileLimit) {
		t.Fatal("limit refusal lost its actionable classification", err)
	}
	for _, text := range []string{"1 MiB", "16 MiB", "100000 rows", "hourly export schedule"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatal("limit remedy missing", text)
		}
	}
	if strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("limit diagnostic leaked private input")
	}
}

func TestDirectoryAndCLILimitRefusals(t *testing.T) {
	for _, mode := range []string{"declared_size", "declared_rows", "manifest_bytes", "stored_bytes", "expanded_gzip_bytes", "actual_rows"} {
		t.Run(mode, func(t *testing.T) {
			data := []byte{}
			rows := int64(0)
			zipped := false
			var change func(*langfuse.File)
			switch mode {
			case "declared_size":
				change = func(f *langfuse.File) { f.SizeBytes = langfuse.MaxFileBytes + 1 }
			case "declared_rows":
				change = func(f *langfuse.File) { f.RowCount = langfuse.MaxRows + 1 }
			case "expanded_gzip_bytes":
				var b bytes.Buffer
				w := gzip.NewWriter(&b)
				if _, err := w.Write(bytes.Repeat([]byte(" "), langfuse.MaxFileBytes+1)); err != nil {
					t.Fatal(err)
				}
				if w.Close() != nil {
					t.Fatal("gzip fixture")
				}
				data = b.Bytes()
				zipped = true
			case "actual_rows":
				// Small synthetic rows reach the row cap before the byte cap.
				line := []byte("{\"id\":\"PRIVATE_LIMIT_SENTINEL\",\"project_id\":\"p\",\"type\":\"SPAN\",\"start_time\":\"2026-10-10 12:00:00\"}\n")
				data = bytes.Repeat(line, langfuse.MaxRows+1)
				rows = langfuse.MaxRows
				if len(data) > langfuse.MaxFileBytes {
					t.Fatal("row fixture hit wrong limit")
				}
			}
			root := limitDirectory(t, data, rows, zipped, change)
			if mode == "manifest_bytes" {
				p := filepath.Join(root, "p/manifests/PRIVATE_LIMIT_SENTINEL.json")
				if os.WriteFile(p, bytes.Repeat([]byte(" "), langfuse.MaxManifestBytes+1), 0600) != nil {
					t.Fatal("large manifest fixture")
				}
			}
			if mode == "stored_bytes" {
				p := filepath.Join(root, "p/observations_v2/PRIVATE_LIMIT_SENTINEL.jsonl")
				if os.Truncate(p, langfuse.MaxFileBytes+1) != nil {
					t.Fatal("large stored fixture")
				}
			}
			r, err := langfuse.Investigate(root, "user_id", "", spend.Options{BeforePeriod: "2026-10-10", AfterPeriod: "2026-10-17", Top: 20, Now: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)})
			requireLimit(t, err)
			if !reflect.DeepEqual(r, langfuse.Report{}) {
				t.Fatal("limit refusal exposed partial totals")
			}
			var out, errout bytes.Buffer
			code := cli.Run([]string{"investigate", "--langfuse-export", root, "--before-period", "2026-10-10", "--after-period", "2026-10-17"}, &out, &errout)
			if code != 1 || out.Len() != 0 || errout.String() != langfuse.ErrFileLimit.Error()+"\n" || strings.Contains(errout.String(), root) {
				t.Fatal("CLI lost actionable refusal or exposed partial/private output")
			}
		})
	}
}

func TestFileObservedByteAndDeclaredLimitRefusals(t *testing.T) {
	data := bytes.Repeat([]byte(" "), langfuse.MaxFileBytes+1)
	f := limitDescriptor(data, 0, false)
	f.SizeBytes = langfuse.MaxFileBytes
	r, err := langfuse.ReadFile(bytes.NewReader(data), f)
	requireLimit(t, err)
	if !reflect.DeepEqual(r, langfuse.FileReport{}) {
		t.Fatal("stream limit returned partial report")
	}
	for _, change := range []func(*langfuse.File){func(f *langfuse.File) { f.SizeBytes = langfuse.MaxFileBytes + 1 }, func(f *langfuse.File) { f.RowCount = langfuse.MaxRows + 1 }} {
		f = limitDescriptor(nil, 0, false)
		change(&f)
		_, err := langfuse.ReadFile(bytes.NewReader(nil), f)
		requireLimit(t, err)
	}
}
