// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/cli"
)

func TestSyntheticCapture(t *testing.T) {
	for _, variant := range []string{"stock", "provenance"} {
		data, err := capture(variant)
		if err != nil {
			t.Fatal(err)
		}
		var request struct {
			ResourceSpans []struct {
				ScopeSpans []struct {
					Spans []struct {
						Attributes []struct {
							Key string `json:"key"`
						} `json:"attributes"`
					} `json:"spans"`
				} `json:"scopeSpans"`
			} `json:"resourceSpans"`
		}
		if err := json.Unmarshal(data, &request); err != nil {
			t.Fatal(err)
		}
		spans := request.ResourceSpans[0].ScopeSpans[0].Spans
		if len(spans) != 6 {
			t.Fatalf("got %d spans", len(spans))
		}
		declared := 0
		for _, span := range spans {
			for _, attr := range span.Attributes {
				if strings.HasPrefix(attr.Key, "gen_ai_sketch.usage.") {
					declared++
				}
			}
		}
		if variant == "stock" && declared != 0 || variant == "provenance" && declared != 6 {
			t.Fatalf("%s has %d provenance declarations", variant, declared)
		}
	}
}

func TestGeneratedCaptureThroughCLI(t *testing.T) {
	for _, variant := range []string{"stock", "provenance"} {
		t.Run(variant, func(t *testing.T) {
			data, err := capture(variant)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "private-capture.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			var out, errout bytes.Buffer
			if code := cli.Run([]string{"inspect", "--format", "json", path}, &out, &errout); code != 0 {
				t.Fatalf("inspect returned %d: %s", code, &errout)
			}
			if strings.Contains(out.String()+errout.String(), "SYNTHETIC_") || strings.Contains(out.String()+errout.String(), path) {
				t.Fatal("inspection echoed synthetic raw values or the input path")
			}
			var report struct {
				Schema          string            `json:"schema"`
				Metrics         map[string]uint64 `json:"metrics"`
				UsageProvenance map[string]uint64 `json:"usage_provenance"`
			}
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			missing, unknown := uint64(1), uint64(4)
			if variant == "provenance" {
				missing, unknown = 2, 1
			}
			if report.Schema != "fleetdiff-inspect/v1" || report.Metrics["gen_ai_sketch_requests_total"] != 4 ||
				report.Metrics["gen_ai_sketch_total_tokens_total"] != 170 ||
				report.Metrics["gen_ai_sketch_missing_token_usage_total"] != missing ||
				report.UsageProvenance["input/unknown"] != unknown {
				t.Fatalf("unexpected %s accounting: %+v", variant, report)
			}
		})
	}
}
