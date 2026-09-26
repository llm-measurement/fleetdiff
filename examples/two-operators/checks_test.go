// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/llm-measurement/fleetdiff/examples/internal/scenario"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

func TestMetricPrivacyScanRequiresBothCandidateSurfaces(t *testing.T) {
	t.Setenv("FLEETDIFF_SCAN_TEST_KEY", "public-metric-scan-test-key-not-for-production")
	secret, err := sketchhash.SecretFromEnv("FLEETDIFF_SCAN_TEST_KEY")
	if err != nil {
		t.Fatal(err)
	}
	s, err := scenario.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.Summary(s.Windows[1], "partner", 30_000_000_000, secret)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func() {
		t.Helper()
		data, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "summary.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := checkMetricHashes(dir, nil); err == nil {
		t.Fatal("empty scan passed")
	}
	write()
	if err := checkMetricHashes(dir, []byte("safe_count 1\n")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"top_prompts", "top_tool_errors"} {
		f, err := frequentitems.Parse(doc.Sketches[name].Data)
		if err != nil {
			t.Fatal(err)
		}
		items, err := f.FrequentItems(frequentitems.NoFalseNegatives)
		if err != nil || len(items) == 0 {
			t.Fatal("fixture must contain candidates")
		}
		for _, format := range []string{"%016x", "%d"} {
			metric := []byte(fmt.Sprintf("bad_count{key=\""+format+"\"} 1\n", items[0].Hash))
			if err := checkMetricHashes(dir, metric); err == nil {
				t.Fatal("candidate hash leak was missed")
			}
		}
		payload := doc.Sketches[name]
		delete(doc.Sketches, name)
		write()
		if err := checkMetricHashes(dir, nil); err == nil {
			t.Fatal("missing candidate surface passed")
		}
		doc.Sketches[name] = payload
		write()
	}
	for _, sentinel := range []string{scenario.Resource("R1"), "FC_PRIVATE_session_test", "FC_PRIVATE_lookup_document"} {
		if err := noPrivateContent([]byte(fmt.Sprintf("bad_count{value=%q} 1", sentinel))); err == nil {
			t.Fatal("raw MCP value leak was missed")
		}
	}
}
