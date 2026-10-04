// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShadowProposal(t *testing.T) {
	proposal := ShadowConfig()
	r := check(t, proposal)
	if r.ExitCode() != 0 {
		t.Fatalf("fixed proposal fails supported checks: %+v", r)
	}
	for _, required := range []string{"PROPOSED STANDALONE", "Do not merge", "secret_env: GENAI_SKETCH_SECRET", "producer_id: shadow", "REVIEW_SCOPE", "REVIEW_KEY_ID", "--expected shadow", "topk: 0", "0700", "independently"} {
		if !strings.Contains(proposal, required) {
			t.Errorf("missing review instruction %q", required)
		}
	}
}

// Validation is opt-in and runs only the trusted generated fixture, never user
// input. No collector packages enter fleetdiff's runtime dependency graph.
func TestShadowCollectorValidation(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_TEST_COLLECTOR")
	if binary == "" {
		t.Skip("set FLEETDIFF_TEST_COLLECTOR to a trusted built distribution")
	}
	path := filepath.Join(t.TempDir(), "shadow.yaml")
	if err := os.WriteFile(path, []byte(ShadowConfig()), 0600); err != nil {
		t.Fatal("cannot prepare validation fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "validate", "--config", path)
	cmd.Env = append(os.Environ(), "GENAI_SKETCH_SECRET=synthetic-validation-secret-at-least-32-bytes")
	if err := cmd.Run(); err != nil {
		t.Fatal("generated configuration failed collector validation")
	}
}
