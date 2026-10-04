// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestCacheFixtures(t *testing.T) {
	out := filepath.Join(t.TempDir(), "cache")
	if err := generate(out); err != nil {
		t.Fatal(err)
	}
	for i, side := range []string{"before", "after"} {
		want, err := os.ReadFile(filepath.Join("data", side, "app.json"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(out, side, "app.json"))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("synthetic cache fixture drift", err)
		}
		doc, err := summary.Parse(got)
		if err != nil || len(doc.Sketches) != 0 {
			t.Fatal("cache fixtures must be canonical and counter-only", err)
		}
		start := time.Date(2026, 10, 4, 0, i, 0, 0, time.UTC).UnixNano()
		if doc.WindowStart != start || doc.ObservedStart != start || doc.WindowDuration != int64(time.Minute) || doc.ObservedEnd != start+int64(time.Minute) || doc.EmittedAt != doc.ObservedEnd {
			t.Fatal("cache fixture dates must be adjacent complete windows on 2026-10-04")
		}
	}
	if err := generate(out); err == nil {
		t.Fatal("generator overwrote existing fixtures")
	}
}
