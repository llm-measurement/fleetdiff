// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestScanTextGroupsFindingsAndPreservesJSON(t *testing.T) {
	start := time.Date(2026, 10, 4, 0, 28, 0, 0, time.UTC).UnixNano()
	minute := int64(time.Minute)
	r := compare.ScanReport{
		Status: "evaluated", UnusualWindows: 2, BaselineWindows: 24, RequestedBaseline: 24,
		BaselineStart: start - 24*minute, BaselineEnd: start,
		Notes: []string{"Full baseline explanation.", "Full attribution explanation.", "Full deduplication explanation."},
		Windows: []compare.ScannedWindow{
			{Start: start, End: start + minute, Status: "evaluated", Findings: []compare.ScanFinding{
				{ScanSignal: compare.ScanSignal{Signal: "tokens_per_attempt", Current: &compare.ScanRange{Lower: 310, Upper: 310}, Median: 100}},
				{ScanSignal: compare.ScanSignal{Signal: "newly_prominent", Measurement: "top_sessions", Current: &compare.ScanRange{Lower: .62, Upper: .62}, WeightBounds: &compare.Interval{Lower: 62, Upper: 62}, TotalWeight: 100}, Item: "session-1"},
			}},
			{Start: start + minute, End: start + 2*minute, Status: "limited", Findings: []compare.ScanFinding{
				{ScanSignal: compare.ScanSignal{Signal: "newly_prominent", Measurement: "top_sessions_requests", Current: &compare.ScanRange{Lower: .62, Upper: .62}, WeightBounds: &compare.Interval{Lower: 62, Upper: 62}, TotalWeight: 100}, Item: "session-1"},
				{ScanSignal: compare.ScanSignal{Signal: "missing_usage_share", Current: &compare.ScanRange{Lower: .4, Upper: .4}}},
			}},
		},
	}
	before, _ := json.Marshal(r)
	var out bytes.Buffer
	renderScan(&out, r)
	after, _ := json.Marshal(r)
	if !bytes.Equal(before, after) {
		t.Fatal("text rendering changed JSON notes, bounds, or baseline metadata")
	}
	first, second := "2026-10-04T00:28:00Z", "2026-10-04T00:29:00Z"
	for _, stamp := range []string{first, second} {
		if strings.Count(out.String(), stamp) != 1 || !strings.Contains(out.String(), "\n"+stamp+"\n") {
			t.Fatal("window timestamp must appear once as a group heading", out.String())
		}
	}
	for _, want := range []string{
		"session-1 now holds 62% of tokens (newly prominent in this scan).",
		"session-1 now holds 62% of model attempts (newly prominent in this scan).",
		"Missing usage: 40.00% of model attempts; typical 0.00% (coverage change, not lower usage).",
		"Coverage is incomplete for some signals.",
		"Baseline: 24 of 24 requested windows, 2026-10-04T00:04:00Z to 2026-10-04T00:27:00Z (window starts).",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	boundary := strings.Index(out.String(), "\n"+second+"\n")
	if strings.Index(out.String(), "of tokens (newly prominent") > boundary || strings.Index(out.String(), "of model attempts (newly prominent") < boundary {
		t.Fatal("findings escaped their window groups", out.String())
	}
	if strings.Count(out.String(), "\nNotes: ") != 1 || strings.Contains(out.String(), "Full baseline explanation") || !strings.Contains(out.String(), "Newly prominent does not mean a new identity.") {
		t.Fatal("text needs one concise note without an identity claim", out.String())
	}
}

func TestScanTextKeepsBoundsAndUnknown(t *testing.T) {
	r := compare.ScanReport{Status: "limited", Windows: []compare.ScannedWindow{{Status: "limited", Findings: []compare.ScanFinding{
		{ScanSignal: compare.ScanSignal{Signal: "newly_prominent", Measurement: "top_sessions", Current: &compare.ScanRange{Lower: .2, Upper: .4}, WeightBounds: &compare.Interval{Lower: 2, Upper: 4}, TotalWeight: 10}, Item: "session-1"},
		{ScanSignal: compare.ScanSignal{Signal: "tokens_per_attempt", Status: "coverage_limited"}},
	}}}}
	var out bytes.Buffer
	renderScan(&out, r)
	for _, want := range []string{"session-1 now holds [20.00%, 40.00%] of tokens", "reported tokens per attempt: unavailable.", "this is not a quiet result"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("bounds or unknown state were lost", out.String())
		}
	}
	if strings.Contains(out.String(), "reported tokens per attempt: 0") {
		t.Fatal("unknown became zero", out.String())
	}
}

func TestScanTextReadinessReason(t *testing.T) {
	r := compare.ScanReport{Status: "not_ready", Notes: []string{"Full baseline explanation.", "Summary history is stale; check collection and archive freshness."}}
	var out bytes.Buffer
	renderScan(&out, r)
	if !strings.Contains(out.String(), "Summary history is stale") || strings.Count(out.String(), "\nNotes: ") != 1 {
		t.Fatal("short notes hid the readiness diagnostic", out.String())
	}
}

func TestScanCLIBaselineBoundaryAndNoContamination(t *testing.T) {
	dir := t.TempDir()
	start := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC).UnixNano()
	minute := int64(time.Minute)
	for i := range 31 {
		input := uint64(10000)
		if i < 4 || i == 30 {
			input = 900000
		} else if i >= 28 {
			input = uint64(i-27) * 31000
		}
		window := start + int64(i)*minute
		doc := summary.Envelope{
			Version: 1, Sequence: 1, ProducerID: "app", Epoch: "boundary-test", ScopeID: "scan-test", KeyID: "test-key", AccountingID: "test-v1",
			WindowStart: window, WindowDuration: minute, ObservedStart: window, ObservedEnd: window + minute, EmittedAt: window + minute,
			Counters: map[string]uint64{"requests": 100, "input_tokens": input, "output_tokens": 0, "missing_token_usage": 0}, Sketches: map[string]summary.Payload{},
		}
		data, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("window-%02d.json", i)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, format := range []string{"json", "text"} {
		var out, diagnostic bytes.Buffer
		args := []string{"scan", dir, "--expected", "app", "--recent", "2", "--as-of", "2026-10-04T00:30:00Z", "--format", format}
		if code := Run(args, &out, &diagnostic); code != 3 || diagnostic.Len() != 0 {
			t.Fatal("boundary scan failed", code, out.String(), diagnostic.String())
		}
		if format == "text" {
			want := "Baseline: 24 of 24 requested windows, 2026-10-04T00:04:00Z to 2026-10-04T00:27:00Z (window starts)."
			if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "\n2026-10-04T00:30:00Z\n") {
				t.Fatal("exclusive end was printed as a baseline window or open window was judged", out.String())
			}
			continue
		}
		var r compare.ScanReport
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.BaselineStart != start+4*minute || r.BaselineEnd != start+28*minute || r.BaselineWindows != 24 || len(r.Windows) != 2 || len(r.Notes) < 3 {
			t.Fatal("JSON metadata or full notes changed", r)
		}
		for i, w := range r.Windows {
			if w.Start != start+int64(28+i)*minute {
				t.Fatal("wrong judged window", w.Start)
			}
			found := false
			for _, s := range w.Signals {
				if s.Signal != "tokens_per_attempt" {
					continue
				}
				found = true
				if s.Median != 100 || s.BaselineUpper != 100 || s.Samples != 24 || s.Current == nil || s.Current.Lower != float64(i+1)*310 {
					t.Fatal("excluded older, recent, or open windows contaminated baseline", s)
				}
			}
			if !found {
				t.Fatal("missing intensity signal")
			}
		}
	}
}
