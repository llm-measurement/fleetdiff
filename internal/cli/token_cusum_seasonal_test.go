// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/change"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestTokenCUSUMSeasonalCLIOptionRefusals(t *testing.T) {
	for _, test := range []struct {
		name    string
		options []string
		want    string
	}{
		{"requires enabled", []string{"--cusum-stratum", "utc-day-of-week"}, "require --token-cusum"},
		{"explicit empty requires enabled", []string{"--cusum-stratum", ""}, "require --token-cusum"},
		{"equals empty requires enabled", []string{"--cusum-stratum="}, "require --token-cusum"},
		{"explicit false", []string{"--token-cusum=false", "--cusum-stratum", "utc-day-of-week"}, "require --token-cusum"},
		{"explicit false empty", []string{"--token-cusum=false", "--cusum-stratum="}, "require --token-cusum"},
		{"unknown", []string{"--token-cusum", "--cusum-stratum", "PRIVATE_MODE_SENTINEL"}, "unsupported"},
		{"case sensitive", []string{"--token-cusum", "--cusum-stratum", "UTC-DAY-OF-WEEK"}, "unsupported"},
		{"no implicit trimming", []string{"--token-cusum", "--cusum-stratum", " utc-day-of-week"}, "unsupported"},
		{"requires explicit baseline", []string{"--token-cusum", "--cusum-stratum", "utc-day-of-week"}, "requires an explicit --baseline"},
		{"duration", []string{"--token-cusum", "--cusum-stratum", "utc-day-of-week", "--baseline", "1008h"}, "integer baseline"},
		{"short duration", []string{"--token-cusum", "--cusum-stratum", "utc-day-of-week", "--baseline", "6m"}, "integer baseline"},
		{"bad baseline", []string{"--token-cusum", "--cusum-stratum", "utc-day-of-week", "--baseline", "PRIVATE_BASELINE_SENTINEL"}, "integer baseline"},
		{"short count", []string{"--token-cusum", "--cusum-stratum", "utc-day-of-week", "--baseline", "5"}, "scan limits"},
		{"long count", []string{"--token-cusum", "--cusum-stratum", "utc-day-of-week", "--baseline", "385"}, "scan limits"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A missing path makes a premature input read return exit 1, not 2.
			path := filepath.Join(t.TempDir(), "PRIVATE_UNREAD_INPUT_SENTINEL")
			args := append([]string{"scan", path, "--expected", "PRIVATE_PRODUCER"}, test.options...)
			var out, errout bytes.Buffer
			if code := Run(args, &out, &errout); code != 2 || out.Len() != 0 || !strings.Contains(errout.String(), test.want) {
				t.Fatalf("options = %d, stdout %q, stderr %q", code, out.String(), errout.String())
			}
			if strings.Contains(errout.String(), "PRIVATE_") || strings.Contains(errout.String(), path) {
				t.Fatal("option error exposed private input")
			}
		})
	}
}

