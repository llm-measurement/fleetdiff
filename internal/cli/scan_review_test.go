// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestScanReviewCLITinyThresholdsStayQuiet(t *testing.T) {
	dir := scanDirectory(t, false)
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			args := []string{"scan", dir, "--expected", "PRIVATE_PRODUCER", "--as-of", "1970-01-01T00:26:00Z",
				"--z", "1e-20", "--relative-change", "1e-20", "--format", format}
			if code := Run(args, &out, &diagnostic); code != 0 || diagnostic.Len() != 0 {
				t.Fatal("flat history became an alert with valid tiny thresholds", code, out.String(), diagnostic.String())
			}
			if strings.Contains(out.String(), "PRIVATE_") {
				t.Fatal("quiet scan leaked metadata")
			}
			if format == "text" {
				if !strings.HasPrefix(out.String(), "No unusual windows among 1 checked, for the available signals.\n") {
					t.Fatal("missing quiet verdict", out.String())
				}
				return
			}
			var r compare.ScanReport
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if r.Status != "evaluated" || r.UnusualWindows != 0 || len(r.Windows) != 1 || len(r.Windows[0].Findings) != 0 || r.Windows[0].PendingPersistence != 0 {
				t.Fatal("flat scan must be quiet, not merely awaiting persistence", r)
			}
			checked := 0
			for _, s := range r.Windows[0].Signals {
				if s.Signal != "attempt_volume" && s.Signal != "tokens_per_attempt" {
					continue
				}
				if s.Status != "evaluated" || s.Current == nil || s.Current.Lower != s.Median || s.Current.Upper != s.Median || s.Score != 0 || s.RequiredChange <= 0 || s.Threshold != s.Median {
					t.Fatal("lost nonzero minimum_change or changed flat observations", s)
				}
				checked++
			}
			if checked != 2 {
				t.Fatal("missing scalar checks", checked)
			}
		})
	}
}
