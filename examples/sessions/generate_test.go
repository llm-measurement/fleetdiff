// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestSessionFixturesReproduceAndExcludeRawValues(t *testing.T) {
	out := filepath.Join(t.TempDir(), "summaries")
	if err := generate(out); err != nil {
		t.Fatal(err)
	}
	for _, side := range []string{"before", "after"} {
		data, err := os.ReadFile(filepath.Join(out, side, "app.json"))
		if err != nil {
			t.Fatal(err)
		}
		checkedIn, err := os.ReadFile(filepath.Join("data", side, "app.json"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, checkedIn) {
			t.Fatal("checked-in fixture differs from generator", side)
		}
		if bytes.Contains(data, []byte("SESSIONS_PRIVATE_")) {
			t.Fatal("raw identity in summary JSON")
		}
		doc, err := summary.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, sketch := range doc.Sketches {
			if bytes.Contains(sketch.Data, []byte("SESSIONS_PRIVATE_")) {
				t.Fatal("raw identity in decoded sketch")
			}
		}
	}
	if err := generate(out); err == nil {
		t.Fatal("generator overwrote an existing directory")
	}
}
