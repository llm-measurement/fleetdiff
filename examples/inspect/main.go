// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// This generator writes synthetic OTLP JSON without an SDK or provider account.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

//go:embed synthetic-spans.json
var fixture []byte

type exampleSpan struct {
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes"`
}

func capture(variant string) ([]byte, error) {
	var source []exampleSpan
	if err := json.Unmarshal(fixture, &source); err != nil {
		return nil, err
	}
	spans := make([]any, 0, len(source))
	for i, s := range source {
		keys := make([]string, 0, len(s.Attributes))
		for key := range s.Attributes {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		attributes := make([]any, 0, len(keys))
		for _, key := range keys {
			if variant == "stock" && strings.HasPrefix(key, "gen_ai_sketch.usage.") {
				continue
			}
			value := map[string]any{}
			switch v := s.Attributes[key].(type) {
			case string:
				value["stringValue"] = v
			case float64:
				value["intValue"] = fmt.Sprintf("%.0f", v)
			default:
				return nil, fmt.Errorf("unsupported synthetic attribute")
			}
			attributes = append(attributes, map[string]any{"key": key, "value": value})
		}
		spans = append(spans, map[string]any{
			"traceId": "00000000000000000000000000000001", "spanId": fmt.Sprintf("%016x", i+1),
			"name": s.Name, "kind": 3, "startTimeUnixNano": "1700000000000000000",
			"endTimeUnixNano": "1700000001000000000", "attributes": attributes,
		})
	}
	return json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
		"resource": map[string]any{"attributes": []any{map[string]any{
			"key": "service.name", "value": map[string]any{"stringValue": "synthetic-inspect-app"},
		}}},
		"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "fleetdiff.capture-example"}, "spans": spans}},
	}}})
}

func main() {
	variant := flag.String("variant", "stock", "stock or provenance")
	flag.Parse()
	if flag.NArg() != 0 || (*variant != "stock" && *variant != "provenance") {
		fmt.Fprintln(os.Stderr, "choose --variant stock or --variant provenance")
		os.Exit(2)
	}
	data, err := capture(*variant)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot generate synthetic capture")
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(append(data, '\n')); err != nil {
		os.Exit(1)
	}
}
