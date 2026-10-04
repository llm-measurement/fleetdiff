// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestDemoWindows(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "windows")
		if err := generate(dir, quiet); err != nil {
			t.Fatal(err)
		}
		docs, err := compare.ReadSeries(dir)
		if err != nil {
			t.Fatal(err)
		}
		o := compare.DefaultScanOptions()
		o.Expected = []string{"app"}
		o.Recent = 2
		o.Now = time.Date(2026, 10, 4, 0, 30, 0, 0, time.UTC).UnixNano()
		r, err := compare.Scan(docs, o)
		if err != nil {
			t.Fatal(err)
		}
		if quiet && r.UnusualWindows != 0 || !quiet && r.UnusualWindows != 2 {
			t.Fatal(r)
		}
		if !quiet {
			var intensity, coverage, session, tool bool
			for _, w := range r.Windows {
				for _, f := range w.Findings {
					intensity = intensity || f.Signal == "tokens_per_attempt" && f.Current.Lower == 310 && f.Median == 100
					coverage = coverage || f.Signal == "missing_usage_share" && f.Current.Lower == .4
					session = session || f.Signal == "newly_prominent" && f.Measurement == "top_sessions" && f.Current.Lower == .62
					tool = tool || f.Signal == "tool_error_surge" && f.Current.Lower == 40
				}
			}
			if !intensity || !coverage || !session || !tool {
				t.Fatal("planted signals missing", r)
			}
		}
	}
}
