// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func save(t *testing.T, path string, e summary.Envelope) {
	t.Helper()
	data, err := e.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadWindowSelectionAndDeterminism(t *testing.T) {
	a, b := windows(t)
	dir := t.TempDir()
	save(t, filepath.Join(dir, "z.json"), a[0])
	save(t, filepath.Join(dir, "a.json"), a[1])
	docs, err := ReadWindow(dir, nil)
	if err != nil || len(docs) != 2 {
		t.Fatal(err, len(docs))
	}
	if docs[0].ProducerID != "selfhosted" {
		t.Fatal("directory order is not deterministic")
	}
	save(t, filepath.Join(dir, "later.json"), b[0])
	if _, err := ReadWindow(dir, nil); err == nil {
		t.Fatal("multiple windows accepted without selection")
	}
	start := minute
	docs, err = ReadWindow(dir, &start)
	if err != nil || len(docs) != 2 {
		t.Fatal(err)
	}
	start = 3 * minute
	if _, err = ReadWindow(dir, &start); err == nil {
		t.Fatal("empty window accepted")
	}
	docs, err = ReadWindow(filepath.Join(dir, "z.json"), nil)
	if err != nil || len(docs) != 1 {
		t.Fatal(err)
	}
}

func TestReadRejectsMalformedOversizedAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SENTINEL_PATH.json")
	for _, data := range [][]byte{[]byte(`{"SENTINEL_CONTENT":true}`), bytes.Repeat([]byte("x"), summary.MaxBytes+1)} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadWindow(path, nil)
		if err == nil || strings.Contains(err.Error(), "SENTINEL") {
			t.Fatal("unsafe diagnostic", err)
		}
	}
	a, _ := windows(t)
	target := filepath.Join(t.TempDir(), "outside.json")
	save(t, target, a[0])
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWindow(link, nil); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWindow(dir, nil); err == nil {
		t.Fatal("directory symlink accepted")
	}
	fifo := filepath.Join(t.TempDir(), "pipe.json")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWindow(fifo, nil); err == nil {
		t.Fatal("FIFO accepted")
	}
	if _, err := ReadWindow(t.TempDir(), nil); err == nil {
		t.Fatal("empty directory accepted")
	}
}

func TestInputLimits(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < MaxEntries+1; i++ {
		f, err := os.CreateTemp(dir, "entry-")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadWindow(dir, nil); err == nil {
		t.Fatal("unbounded directory listing")
	}
	tooMany := t.TempDir()
	for i := 0; i < MaxFiles+1; i++ {
		f, err := os.CreateTemp(tooMany, "entry-*.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadWindow(tooMany, nil); err == nil {
		t.Fatal("unbounded snapshot count")
	}
	limited := t.TempDir()
	a, _ := windows(t)
	save(t, filepath.Join(limited, "one.json"), a[0])
	root, err := os.OpenRoot(limited)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := readRegular(root, "one.json", 1); err == nil {
		t.Fatal("aggregate byte budget ignored")
	}
}

func FuzzCanonicalSummary(f *testing.F) {
	seed := summary.Envelope{Version: 1, Sequence: 1, ProducerID: "fixture", Epoch: "one", ScopeID: "fixture", KeyID: "fixture-key", AccountingID: "fixture-accounting", WindowStart: 0, WindowDuration: minute, ObservedStart: 0, ObservedEnd: minute, EmittedAt: minute, Counters: map[string]uint64{}, Sketches: map[string]summary.Payload{}}
	rich := fixture(f, 0, fixtureSource{Producer: "fixture", Users: []uint64{1, 2}, Prompts: []uint64{3, 4}, Weights: []int64{10, 20}})
	partial := rich
	partial.ObservedStart = minute / 2
	late := seed
	late.EmittedAt = math.MaxInt64
	last := seed
	last.WindowDuration = 1
	last.WindowStart = math.MaxInt64 - 1
	last.ObservedStart = last.WindowStart
	last.ObservedEnd = math.MaxInt64
	last.EmittedAt = math.MaxInt64
	// The first three seeds must reach report construction, not merely parsing.
	reportSeeds := map[string]bool{}
	for i, e := range []summary.Envelope{seed, rich, partial, late, last} {
		data, err := e.MarshalBinary()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
		reportSeeds[string(data)] = i < 3
	}
	f.Add([]byte(`{"version":1}`))
	f.Add([]byte(`{`))
	f.Add([]byte("SENTINEL"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128<<10 {
			return
		}
		e, err := summary.Parse(data)
		if err != nil {
			return
		}
		// Shift every timestamp, preserving coverage and guarding both the new
		// window end and a possibly later emission timestamp against overflow.
		if max(e.WindowStart+e.WindowDuration, e.EmittedAt) > math.MaxInt64-e.WindowDuration {
			return
		}
		after := e
		after.WindowStart += e.WindowDuration
		after.ObservedStart += e.WindowDuration
		after.ObservedEnd += e.WindowDuration
		after.EmittedAt += e.WindowDuration
		r, err := Compare([]summary.Envelope{e}, []summary.Envelope{after}, Options{Expected: []string{e.ProducerID}, Top: 20, AllowPartial: true})
		if err != nil {
			if reportSeeds[string(data)] {
				t.Fatalf("valid seed did not reach report construction: %v", err)
			}
			return
		}
		if reportSeeds[string(data)] && (len(r.Counters) != len(e.Counters) || len(r.Distinct)+len(r.Concentration) != len(e.Sketches)) {
			t.Fatal("valid seed did not exercise its measurements")
		}
		for _, c := range r.Counters {
			if c.Delta != 0 {
				t.Fatal("shifting an unchanged window changed a counter")
			}
		}
		if _, err := json.Marshal(r); err != nil {
			t.Fatalf("cannot encode successful report: %v", err)
		}
	})
}
