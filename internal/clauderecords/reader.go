// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package clauderecords compares observed Claude Code api_request log records.
package clauderecords

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/attribution"
	"github.com/llm-measurement/fleetdiff/internal/boundedjson"
	"github.com/llm-measurement/fleetdiff/internal/localfile"
)

const (
	MaxFileBytes     = 128 << 20
	MaxEnvelopeBytes = 8 << 20
	MaxRecords       = 1_000_000
	MaxIdentities    = 250_000
	MaxProfiles      = 128
	MaxFiles         = 256
)

type value struct {
	kind, text string
	invalid    bool
}
type record struct {
	event           string
	time            time.Time
	attrs, resource map[string]value
	fingerprint     [32]byte
}

var allowed = map[string]bool{
	"input_tokens": true, "output_tokens": true, "cache_read_tokens": true, "cache_creation_tokens": true,
	"cost_usd": true, "cost_usd_micros": true, "model": true, "status_code": true, "attempt": true,
	"user.account_uuid": true, "user.email": true, "user.id": true, "session.id": true, "prompt.id": true,
	"identity.source": true, "service.version": true, "fleetdiff.capture.profile": true,
}

func object(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("expected an OTLP JSON object")
	}
	return m, nil
}

func children(m map[string]any, name string, limit int) ([]any, error) {
	v, present := m[name]
	if !present {
		return nil, nil
	}
	a, ok := v.([]any)
	if !ok || len(a) > limit {
		return nil, errors.New("invalid or over-limit OTLP repeated field")
	}
	return a, nil
}

func attributes(m map[string]any) (map[string]value, error) {
	a, err := children(m, "attributes", 256)
	if err != nil {
		return nil, err
	}
	out, seen := map[string]value{}, map[string]bool{}
	for _, item := range a {
		kv, err := object(item)
		if err != nil {
			return nil, err
		}
		key, ok := kv["key"].(string)
		if !ok || key == "" || len(key) > 256 || seen[key] {
			return nil, errors.New("invalid or duplicate OTLP attribute")
		}
		seen[key] = true
		if !allowed[key] {
			continue
		}
		v, ok := kv["value"].(map[string]any)
		if !ok || len(v) != 1 {
			out[key] = value{invalid: true}
			continue
		}
		for kind, raw := range v {
			item := value{kind: kind}
			switch x := raw.(type) {
			case string:
				item.text = x
			case json.Number:
				item.text = x.String()
				if kind == "stringValue" {
					item.invalid = true
				}
			default:
				item.invalid = true
			}
			if kind != "stringValue" && kind != "intValue" && kind != "doubleValue" || len(item.text) > 4096 {
				item.invalid = true
				item.text = ""
			}
			out[key] = item
		}
	}
	return out, nil
}

// Keep a bounded snapshot: rotation cannot change bytes between period selection
// and accounting. Limits apply to the entire invocation, not to each file.
func load(paths []string) ([][]byte, error) {
	if len(paths) == 0 || len(paths) > MaxFiles {
		return nil, errors.New("select 1-256 collector JSONL files")
	}
	data := make([][]byte, 0, len(paths))
	remaining := MaxFileBytes
	for i, path := range paths {
		root, err := os.OpenRoot(filepath.Dir(path))
		if err != nil {
			return nil, fmt.Errorf("capture file %d: cannot open capture directory", i+1)
		}
		b, err := localfile.Read(root, filepath.Base(path), remaining)
		root.Close()
		if err != nil {
			return nil, fmt.Errorf("capture file %d: %w", i+1, err)
		}
		data = append(data, b)
		remaining -= len(b)
	}
	return data, nil
}

func read(data [][]byte, key []byte, visit func(record) error) (uint64, uint64, error) {
	var total, ignored uint64
	for i, b := range data {
		n, skip, err := readFile(b, key, MaxRecords-total, visit)
		total += n
		ignored += skip
		if err != nil {
			return total, ignored, fmt.Errorf("capture file %d: %w", i+1, err)
		}
	}
	if total == 0 {
		return 0, 0, errors.New("capture contains no log records; use the collector JSON logs recipe")
	}
	return total, ignored, nil
}

