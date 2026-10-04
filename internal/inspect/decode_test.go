// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
)

const minimalCapture = `{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"0123456789abcdef0123456789abcdef","spanId":"0123456789abcdef","parentSpanId":"0000000000000000","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"chat"}},{"key":"gen_ai.usage.input_tokens","value":{"intValue":"10"}},{"key":"gen_ai.usage.output_tokens","value":{"intValue":"2"}}]}]}]}]}`

func TestCaptureEncodings(t *testing.T) {
	req, err := decodeJSON([]byte(minimalCapture))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(wire)))
	frame = append(frame, wire...)
	for _, tc := range []struct {
		name, format string
		data         []byte
		records      int
	}{
		{"json", "json", []byte(minimalCapture), 1},
		{"auto", "auto", []byte(" \n" + minimalCapture), 1},
		{"jsonl", "json", []byte(minimalCapture + "\n" + minimalCapture + "\n"), 2},
		{"proto", "proto", wire, 1},
		{"file-proto", "file-proto", append(append([]byte{}, frame...), frame...), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := 0
			err := visitRecords(tc.data, tc.format, func(r *traceRequest) error {
				n++
				span := r.ResourceSpans[0].ScopeSpans[0].Spans[0]
				if len(span.TraceId) != 16 || len(span.SpanId) != 8 || !bytes.Equal(span.ParentSpanId, make([]byte, 8)) {
					t.Fatal("OTLP hex IDs changed")
				}
				return nil
			})
			if err != nil || n != tc.records {
				t.Fatalf("records=%d err=%v", n, err)
			}
		})
	}
}

func TestMalformedCaptureNeverEchoesContent(t *testing.T) {
	for _, input := range []string{
		`{"SENTINEL":"private"}`, `{"resourceSpans":42}`, `{"resourceSpans":[],"resourceSpans":[]}`,
		`{"resourceSpans":[{"scopeSpans":[{"spans":[{"traceId":"SENTINEL"}]}]}]}`,
		minimalCapture + `{"SENTINEL":`, `{"resourceSpans":[` + strings.Repeat("[", 100) + strings.Repeat("]", 100) + `]}`,
		`{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":[{"key":"private","value":{"stringValue":"x","intValue":"1"}}]}]}]}]}`,
	} {
		err := visitRecords([]byte(input), "json", func(*traceRequest) error { return nil })
		if err == nil || strings.Contains(err.Error(), "SENTINEL") || strings.Contains(err.Error(), "private") {
			t.Fatalf("unsafe acceptance/diagnostic: %v", err)
		}
	}
	for _, wire := range [][]byte{{0, 0, 0}, {255, 255, 255, 255}, {0, 0, 0, 8, 1}, {0x0a, 0xff}} {
		if err := visitRecords(wire, "file-proto", func(*traceRequest) error { return nil }); err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
}

func FuzzTraceRecord(f *testing.F) {
	// Stable hashing makes coverage and minimized inputs repeatable.
	f.Setenv("FLEETDIFF_FUZZ_KEY", "synthetic-fuzz-only-key-4917608235")
	f.Add([]byte(minimalCapture), uint8(0))
	f.Add([]byte{0, 0, 0, 1, 255}, uint8(2))
	f.Fuzz(func(t *testing.T, data []byte, mode uint8) {
		if len(data) > 65536 {
			t.Skip()
		}
		_, _ = Inspect("-", bytes.NewReader(data), Options{InputFormat: []string{"json", "proto", "file-proto"}[mode%3], Top: 10, SecretEnv: "FLEETDIFF_FUZZ_KEY"})
	})
}