func TestTokenCUSUMSeasonalCLIReports(t *testing.T) {
	for _, baseline := range []int{6, 24} {
		for _, spike := range []bool{false, true} {
			t.Run(fmt.Sprintf("baseline-%d/spike-%t", baseline, spike), func(t *testing.T) {
				directory, asOf := tokenCUSUMSeasonalDirectory(t, baseline, spike)
				files, err := os.ReadDir(directory)
				if err != nil {
					t.Fatal(err)
				}
				original := make(map[string][]byte, len(files))
				for _, file := range files {
					data, err := os.ReadFile(filepath.Join(directory, file.Name()))
					if err != nil {
						t.Fatal(err)
					}
					original[file.Name()] = data
				}
				for _, format := range []string{"text", "json"} {
					t.Run(format, func(t *testing.T) {
						args := []string{"scan", "--expected", "PRIVATE_PRODUCER", "--as-of", asOf.Format(time.RFC3339),
							"--recent", "7", "--format", format}
						if baseline != 24 {
							args = append(args, "--baseline", strconv.Itoa(baseline))
						}
						var legacy, out, errout bytes.Buffer
						legacyCode := Run(append(append([]string{}, args...), directory), &legacy, &errout)
						wantCode := 4
						if spike {
							wantCode = 3
						}
						if legacyCode != 0 && legacyCode != 3 || errout.Len() != 0 {
							t.Fatalf("legacy scan = %d: %s", legacyCode, errout.String())
						}
						args = append(args, "--token-cusum", "--cusum-stratum", compare.TokenCUSUMWeekdayStratum)
						if baseline == 24 {
							args = append(args, "--baseline", "24")
						}
						// Exercise both documented positional forms on the same input.
						for _, pathFirst := range []bool{false, true} {
							call := append(append([]string{}, args...), directory)
							if pathFirst {
								call = append([]string{"scan", directory}, args[1:]...)
							}
							var got bytes.Buffer
							if code := Run(call, &got, &errout); code != wantCode || errout.Len() != 0 {
								t.Fatalf("seasonal scan = %d, want %d: %s", code, wantCode, errout.String())
							}
							if pathFirst && !bytes.Equal(got.Bytes(), out.Bytes()) {
								t.Fatal("positional spelling changed deterministic output")
							}
							if !pathFirst {
								out.Write(got.Bytes())
							}
							if strings.Contains(got.String(), "PRIVATE_") || strings.Contains(got.String(), directory) {
								t.Fatal("seasonal report exposed private input metadata")
							}
						}
						if format == "text" {
							if !bytes.HasPrefix(out.Bytes(), legacy.Bytes()) {
								t.Fatal("seasonal diagnostics changed legacy text")
							}
							for _, want := range []string{
								"Observed-token CUSUM: uncalibrated (collector processing time).",
								"Seasonality: utc-day-of-week; timezone UTC; cadence 24h; Sunday=0 through Saturday=6.",
								fmt.Sprintf("Reference: %d preceding matching weekly windows;", baseline),
								"[stratum key 0]: uncalibrated; 1000 observed tokens; statistic 0; residual 0.",
								"[stratum key 6]: uncalibrated;",
							} {
								if !strings.Contains(out.String(), want) {
									t.Fatalf("missing seasonal text %q:\n%s", want, out.String())
								}
							}
							return
						}
						var report compare.TokenCUSUMScanReport
						var base compare.ScanReport
						if err := json.Unmarshal(out.Bytes(), &report); err != nil {
							t.Fatal(err)
						}
						if err := json.Unmarshal(legacy.Bytes(), &base); err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(report.ScanReport, base) {
							t.Fatal("seasonal diagnostics changed legacy findings or metadata")
						}
						analysis := report.TokenCUSUM
						wantSeasonality := compare.TokenCUSUMSeasonality{
							Key: compare.TokenCUSUMWeekdayStratum, Timezone: "UTC", Cadence: "24h",
							KeyConvention: "Sunday=0 through Saturday=6", ReferenceUnit: "preceding matching weekly windows",
						}
						if analysis.Seasonality == nil || *analysis.Seasonality != wantSeasonality ||
							analysis.Unit != "observed_tokens" || analysis.Clock != "collector_processing_time" ||
							analysis.Reference != baseline || analysis.Status != change.CUSUMUncalibrated || len(analysis.Windows) != 7 {
							t.Fatalf("incorrect seasonal metadata: %+v", analysis)
						}
						end := asOf.Truncate(24 * time.Hour)
						for key, window := range analysis.Windows {
							wantTotal := uint64(1000 + 100*key)
							if spike && key == 6 {
								wantTotal = 8000
							}
							if window.StratumKey == nil || *window.StratumKey != key ||
								window.Start != end.Add(time.Duration(key-7)*24*time.Hour).UnixNano() ||
								window.End-window.Start != int64(24*time.Hour) || window.Total == nil || *window.Total != wantTotal ||
								window.Samples != baseline || window.Status != change.CUSUMUncalibrated ||
								window.Diagnostic == nil || window.Diagnostic.Median != float64(1000+100*key) {
								t.Fatalf("incorrect weekday %d (including serialized key zero): %+v", key, window)
							}
						}
					})
				}
				after, err := os.ReadDir(directory)
				if err != nil || len(after) != len(original) {
					t.Fatalf("scan changed input directory: %v", err)
				}
				for _, file := range after {
					got, err := os.ReadFile(filepath.Join(directory, file.Name()))
					if err != nil || !bytes.Equal(got, original[file.Name()]) {
						t.Fatalf("scan changed input %s: %v", file.Name(), err)
					}
				}
			})
		}
	}
}

