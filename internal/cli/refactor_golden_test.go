// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// These bytes were recorded from v0.8.0, before extracting shared import helpers.
func TestImportRefactorGoldenOutputs(t *testing.T) {
	t.Setenv("FLEETDIFF_GOLDEN_KEY", "synthetic-refactor-regression-key-32-bytes")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"inspect", []string{"inspect", "--secret-env", "FLEETDIFF_GOLDEN_KEY", "--show-hashes"}},
		{"spend-periods", []string{"investigate", "--litellm-spend", "../../examples/litellm-spend/synthetic.csv", "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--group-by", "session", "--hash-secret-env", "FLEETDIFF_GOLDEN_KEY", "--show-hashes"}},
		{"session-review", []string{"investigate", "--litellm-spend", "../../examples/litellm-spend/session-budget.csv", "--period", "2026-10-03", "--session-budget-usd", "1", "--hash-secret-env", "FLEETDIFF_GOLDEN_KEY", "--show-hashes"}},
		{"cache-periods", []string{"investigate", "--litellm-spend", "../../examples/litellm-spend/cache-drop.csv", "--before-period", "2026-10-07", "--after-period", "2026-10-08", "--hash-secret-env", "FLEETDIFF_GOLDEN_KEY", "--show-hashes"}},
	} {
		for _, format := range []string{"text", "json"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				args := append(append([]string{}, tc.args...), "--format", format)
				if tc.name == "inspect" {
					args = append(args, "../../examples/inspect/synthetic-traces.json")
				}
				var out, errout bytes.Buffer
				if code := Run(args, &out, &errout); code != 0 || errout.Len() != 0 {
					t.Fatalf("exit=%d stderr=%s", code, &errout)
				}
				path := filepath.Join("testdata", "import-refactor", tc.name+"."+format)
				if os.Getenv("FLEETDIFF_UPDATE_REFACTOR_GOLDENS") == "1" {
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, out.Bytes(), 0644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(out.Bytes(), want) {
					t.Fatalf("output differs from v0.8.0 golden %s (got %d bytes, want %d)", path, out.Len(), len(want))
				}
			})
		}
	}
}
