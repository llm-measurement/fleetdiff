// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/change"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestTokenCUSUMOffGolden(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, errout bytes.Buffer
			args := []string{"scan", scanDirectory(t, true), "--expected", "PRIVATE_PRODUCER",
				"--as-of", "1970-01-01T00:26:00Z", "--format", format}
			if code := Run(args, &out, &errout); code != 3 || errout.Len() != 0 {
				t.Fatalf("scan = %d: %s", code, errout.String())
			}
			assertTokenCUSUMGolden(t, "off."+format+".golden", out.Bytes())
			out.Reset()
			if code := Run(append(args, "--token-cusum=false"), &out, &errout); code != 3 || errout.Len() != 0 {
				t.Fatalf("explicit off scan = %d: %s", code, errout.String())
			}
			assertTokenCUSUMGolden(t, "off."+format+".golden", out.Bytes())
		})
	}
}

func TestTokenCUSUMOnGolden(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, errout bytes.Buffer
			args := []string{"scan", tokenCUSUMDirectory(t, true), "--expected", "PRIVATE_PRODUCER",
				"--as-of", "1970-01-01T00:26:00Z", "--format", format, "--token-cusum"}
			if code := Run(args, &out, &errout); code != 3 || errout.Len() != 0 {
				t.Fatalf("scan = %d: %s", code, errout.String())
			}
			assertTokenCUSUMGolden(t, "on."+format+".golden", out.Bytes())
			if strings.Contains(out.String(), "PRIVATE_") {
				t.Fatal("private input metadata leaked")
			}
		})
	}
}

func TestTokenCUSUMCLIUncalibratedIsNotQuiet(t *testing.T) {
	for _, test := range []struct {
		name, baseline string
		want           change.CUSUMStatus
	}{
		{"complete", "24", change.CUSUMUncalibrated},
		{"short reference", "30", change.CUSUMInsufficient},
		{"duration", "6m", change.CUSUMUncalibrated},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			args := []string{"scan", "--expected", "PRIVATE_PRODUCER", "--as-of", "1970-01-01T00:26:00Z",
				"--format", "json", "--token-cusum", "--baseline", test.baseline, tokenCUSUMDirectory(t, false)}
			if code := Run(args, &out, &errout); code != 4 || errout.Len() != 0 {
				t.Fatalf("unsupported detector became quiet or unusual: %d: %s", code, errout.String())
			}
			var report compare.TokenCUSUMScanReport
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			if report.TokenCUSUM.Status != test.want || report.UnusualWindows != 0 || len(report.Windows[0].Findings) != 0 {
				t.Fatalf("diagnostic changed finding/alert semantics: %+v", report)
			}
		})
	}
}

func TestTokenCUSUMCLIOptionsAndErrors(t *testing.T) {
	directory := tokenCUSUMDirectory(t, true)
	base := []string{"scan", directory, "--expected", "PRIVATE_PRODUCER", "--as-of", "1970-01-01T00:26:00Z"}
	for _, options := range [][]string{
		{"--cusum-scale-floor", "1"}, {"--cusum-min-shift", "2", "--token-cusum=false"},
		{"--token-cusum", "--cusum-scale-floor", "NaN"}, {"--token-cusum", "--cusum-scale-floor", "0"},
		{"--token-cusum", "--cusum-scale-floor", "-1"}, {"--token-cusum", "--cusum-min-shift", "Inf"},
		{"--token-cusum", "--cusum-min-shift", "5e-324"}, {"--token-cusum", "--cusum-min-shift", "PRIVATE_BAD"},
	} {
		var out, errout bytes.Buffer
		args := append(append([]string{}, base...), options...)
		if code := Run(args, &out, &errout); code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "PRIVATE_") {
			t.Fatalf("options %v: %d, %s, %s", options, code, out.String(), errout.String())
		}
	}
	var out, errout bytes.Buffer
	if code := Run(append(base, "--token-cusum", "--cusum-scale-floor", "5e-324"), &out, &errout); code != 1 || out.Len() != 0 {
		t.Fatalf("numeric overflow became a partial report: %d, %s", code, out.String())
	}
	errout.Reset()
	if code := Run(append(base, "--token-cusum"), failedWriter{}, &errout); code != 1 || strings.Contains(errout.String(), "SENTINEL") {
		t.Fatalf("output failure was not private: %d, %s", code, errout.String())
	}
}

func TestTokenCUSUMLocalBinary(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_G9_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_G9_BINARY to test a locally built executable")
	}
	for _, enabled := range []bool{false, true} {
		for _, spike := range []bool{false, true} {
			args := []string{"scan", tokenCUSUMDirectory(t, spike), "--expected", "PRIVATE_PRODUCER",
				"--as-of", "1970-01-01T00:26:00Z", "--format", "json"}
			wantCode := 0
			if enabled {
				args = append(args, "--token-cusum")
				wantCode = 4
			}
			if spike {
				wantCode = 3
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			data, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
			cancel()
			code := 0
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != wantCode || bytes.Contains(data, []byte("PRIVATE_")) || !json.Valid(data) {
				t.Fatalf("binary = %d, want %d: %s", code, wantCode, data)
			}
			if spike {
				name := "off.json.golden"
				if enabled {
					name = "on.json.golden"
				}
				assertTokenCUSUMGolden(t, name, data)
			}
		}
	}
}

func tokenCUSUMDirectory(t testing.TB, spike bool) string {
	t.Helper()
	directory := scanDirectory(t, spike)
	files, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		path := filepath.Join(directory, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := summary.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"input", "output"} {
			for _, state := range []string{"reported", "missing", "invalid", "conflict"} {
				doc.Counters["token_observations."+field+"."+state] = 0
			}
			doc.Counters["token_observations."+field+".reported"] = doc.Counters["requests"]
		}
		data, err = doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func assertTokenCUSUMGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "token-cusum", name)
	if os.Getenv("UPDATE_TOKEN_CUSUM_GOLDENS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("output differs from %s\ngot:\n%s", path, got)
	}
}
