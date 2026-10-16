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

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func TestReleaseBinaryWhatIf(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test an unpacked release archive")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		for _, mode := range []string{"simple", "scenario", "mapped"} {
			t.Run(mode+"/"+format, func(t *testing.T) {
				args := whatIfArgs()
				if mode == "simple" {
					args = append(args[:len(args)-2], "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5")
				}
				if mode == "mapped" {
					args = append(args, "--workload-map", "../../examples/what-if/workload-map.json")
				}
				args = append(args, "--format", format)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, args...)
				var diagnostic bytes.Buffer
				cmd.Stderr = &diagnostic
				output, err := cmd.Output()
				if err != nil || diagnostic.Len() != 0 {
					t.Fatalf("packaged what-if command failed: %v", err)
				}
				if bytes.Contains(output, []byte("SENTINEL_")) {
					t.Fatal("packaged what-if exposed a raw identity")
				}
				if mode != "mapped" {
					want, err := os.ReadFile("../../examples/what-if/expected." + format)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(output, want) {
						t.Fatal("packaged what-if differs from the checked example")
					}
					if format == "text" {
						doc, err := os.ReadFile("../../docs/releases/v0.10.0.md")
						if err != nil {
							t.Fatal(err)
						}
						first, _, _ := strings.Cut(string(output), "\n")
						if !bytes.Contains(doc, []byte(first)) {
							t.Fatal("release note headline differs from the packaged output")
						}
					}
				} else if format == "json" {
					var report reprice.Report
					if err := json.Unmarshal(output, &report); err != nil {
						t.Fatal(err)
					}
					if report.Schema != "fleetdiff-what-if/v1" || len(report.Level2) != 1 ||
						report.Level2[0].Workload != "mapped" || report.Unmapped != 0 ||
						report.Covered != 3 || report.Excluded["missing_split"] != 1 {
						t.Fatal("packaged what-if workload mapping or coverage changed")
					}
				} else if !bytes.Contains(output, []byte("Level 2, mapped:")) {
					t.Fatal("packaged what-if text lacks the mapped result")
				}
			})
		}
	}
	t.Run("summary gap", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "what-if", "--summaries", "../../examples/sessions/data/before", "--expected", "app",
			"--period", "1970-01-01", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5", "--format", "json")
		var diagnostic bytes.Buffer
		cmd.Stderr = &diagnostic
		output, err := cmd.Output()
		if err != nil || diagnostic.Len() != 0 {
			t.Fatalf("packaged summary gap failed: %v", err)
		}
		var report reprice.Report
		if err := json.Unmarshal(output, &report); err != nil {
			t.Fatal(err)
		}
		if report.Before != nil || report.Level1 != nil || len(report.Level2) != 0 || len(report.Source.Gaps) == 0 {
			t.Fatal("packaged what-if fabricated summary costs")
		}
	})
	missingScenario := whatIfArgs()
	missingScenario[len(missingScenario)-1] = "SENTINEL_PATH"
	badFraction := whatIfArgs()
	badFraction = append(badFraction[:len(badFraction)-2], "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "SENTINEL_FRACTION")
	for name, args := range map[string][]string{
		"unknown flag": {"what-if", "--SENTINEL_FLAG"},
		"private path": missingScenario,
		"bad fraction": badFraction,
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, args...)
			var output, diagnostic bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &diagnostic
			err := cmd.Run()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 2 || output.Len() != 0 || diagnostic.Len() == 0 {
				t.Fatalf("packaged what-if did not reject invalid input: %v", err)
			}
			if strings.Contains(output.String()+diagnostic.String(), "SENTINEL_") {
				t.Fatal("packaged what-if error exposed private input")
			}
		})
	}
}
