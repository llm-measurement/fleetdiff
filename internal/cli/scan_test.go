// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func scanDirectory(t testing.TB, spike bool) string {
	t.Helper()
	dir := t.TempDir()
	for i := range 25 {
		start := int64(i+1) * int64(time.Minute)
		e := summary.Envelope{Version: 1, ProducerID: "PRIVATE_PRODUCER", Epoch: "PRIVATE_EPOCH", ScopeID: "PRIVATE_SCOPE", KeyID: "PRIVATE_KEY", AccountingID: "PRIVATE_ACCOUNTING", Sequence: 1, WindowStart: start, WindowDuration: int64(time.Minute), ObservedStart: start, ObservedEnd: start + int64(time.Minute), EmittedAt: start + int64(time.Minute), Counters: map[string]uint64{"requests": 100, "input_tokens": 1000, "output_tokens": 0, "missing_token_usage": 0}, Sketches: map[string]summary.Payload{}}
		if spike && i == 24 {
			e.Counters["input_tokens"] = 4000
		}
		data, err := e.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("PRIVATE_FILE_%d.json", i)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestScanCLIExitCodesPrivacyAndOutput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spike bool
		asOf  string
		code  int
	}{{"quiet", false, "1970-01-01T00:26:00Z", 0}, {"unusual", true, "1970-01-01T00:26:00Z", 3}, {"stale", false, "1970-01-01T01:00:00Z", 4}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := scanDirectory(t, tc.spike)
			for _, format := range []string{"text", "json"} {
				var out, errout bytes.Buffer
				code := Run([]string{"scan", dir, "--expected", " PRIVATE_PRODUCER ", "--as-of", tc.asOf, "--format", format}, &out, &errout)
				if code != tc.code || errout.Len() != 0 {
					t.Fatal(code, errout.String(), out.String())
				}
				if strings.Contains(out.String(), "PRIVATE_") {
					t.Fatal("private metadata leaked")
				}
				if format == "json" {
					var r compare.ScanReport
					if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Schema != "fleetdiff-scan/v1" {
						t.Fatal("bad report", err)
					}
				}
			}
		})
	}
	for _, args := range [][]string{{"scan", "--z", "NaN", "PRIVATE_PATH", "--expected", "x"}, {"scan", "--baseline", "-1", "PRIVATE_PATH", "--expected", "x"}, {"scan", "--as-of", "PRIVATE_TIME", "PRIVATE_PATH", "--expected", "x"}, {"scan", "--PRIVATE_FLAG"}} {
		var out, errout bytes.Buffer
		if code := Run(args, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "PRIVATE") {
			t.Fatal(code, &out, &errout)
		}
	}
}

func TestScanCLIInputAndOutputErrors(t *testing.T) {
	var out, errout bytes.Buffer
	dir := scanDirectory(t, false)
	if code := Run([]string{"scan", dir, "--expected", "PRIVATE_PRODUCER", "--as-of", "1970-01-01T00:26:00Z"}, failedWriter{}, &errout); code != 1 || strings.Contains(errout.String(), "SENTINEL") {
		t.Fatal("output error not reported privately", code, errout.String())
	}
	errout.Reset()
	if Run([]string{"scan", "PRIVATE_MISSING", "--expected", "app"}, &out, &errout) != 1 || strings.Contains(errout.String(), "PRIVATE") {
		t.Fatal(&out, &errout)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("PRIVATE_INVALID"), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errout.Reset()
	if Run([]string{"scan", dir, "--expected", "PRIVATE_PRODUCER"}, &out, &errout) != 1 || out.Len() != 0 || strings.Contains(errout.String(), "PRIVATE") {
		t.Fatal(&out, &errout)
	}
}

func TestReleaseBinaryScan(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test a packaged executable")
	}
	for _, tc := range []struct {
		spike          bool
		asOf           string
		code, findings int
	}{
		{false, "1970-01-01T00:26:00Z", 0, 0}, {true, "1970-01-01T00:26:00Z", 3, 1}, {false, "1970-01-01T01:00:00Z", 4, 0},
	} {
		dir := scanDirectory(t, tc.spike)
		cmd := exec.Command(binary, "scan", dir, "--expected", "PRIVATE_PRODUCER", "--as-of", tc.asOf, "--format", "json")
		data, err := cmd.CombinedOutput()
		if tc.code != 0 {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != tc.code {
				t.Fatal(err, string(data))
			}
		} else if err != nil {
			t.Fatal(err, string(data))
		}
		var r compare.ScanReport
		if err := json.Unmarshal(data, &r); err != nil || r.UnusualWindows != tc.findings || bytes.Contains(data, []byte("PRIVATE_")) {
			t.Fatal("packaged scan mismatch", err)
		}
	}
}

func TestScanTextPreservesIntegerUncertainty(t *testing.T) {
	bounds := &compare.Interval{Lower: math.MaxInt64 - 1, Upper: math.MaxInt64}
	r := compare.ScanReport{Status: "evaluated", UnusualWindows: 1, Windows: []compare.ScannedWindow{{Status: "evaluated", Findings: []compare.ScanFinding{
		{ScanSignal: compare.ScanSignal{Signal: "top_share", Measurement: "top_users", Current: &compare.ScanRange{Lower: 1, Upper: 1}, WeightBounds: bounds, TotalWeight: math.MaxInt64}, Item: "user-1"},
		{ScanSignal: compare.ScanSignal{Signal: "tool_error_surge", Measurement: "top_tool_errors", Current: &compare.ScanRange{Lower: float64(math.MaxInt64), Upper: float64(math.MaxInt64)}, WeightBounds: bounds, TotalWeight: math.MaxInt64}, Item: "error-1"},
	}}}}
	var out bytes.Buffer
	renderScan(&out, r)
	for _, want := range []string{"[99.99%, 100.00%]", "[9223372036854775806, 9223372036854775807] events"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("rounded away integer uncertainty", out.String())
		}
	}
}
