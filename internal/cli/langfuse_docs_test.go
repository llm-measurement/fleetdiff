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

	"github.com/llm-measurement/fleetdiff/internal/langfuse"
)

func langfuseExampleArgs(format string) []string {
	return []string{"investigate", "--langfuse-export", "../../examples/langfuse-spend/export",
		"--before-period", "2026-10-07T00:00:00Z/2026-10-07T01:00:00Z",
		"--after-period", "2026-10-08T00:00:00Z/2026-10-08T01:00:00Z",
		"--group-by", "trace_name", "--format", format}
}

func checkLangfuseExample(t *testing.T, format string, output []byte) {
	t.Helper()
	for _, raw := range []string{"synthetic-model", "interactive", "background", "synthetic-example"} {
		if bytes.Contains(output, []byte(raw)) {
			t.Fatalf("raw example label leaked: %s", raw)
		}
	}
	if format == "json" {
		var r langfuse.Report
		if err := json.Unmarshal(output, &r); err != nil {
			t.Fatal(err)
		}
		if r.Incomplete || r.Coverage.Incomplete || r.Coverage.UniqueRows != 10 ||
			r.Before.Requests != 4 || r.After.Requests != 6 || r.Before.Tokens != 8000 || r.After.Tokens != 16000 ||
			r.Before.RecordedSpend != "0.008" || r.After.RecordedSpend != "0.016" ||
			r.Before.Quality.UnknownStatus != 4 || r.After.Quality.UnknownStatus != 6 ||
			r.Before.Quality.Unclear != 4 || r.After.Quality.Unclear != 6 ||
			r.Increase == nil || r.Increase.Count != 1 || r.Increase.Share.Lower != 1 {
			t.Fatal("public example totals, contributor or manifest coverage differ")
		}
	} else {
		for _, want := range []string{"Recorded tokens doubled (8,000 -> 16,000).", "Logged model requests: 4 -> 6.",
			"trace_name-1: 4,000 -> 12,000 tokens; change +8,000.",
			"trace_name-2: 4,000 -> 4,000 tokens; change +0.",
			"1 leading tracked trace_name accounts for 100% of the net recorded increase.",
			"Unknown call status (not itself a usage-review flag when level is DEFAULT/DEBUG/WARNING): 4 -> 6."} {
			if !strings.Contains(string(output), want) {
				t.Fatalf("example missing %q", want)
			}
		}
	}
}

func TestLangfuseGuideStatusContract(t *testing.T) {
	doc, err := os.ReadFile("../../docs/LANGFUSE.md")
	if err != nil {
		t.Fatal(err)
	}
	want := "Non-error severity does not prove a successful provider call or complete billing."
	if !strings.Contains(strings.Join(strings.Fields(string(doc)), " "), want) {
		t.Fatal("Langfuse severity must not imply provider success or billing completeness")
	}
}

func TestLangfusePublicExample(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			if code := Run(langfuseExampleArgs(format), &out, &diagnostic); code != 0 || diagnostic.Len() != 0 {
				t.Fatalf("example command failed: code %d %s", code, diagnostic.String())
			}
			checkLangfuseExample(t, format, out.Bytes())
		})
	}
}

func TestReleaseBinaryLangfuseExample(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test the packaged binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, langfuseExampleArgs(format)...)
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			out, err := cmd.Output()
			if err != nil || diagnostic.Len() != 0 {
				t.Fatalf("packaged example failed: %v %s", err, diagnostic.String())
			}
			checkLangfuseExample(t, format, out)
		})
	}
	t.Run("installer-with-separate-checkout", func(t *testing.T) {
		dir := t.TempDir()
		example := filepath.Join(dir, "fleetdiff", "examples", "langfuse-spend", "export")
		if err := os.CopyFS(example, os.DirFS("../../examples/langfuse-spend/export")); err != nil {
			t.Fatal(err)
		}
		args := langfuseExampleArgs("text")
		args[2] = "fleetdiff/examples/langfuse-spend/export"
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = dir
		var diagnostic bytes.Buffer
		cmd.Stderr = &diagnostic
		out, err := cmd.Output()
		if err != nil || diagnostic.Len() != 0 {
			t.Fatalf("installer with checkout example failed: %v %s", err, diagnostic.String())
		}
		checkLangfuseExample(t, "text", out)
	})
}
