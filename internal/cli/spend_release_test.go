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

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestReleaseBinarySpend(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test an unpacked release archive")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, encoding := range []string{"csv", "json", "jsonl"} {
		for _, format := range []string{"text", "json"} {
			t.Run(encoding+"/"+format, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, binary, "investigate", "--litellm-spend", "../../examples/litellm-spend/synthetic."+encoding,
					"--before-period", "2026-10-07", "--after-period", "2026-10-08", "--format", format)
				var diagnostic bytes.Buffer
				cmd.Stderr = &diagnostic
				output, err := cmd.Output()
				if err != nil || diagnostic.Len() != 0 {
					t.Fatalf("packaged spend command failed: %v", err)
				}
				for _, sentinel := range []string{"synthetic-", "SENTINEL_", `"hash"`} {
					if bytes.Contains(output, []byte(sentinel)) {
						t.Fatal("private fixture metadata leaked")
					}
				}
				if format == "text" {
					for _, want := range []string{"Recorded tokens doubled (8,000 -> 16,000).", "2 leading tracked keys account for 100%", "No other usage problems found."} {
						if !strings.Contains(string(output), want) {
							t.Fatalf("packaged report missing %q", want)
						}
					}
				} else {
					var r spend.Report
					if err := json.Unmarshal(output, &r); err != nil {
						t.Fatal(err)
					}
					if r.Incomplete || r.Before.Tokens != 8000 || r.After.Tokens != 16000 ||
						r.Before.Quality.ZeroUnknown != 2 || r.After.Quality.ZeroUnknown != 4 ||
						r.Increase == nil || r.Increase.Count != 2 || r.Increase.Share.Lower != 1 {
						t.Fatal("packaged spend accounting differs")
					}
				}
			})
		}
	}
}
