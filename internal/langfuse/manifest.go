// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package langfuse reads bounded local observations_v2 export snapshots.
package langfuse

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/boundedjson"
)

const (
	MaxManifestBytes = 1 << 20
	MaxFileBytes     = 16 << 20
	MaxRowBytes      = 64 << 10
	MaxFieldBytes    = 8 << 10
	MaxFields        = 64
	MaxDepth         = 8
	MaxRows          = 100_000
)

var (
	ErrInvalid         = errors.New("invalid or over-limit Langfuse export")
	ErrIncomplete      = errors.New("Langfuse export size or row count mismatch")
	ErrUnsupported     = errors.New("unsupported Langfuse export format or table")
	ErrAccountingLimit = errors.New("Langfuse accounting aggregate exceeds supported bounds")
	ErrFileLimit       = errors.New("Langfuse export exceeds supported limits: 1 MiB manifest, 16 MiB stored or expanded per file, or 100000 rows per file; use an hourly export schedule")
)

type File struct {
	Key         string
	Table       string
	FileType    string
	Format      string
	Compressed  bool
	ContentType string
	SizeBytes   int64
	RowCount    int64
}

type Manifest struct {
	ProjectID string
	Start     time.Time
	End       time.Time
	Files     []File
}

// ParseManifest reads only the small completion index. Keys are relative to the
// bucket-copy root, not relative to the manifest directory. No files are opened.
func ParseManifest(r io.Reader) (Manifest, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if len(b) > MaxManifestBytes {
		return Manifest{}, ErrFileLimit
	}
	if err != nil {
		return Manifest{}, ErrInvalid
	}
	v, err := boundedjson.Decode(b, MaxDepth, 20_000)
	if err != nil {
		return Manifest{}, ErrInvalid
	}
	m, ok := v.(map[string]any)
	if !ok || m["version"] != json.Number("1") || m["exportSource"] != "EVENTS" {
		return Manifest{}, ErrUnsupported
	}
	project, ok := m["projectId"].(string)
	if !ok || project == "" || len(project) > MaxFieldBytes {
		return Manifest{}, ErrInvalid
	}
	window, ok := m["window"].(map[string]any)
	if !ok {
		return Manifest{}, ErrInvalid
	}
	start, e1 := manifestTime(window["minTimestamp"])
	end, e2 := manifestTime(window["maxTimestamp"])
	if e1 != nil || e2 != nil || !start.Before(end) || m["maxTimestamp"] != window["maxTimestamp"] {
		return Manifest{}, ErrInvalid
	}
	files, ok := m["files"].([]any)
	if !ok || len(files) == 0 || len(files) > 1000 {
		return Manifest{}, ErrInvalid
	}
	out := Manifest{ProjectID: project, Start: start, End: end}
	seen := map[string]bool{}
	for _, raw := range files {
		entry, ok := raw.(map[string]any)
		if !ok {
			return Manifest{}, ErrInvalid
		}
		var f File
		f.Key, _ = entry["key"].(string)
		f.Table, _ = entry["table"].(string)
		f.FileType, _ = entry["fileType"].(string)
		f.Format, _ = entry["format"].(string)
		f.ContentType, _ = entry["contentType"].(string)
		compressed, ok := entry["compressed"].(bool)
		if !ok || !localKey(f.Key) || seen[f.Key] || (f.Table != "observations_v2" && f.Table != "scores") {
			return Manifest{}, ErrInvalid
		}
		seen[f.Key] = true
		f.Compressed = compressed
		f.SizeBytes, err = count(entry["sizeBytes"], MaxFileBytes)
		if err != nil {
			return Manifest{}, err
		}
		f.RowCount, err = count(entry["rowCount"], MaxRows)
		if err != nil {
			return Manifest{}, err
		}
		if err = validateFormat(f); err != nil {
			return Manifest{}, err
		}
		if !strings.Contains("/"+f.Key, "/"+f.Table+"/") {
			return Manifest{}, ErrInvalid
		}
		out.Files = append(out.Files, f)
	}
	return out, nil
}

func manifestTime(v any) (time.Time, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, ErrInvalid
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || !strings.HasSuffix(s, "Z") {
		return time.Time{}, ErrInvalid
	}
	return t, nil
}

func count(v any, max int64) (int64, error) {
	if max < 0 {
		return 0, ErrInvalid
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, ErrInvalid
	}
	i, err := strconv.ParseUint(string(n), 10, 64)
	if errors.Is(err, strconv.ErrRange) {
		return 0, ErrFileLimit
	}
	if err != nil {
		return 0, ErrInvalid
	}
	if i > math.MaxInt64 || i > uint64(max) {
		return 0, ErrFileLimit
	}
	return int64(i), nil
}

func localKey(s string) bool {
	if s == "" || len(s) > 4096 || strings.ContainsAny(s, "\\:\x00") || strings.HasPrefix(s, "/") || path.Clean(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}

func validateFormat(f File) error {
	ext, media := "", ""
	switch f.FileType {
	case "JSONL":
		ext, media = "jsonl", "application/x-ndjson; charset=utf-8"
	case "JSON":
		ext, media = "json", "application/json; charset=utf-8"
	case "CSV":
		ext, media = "csv", "text/csv; charset=utf-8"
	default:
		return ErrUnsupported
	}
	format := ext + "-raw"
	if f.Compressed {
		format, ext, media = strings.ToLower(f.FileType)+"-gzip", ext+".gz", "application/gzip"
	}
	if f.Format != format || f.ContentType != media || !strings.HasSuffix(f.Key, "."+ext) {
		return ErrUnsupported
	}
	return nil
}
