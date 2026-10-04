// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestReleaseBinaryCache(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test a packaged executable")
	}
	cmd := exec.Command(binary, "investigate", "--before", "../../examples/cache/data/before", "--after", "../../examples/cache/data/after", "--expected", "app", "--format", "json")
	data, err := cmd.Output()
	if err != nil {
		t.Fatal("packaged cache investigation failed")
	}
	var r compare.Investigation
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal("invalid packaged cache report")
	}
	for _, q := range r.Questions {
		if q.ID != "cache" {
			continue
		}
		if q.Status != "observed" || q.Cache == nil || q.Cache.BeforeShare != .6 || q.Cache.AfterShare != .2 || q.Cache.DeltaPercentagePoints != -40 {
			t.Fatal("packaged cache arithmetic changed", q)
		}
		return
	}
	t.Fatal("packaged binary lacks cache comparison")
}
