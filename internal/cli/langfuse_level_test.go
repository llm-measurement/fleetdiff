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

func TestLangfuseLevelCLIReviewPrivacyAndSubsets(t *testing.T) {
	testLangfuseLevel(t, "")
}

func TestReleaseBinaryLangfuseLevel(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test the packaged binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	testLangfuseLevel(t, binary)
}

func testLangfuseLevel(t *testing.T, binary string) {
	t.Helper()
	for _, mode := range []string{"known-non-error", "error-and-invalid"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.CopyFS(root, os.DirFS("../../examples/langfuse-spend/export")); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(root, "synthetic-langfuse/observations_v2/*.jsonl"))
			if err != nil || len(files) != 2 {
				t.Fatal("synthetic files unavailable")
			}
			for _, file := range files {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var body strings.Builder
				for i, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
					level := []string{"DEFAULT", "DEBUG", "WARNING"}[i%3]
					if mode == "error-and-invalid" && i == 0 {
						level = "ERROR"
					} else if mode == "error-and-invalid" && i == 1 {
						level = "PRIVATE_LEVEL_SENTINEL"
					}
					body.WriteString(`{"level":"` + level + `","input":"PRIVATE_INPUT_SENTINEL","output":"PRIVATE_OUTPUT_SENTINEL","metadata":"PRIVATE_METADATA_SENTINEL","tool_calls":"PRIVATE_TOOLS_SENTINEL","status_message":"PRIVATE_STATUS_SENTINEL",` + line[1:] + "\n")
				}
				if err := os.WriteFile(file, []byte(body.String()), 0600); err != nil {
					t.Fatal(err)
				}
			}
			manifests, err := filepath.Glob(filepath.Join(root, "synthetic-langfuse/manifests/*.json"))
			if err != nil || len(manifests) != 2 {
				t.Fatal("synthetic manifests unavailable")
			}
			for _, path := range manifests {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				for _, f := range m["files"].([]any) {
					file := f.(map[string]any)
					info, err := os.Stat(filepath.Join(root, "synthetic-langfuse/observations_v2", filepath.Base(file["key"].(string))))
					if err != nil {
						t.Fatal(err)
					}
					file["sizeBytes"] = info.Size()
				}
				data, _ = json.Marshal(m)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, format := range []string{"text", "json"} {
				args := langfuseExampleArgs(format)
				args[2] = root
				var out, diagnostic bytes.Buffer
				code := 0
				if binary == "" {
					code = Run(args, &out, &diagnostic)
				} else {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					cmd := exec.CommandContext(ctx, binary, args...)
					cmd.Stdout, cmd.Stderr = &out, &diagnostic
					err := cmd.Run()
					cancel()
					if err != nil {
						t.Fatal("packaged severity command failed", err)
					}
				}
				if code != 0 || diagnostic.Len() != 0 {
					t.Fatal("severity command failed", code, diagnostic.String())
				}
				if strings.Contains(out.String()+diagnostic.String(), "PRIVATE_") {
					t.Fatal("severity or payload sentinel leaked")
				}
				if format == "text" {
					if !strings.Contains(out.String(), "provider call status is unknown") || mode == "error-and-invalid" && !strings.Contains(out.String(), "Tokens recorded on ERROR observations:") {
						t.Fatal("severity/status or error-subset wording absent")
					}
					if mode == "known-non-error" && strings.Contains(out.String(), "Records needing usage review") {
						t.Fatal("non-error severity caused usage-review noise")
					}
					continue
				}
				var r langfuse.Report
				if err := json.Unmarshal(out.Bytes(), &r); err != nil {
					t.Fatal(err)
				}
				if r.Before.Tokens != 8000 || r.After.Tokens != 16000 || r.Before.RecordedSpend != "0.008" || r.After.RecordedSpend != "0.016" || r.Before.Quality.UnknownStatus != 4 || r.After.Quality.UnknownStatus != 6 || r.Before.Quality.Failed != 0 || r.After.Quality.Failed != 0 || r.Volume != nil {
					t.Fatal("level changed recorded totals or claimed provider status")
				}
				if mode == "known-non-error" {
					if r.Before.Quality.Unclear != 0 || r.After.Quality.Unclear != 0 || r.Before.ErrorObservationTokens != nil {
						t.Fatal("non-error severity caused review/error subset")
					}
				} else if r.Before.Quality.Unclear != 2 || r.After.Quality.Unclear != 2 || r.Before.Quality.ErrorObservations != 1 || r.After.Quality.ObservationLevels["unknown"] != 1 || r.Before.ErrorObservationTokens == nil || r.Before.ErrorObservationTokens.Total != 2000 || r.After.ErrorObservationTokens == nil || r.After.ErrorObservationTokens.Total != 3000 {
					t.Fatal("error or invalid severity review/subset differs")
				}
			}
		})
	}
}