func TestTokenCUSUMSeasonalCLIReportsRequiredHistory(t *testing.T) {
	directory, asOf := tokenCUSUMSeasonalDirectory(t, 6, false)
	args := []string{"scan", directory, "--expected", "PRIVATE_PRODUCER", "--as-of", asOf.Format(time.RFC3339),
		"--token-cusum", "--cusum-stratum", compare.TokenCUSUMWeekdayStratum, "--baseline=24"}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, errout bytes.Buffer
			if code := Run(append(args, "--format", format), &out, &errout); code != 4 || errout.Len() != 0 {
				t.Fatalf("incomplete history = %d: %s", code, errout.String())
			}
			want := "Needs 24 matching weeks per weekday; found 6 consecutive eligible weeks."
			if !strings.Contains(out.String(), want) {
				t.Fatalf("missing required versus found history %q: %s", want, out.String())
			}
		})
	}
}

func TestTokenCUSUMSeasonalCLIEmptyModePreservesOutput(t *testing.T) {
	directory := tokenCUSUMDirectory(t, true)
	for _, format := range []string{"text", "json"} {
		for _, baseline := range []string{"24", "6m"} {
			args := []string{"scan", directory, "--expected", "PRIVATE_PRODUCER", "--as-of", "1970-01-01T00:26:00Z",
				"--token-cusum", "--format", format, "--baseline", baseline}
			var before, after, errout bytes.Buffer
			if code := Run(args, &before, &errout); code != 3 || errout.Len() != 0 {
				t.Fatalf("nonseasonal scan = %d: %s", code, errout.String())
			}
			if code := Run(append(args, "--cusum-stratum="), &after, &errout); code != 3 || errout.Len() != 0 || !bytes.Equal(before.Bytes(), after.Bytes()) {
				t.Fatalf("empty mode changed nonseasonal output: %d: %s", code, errout.String())
			}
			if strings.Contains(after.String(), "seasonality") || strings.Contains(after.String(), "stratum key") {
				t.Fatal("nonseasonal output acquired seasonal metadata")
			}
		}
	}
}

func TestTokenCUSUMSeasonalCLIInputAndOutputErrors(t *testing.T) {
	misaligned, asOf := tokenCUSUMSeasonalDirectory(t, 6, false)
	files, err := os.ReadDir(misaligned)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		path := filepath.Join(misaligned, file.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := summary.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		doc.WindowStart += int64(time.Hour)
		doc.ObservedStart += int64(time.Hour)
		doc.ObservedEnd += int64(time.Hour)
		doc.EmittedAt += int64(time.Hour)
		// Bypass the canonical encoder, which already refuses this alignment.
		data, err = json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name, path, want string
	}{
		{"not daily", tokenCUSUMDirectory(t, false), "one-day"},
		{"not UTC aligned", misaligned, ""},
		{"URL is not fetched", "https://PRIVATE_NETWORK_SENTINEL.invalid/summary", ""},
		{"missing local input", filepath.Join(t.TempDir(), "PRIVATE_MISSING_SENTINEL"), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, errout bytes.Buffer
			args := []string{"scan", test.path, "--expected", "PRIVATE_PRODUCER", "--as-of", asOf.Format(time.RFC3339),
				"--token-cusum", "--cusum-stratum", compare.TokenCUSUMWeekdayStratum, "--baseline", "6"}
			if code := Run(args, &out, &errout); code != 1 || out.Len() != 0 || !strings.Contains(errout.String(), test.want) {
				t.Fatalf("input error = %d, %q, %q", code, out.String(), errout.String())
			}
			if strings.Contains(errout.String(), "PRIVATE_") || strings.Contains(errout.String(), test.path) {
				t.Fatal("input error exposed private metadata")
			}
		})
	}
	directory, asOf := tokenCUSUMSeasonalDirectory(t, 6, false)
	for _, format := range []string{"text", "json"} {
		var errout bytes.Buffer
		args := []string{"scan", directory, "--expected", "PRIVATE_PRODUCER", "--as-of", asOf.Format(time.RFC3339),
			"--token-cusum", "--cusum-stratum", compare.TokenCUSUMWeekdayStratum, "--baseline", "6", "--format", format}
		if code := Run(args, failedWriter{}, &errout); code != 1 || strings.Contains(errout.String(), "SENTINEL") {
			t.Fatalf("output failure = %d: %s", code, errout.String())
		}
	}
}

