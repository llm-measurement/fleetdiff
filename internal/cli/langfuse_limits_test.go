// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/langfuse"
)

func TestLangfuseFileLimitsAreActionableCLIRefusals(t *testing.T) {
	for _, field := range []string{"rowCount", "sizeBytes"} {
		t.Run(field, func(t *testing.T) {
			root := langfuseDirectory(t)
			paths, err := filepath.Glob(filepath.Join(root, "synthetic-capture-project/manifests/*.json"))
			if err != nil || len(paths) != 2 {
				t.Fatal("test manifests missing")
			}
			data, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if json.Unmarshal(data, &manifest) != nil {
				t.Fatal("invalid test manifest")
			}
			file := manifest["files"].([]any)[0].(map[string]any)
			limit := langfuse.MaxRows
			if field == "sizeBytes" {
				limit = langfuse.MaxFileBytes
			}
			file[field] = limit + 1
			data, err = json.Marshal(manifest)
			if err != nil || os.WriteFile(paths[0], data, 0600) != nil {
				t.Fatal("cannot write test manifest")
			}
			for _, format := range []string{"text", "json"} {
				var out, diagnostic bytes.Buffer
				code := Run([]string{"investigate", "--langfuse-export", root,
					"--before-period", "2026-10-10", "--after-period", "2026-10-17",
					"--format", format}, &out, &diagnostic)
				if code == 0 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "hourly") {
					t.Fatal("limit was not an actionable refusal", code, diagnostic.String())
				}
				if strings.Contains(diagnostic.String(), root) || strings.Contains(diagnostic.String(), "synthetic-capture-project") {
					t.Fatal("private export path in diagnostic")
				}
			}
		})
	}
}
