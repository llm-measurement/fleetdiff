// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package inspect describes local OTLP captures without exporting their raw data.
package inspect

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	tracepb "go.opentelemetry.io/proto/slim/otlp/collector/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type traceRequest = tracepb.ExportTraceServiceRequest

const MaxRecordBytes = 8 << 20
const maxJSONDepth = 32
const maxJSONNodes = 250000

var errEncoding = errors.New("invalid OTLP capture encoding; use a documented capture recipe and input format")

func visitRecords(data []byte, format string, visit func(*traceRequest) error) error {
	if len(data) == 0 {
		return errors.New("capture is empty")
	}
	switch format {
	case "auto", "json":
		dec := json.NewDecoder(bytes.NewReader(data))
		n := 0
		for {
			var raw json.RawMessage
			if err := dec.Decode(&raw); errors.Is(err, io.EOF) {
				if n == 0 {
					return errors.New("capture is empty")
				}
				return nil
			} else if err != nil {
				return errEncoding
			}
			r, err := decodeJSON(raw)
			if err != nil {
				return err
			}
			if err := visit(r); err != nil {
				return err
			}
			n++
		}
	case "proto":
		return visitProto(data, visit)
	case "file-proto":
		for len(data) > 0 {
			if len(data) < 4 {
				return errEncoding
			}
			n := uint64(binary.BigEndian.Uint32(data[:4]))
			data = data[4:]
			if n > MaxRecordBytes || n > uint64(len(data)) {
				return errEncoding
			}
			if err := visitProto(data[:int(n)], visit); err != nil {
				return err
			}
			data = data[int(n):]
		}
		return nil
	default:
		return errors.New("unsupported input format")
	}
}

func visitProto(data []byte, visit func(*traceRequest) error) error {
	if len(data) > MaxRecordBytes {
		return errors.New("OTLP record exceeds 8 MiB")
	}
	if err := preflightProto(data); err != nil {
		return err
	}
	var r traceRequest
	if err := (proto.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: maxJSONDepth}).Unmarshal(data, &r); err != nil {
		return errEncoding
	}
	return visit(&r)
}

// OTLP uses hexadecimal trace/span IDs, unlike protobuf JSON's base64 bytes.
// Preserve typed values through the standard JSON and protobuf parsers; reject
// duplicate members before converting just those protocol-defined ID fields.
func decodeJSON(data []byte) (*traceRequest, error) {
	if len(data) > MaxRecordBytes {
		return nil, errors.New("OTLP record exceeds 8 MiB")
	}
	if !utf8.Valid(data) {
		return nil, errEncoding
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	left := maxJSONNodes
	value, err := readJSONValue(dec, 0, &left)
	if err != nil {
		return nil, errEncoding
	}
	if _, err = dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errEncoding
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil, errEncoding
	}
	resources, ok := root["resourceSpans"].([]any)
	if !ok {
		return nil, errEncoding
	}
	if len(resources) > 1024 {
		return nil, errors.New("JSON capture exceeds 1024 resource groups")
	}
	spanCount := 0
	for _, r := range resources {
		if len(jsonChildren(r, "scopeSpans")) > 1024 {
			return nil, errors.New("JSON capture exceeds 1024 scope groups")
		}
		for _, scope := range jsonChildren(r, "scopeSpans") {
			spanCount += len(jsonChildren(scope, "spans"))
			if spanCount > MaxSpans {
				return nil, errors.New("capture exceeds 100000 spans")
			}
			for _, span := range jsonChildren(scope, "spans") {
				for _, field := range []string{"attributes", "events", "links"} {
					if len(jsonChildren(span, field)) > 256 {
						return nil, errors.New("JSON span repeated field exceeds 256 entries")
					}
				}
				if err = fixIDs(span, true); err != nil {
					return nil, err
				}
				for _, link := range jsonChildren(span, "links") {
					if err = fixIDs(link, false); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, errEncoding
	}
	var result traceRequest
	if err = (protojson.UnmarshalOptions{DiscardUnknown: true, RecursionLimit: maxJSONDepth}).Unmarshal(encoded, &result); err != nil {
		return nil, errEncoding
	}
	return &result, nil
}

func jsonChildren(v any, field string) []any {
	m, _ := v.(map[string]any)
	a, _ := m[field].([]any)
	return a
}

func fixIDs(v any, parent bool) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errEncoding
	}
	fields := []struct {
		name string
		size int
	}{{"traceId", 16}, {"spanId", 8}}
	if parent {
		fields = append(fields, struct {
			name string
			size int
		}{"parentSpanId", 8})
	}
	for _, f := range fields {
		x, exists := m[f.name]
		if !exists || x == nil {
			continue
		}
		s, ok := x.(string)
		if !ok {
			return errEncoding
		}
		if s == "" {
			continue
		}
		if len(s) != f.size*2 {
			return errEncoding
		}
		b, err := hex.DecodeString(s)
		if err != nil {
			return errEncoding
		}
		m[f.name] = base64.StdEncoding.EncodeToString(b)
	}
	return nil
}

func readJSONValue(d *json.Decoder, depth int, left *int) (any, error) {
	*left--
	if depth > maxJSONDepth || *left < 0 {
		return nil, errEncoding
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := t.(string)
			if !ok {
				return nil, errEncoding
			}
			if _, duplicate := m[key]; duplicate {
				return nil, errEncoding
			}
			v, err := readJSONValue(d, depth+1, left)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		if t, err := d.Token(); err != nil || t != json.Delim('}') {
			return nil, errEncoding
		}
		return m, nil
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := readJSONValue(d, depth+1, left)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		if t, err := d.Token(); err != nil || t != json.Delim(']') {
			return nil, errEncoding
		}
		return a, nil
	default:
		if _, bad := t.(json.Delim); bad {
			return nil, errEncoding
		}
		return t, nil
	}
}