func readFile(data, key []byte, remaining uint64, visit func(record) error) (uint64, uint64, error) {
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 64<<10), MaxEnvelopeBytes+1)
	var total, ignored uint64
	line := 0
	for scan.Scan() {
		line++
		b := bytes.TrimSpace(scan.Bytes())
		if len(b) == 0 {
			continue
		}
		if len(b) > MaxEnvelopeBytes {
			return total, ignored, errors.New("OTLP envelope exceeds 8 MiB")
		}
		v, err := boundedjson.Decode(b, 32, 250000)
		if err != nil {
			return total, ignored, fmt.Errorf("capture envelope %d: invalid or over-limit JSON", line)
		}
		n, skip, err := envelope(v, key, remaining-total, visit)
		total += n
		ignored += skip
		if err != nil {
			return total, ignored, fmt.Errorf("capture envelope %d: %s", line, err)
		}
	}
	if err := scan.Err(); err != nil {
		return total, ignored, errors.New("cannot read capture or envelope exceeds 8 MiB")
	}
	return total, ignored, nil
}

func envelope(v any, key []byte, remaining uint64, visit func(record) error) (uint64, uint64, error) {
	root, err := object(v)
	if err != nil {
		return 0, 0, err
	}
	if _, ok := root["resourceLogs"]; !ok {
		return 0, 0, errors.New("expected resourceLogs; use a collector JSON logs export")
	}
	resources, err := children(root, "resourceLogs", 1024)
	if err != nil {
		return 0, 0, err
	}
	var total, ignored uint64
	for _, rv := range resources {
		r, err := object(rv)
		if err != nil {
			return total, ignored, err
		}
		rm := map[string]any{}
		if x, ok := r["resource"]; ok {
			rm, err = object(x)
			if err != nil {
				return total, ignored, err
			}
		}
		ra, err := attributes(rm)
		if err != nil {
			return total, ignored, err
		}
		scopes, err := children(r, "scopeLogs", 1024)
		if err != nil {
			return total, ignored, err
		}
		for _, sv := range scopes {
			s, err := object(sv)
			if err != nil {
				return total, ignored, err
			}
			logs, err := children(s, "logRecords", 100000)
			if err != nil {
				return total, ignored, err
			}
			for _, lv := range logs {
				if total >= remaining {
					return total, ignored, errors.New("capture exceeds 1000000 log records")
				}
				total++
				l, err := object(lv)
				if err != nil {
					return total, ignored, err
				}
				body, _ := l["body"].(map[string]any)
				event, _ := body["stringValue"].(string)
				if len(body) != 1 || event != "claude_code.api_request" && event != "claude_code.api_error" && event != "claude_code.api_retries_exhausted" {
					ignored++
					continue
				}
				a, err := attributes(l)
				if err != nil {
					return total, ignored, err
				}
				var stamp string
				switch x := l["timeUnixNano"].(type) {
				case string:
					stamp = x
				case json.Number:
					stamp = x.String()
				}
				n, err := strconv.ParseInt(stamp, 10, 64)
				if err != nil || n <= 0 || len(stamp) > 19 {
					return total, ignored, errors.New("recognized log needs a positive source timeUnixNano within signed 64-bit range")
				}
				original, _ := json.Marshal([]any{rm, s["scope"], r["schemaUrl"], s["schemaUrl"], l})
				row := record{event: event, time: time.Unix(0, n).UTC(), attrs: a, resource: ra, fingerprint: attribution.Digest(key, "claude-records/exact-copy/v1", original)}
				if err := visit(row); err != nil {
					return total, ignored, err
				}
			}
		}
	}
	return total, ignored, nil
}

// resolve compares locality before choosing; a conflicting identity is unknown.
func (r record) resolve(name string) (value, bool) {
	a, ap := r.attrs[name]
	b, bp := r.resource[name]
	if ap && bp && a != b {
		return value{invalid: true}, true
	}
	if ap {
		return a, true
	}
	return b, bp
}

func (r record) text(name string) (string, bool) {
	v, ok := r.resolve(name)
	if !ok || v.invalid || v.kind != "stringValue" || strings.TrimSpace(v.text) == "" {
		return "", false
	}
	return v.text, true
}