func TestTokenCUSUMSeasonalCLIHelp(t *testing.T) {
	var out, errout bytes.Buffer
	if code := Run([]string{"scan", "--help"}, &out, &errout); code != 0 || errout.Len() != 0 {
		t.Fatalf("help = %d: %s", code, errout.String())
	}
	for _, want := range []string{"--cusum-stratum MODE", "utc-day-of-week", "UTC-aligned daily windows", "preceding matching weekly windows", "explicit", "no default"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help is missing %q", want)
		}
	}
}

func TestTokenCUSUMSeasonalLocalBinary(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_G9_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_G9_BINARY to test a locally built executable")
	}
	for _, spike := range []bool{false, true} {
		t.Run(fmt.Sprintf("spike-%t", spike), func(t *testing.T) {
			directory, asOf := tokenCUSUMSeasonalDirectory(t, 6, spike)
			for _, format := range []string{"text", "json"} {
				t.Run(format, func(t *testing.T) {
					args := []string{"scan", directory, "--expected", "PRIVATE_PRODUCER", "--as-of", asOf.Format(time.RFC3339),
						"--token-cusum", "--cusum-stratum", compare.TokenCUSUMWeekdayStratum, "--baseline", "6", "--recent", "7", "--format", format}
					var wantOut, wantErr bytes.Buffer
					wantCode := Run(args, &wantOut, &wantErr)
					expectedCode := 4
					if spike {
						expectedCode = 3
					}
					if wantCode != expectedCode || wantErr.Len() != 0 {
						t.Fatalf("in-process scan = %d, want %d: %s", wantCode, expectedCode, wantErr.String())
					}
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					defer cancel()
					command := exec.CommandContext(ctx, binary, args...)
					var gotOut, gotErr bytes.Buffer
					command.Stdout, command.Stderr = &gotOut, &gotErr
					err := command.Run()
					if ctx.Err() != nil {
						t.Fatalf("binary deadline exceeded: %v", ctx.Err())
					}
					code := 0
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else if err != nil {
						t.Fatal(err)
					}
					if code != wantCode || !bytes.Equal(gotOut.Bytes(), wantOut.Bytes()) || !bytes.Equal(gotErr.Bytes(), wantErr.Bytes()) {
						t.Fatalf("binary exit = %d, want %d\nstdout = %q\nwant stdout = %q\nstderr = %q\nwant stderr = %q",
							code, wantCode, gotOut.String(), wantOut.String(), gotErr.String(), wantErr.String())
					}
				})
			}
		})
	}
}

func tokenCUSUMSeasonalDirectory(t testing.TB, baseline int, spike bool) (string, time.Time) {
	t.Helper()
	directory := t.TempDir()
	start := time.Date(2026, time.January, 4, 0, 0, 0, 0, time.UTC) // Sunday, key zero.
	days := (baseline + 1) * 7
	for day := range days {
		window := start.Add(time.Duration(day) * 24 * time.Hour)
		end := window.Add(24 * time.Hour)
		doc := summary.Envelope{
			Version: 1, ProducerID: "PRIVATE_PRODUCER", Epoch: "PRIVATE_EPOCH", ScopeID: "PRIVATE_SCOPE",
			KeyID: "PRIVATE_KEY", AccountingID: "PRIVATE_ACCOUNTING", Sequence: 1,
			WindowStart: window.UnixNano(), WindowDuration: int64(24 * time.Hour),
			ObservedStart: window.UnixNano(), ObservedEnd: end.UnixNano(), EmittedAt: end.UnixNano(),
			Counters: map[string]uint64{
				"requests": 100, "input_tokens": uint64(1000 + 100*int(window.Weekday())),
				"output_tokens": 0, "missing_token_usage": 0,
			},
			Sketches: map[string]summary.Payload{},
		}
		if spike && day == days-1 {
			doc.Counters["input_tokens"] = 8000
		}
		for _, field := range []string{"input", "output"} {
			for _, state := range []string{"reported", "missing", "invalid", "conflict"} {
				doc.Counters["token_observations."+field+"."+state] = 0
			}
			doc.Counters["token_observations."+field+".reported"] = doc.Counters["requests"]
		}
		data, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("PRIVATE_FILE_%03d.json", day)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return directory, start.Add(time.Duration(days)*24*time.Hour + 12*time.Hour)
}
