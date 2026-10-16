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
)

func summaryWhatIfArgs(t *testing.T) []string {
	t.Helper()
	contract, err := os.ReadFile("../../examples/what-if-summaries/snapshots/contract.txt")
	if err != nil {
		t.Fatal(err)
	}
	return []string{"what-if", "--summaries", "../../examples/what-if-summaries/snapshots", "--expected", "demo-a,demo-b",
		"--period", "2026-10-07T00:00:00Z/2026-10-07T00:01:00Z", "--model-usage-contract", strings.TrimSpace(string(contract)),
		"--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5"}
}

func TestWhatIfModelUsageCheckedExample(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var out, errs bytes.Buffer
		if Run(append(summaryWhatIfArgs(t), "--format", format), &out, &errs) != 0 || errs.Len() != 0 {
			t.Fatalf("summary repricing: %s", &errs)
		}
		want, err := os.ReadFile("../../examples/what-if-summaries/expected." + format)
		if err != nil || !bytes.Equal(want, out.Bytes()) {
			t.Fatalf("summary example changed: %v\n%s", err, &out)
		}
	}
}

func TestWhatIfModelUsageErrorsAreActionableAndPrivate(t *testing.T) {
	for _, contract := range []string{"", "SENTINEL_CONTRACT", "model-usage-v1:" + strings.Repeat("0", 64)} {
		args := summaryWhatIfArgs(t)
		args[8] = contract
		var out, errs bytes.Buffer
		if Run(args, &out, &errs) == 0 || out.Len() != 0 || strings.Contains(errs.String(), "SENTINEL") {
			t.Fatalf("invalid contract emitted a report or leaked: %s %s", &out, &errs)
		}
	}
	data, err := os.ReadFile("../../examples/what-if-summaries/snapshots/a-latest.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	var block map[string]json.RawMessage
	if err = json.Unmarshal(doc["model_usage"], &block); err != nil {
		t.Fatal(err)
	}
	block["version"] = json.RawMessage("2")
	doc["model_usage"], err = json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "SENTINEL_PATH.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	args := summaryWhatIfArgs(t)
	args[2] = path
	var out, errs bytes.Buffer
	if Run(args, &out, &errs) == 0 || out.Len() != 0 || !strings.Contains(errs.String(), "upgrade fleetdiff") || strings.Contains(errs.String(), "SENTINEL") {
		t.Fatalf("unsupported extension not actionable/private: %s %s", &out, &errs)
	}
}

func TestReleaseBinaryModelUsage(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test an unpacked release archive")
	}
	var err error
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		cmd := exec.CommandContext(ctx, binary, append(summaryWhatIfArgs(t), "--format", format)...)
		var errs bytes.Buffer
		cmd.Stderr = &errs
		out, err := cmd.Output()
		cancel()
		if err != nil || errs.Len() != 0 {
			t.Fatalf("packaged summary command: %v %s", err, &errs)
		}
		want, err := os.ReadFile("../../examples/what-if-summaries/expected." + format)
		if err != nil || !bytes.Equal(out, want) {
			t.Fatalf("packaged summary report changed: %v", err)
		}
	}
}

func TestSummaryWhatIfRejectsUnavailableIntersections(t *testing.T) {
	base := []string{"what-if", "--summaries", "../../examples/sessions/data/before", "--expected", "app", "--period", "1970-01-01", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5"}
	for _, extra := range [][]string{
		{"--scope-key", "SENTINEL_KEY"},
		{"--scope-team", "SENTINEL_TEAM"},
		{"--workload-map", "SENTINEL_PATH"},
	} {
		var out, errs bytes.Buffer
		args := append(append([]string{}, base...), extra...)
		if Run(args, &out, &errs) != 2 || out.Len() != 0 || !strings.Contains(errs.String(), "intersections") || strings.Contains(errs.String(), "SENTINEL") {
			t.Fatalf("unavailable intersection accepted or leaked: %s %s", &out, &errs)
		}
	}
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"moves":[{"from":"claude-sonnet-5-5","to":"gpt-6.1-sol","fraction":"0.5","scope":{"kind":"key","value":"SENTINEL_KEY"}}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if Run(append(base[:7:7], "--scenario", path), &out, &errs) != 2 || out.Len() != 0 || !strings.Contains(errs.String(), "intersections") || strings.Contains(errs.String(), "SENTINEL") {
		t.Fatalf("scenario intersection: %s %s", &out, &errs)
	}
}

func TestWhatIfContractIsSummaryOnly(t *testing.T) {
	var out, errs bytes.Buffer
	if Run(append(whatIfArgs(), "--model-usage-contract", "SENTINEL_CONTRACT"), &out, &errs) != 2 || out.Len() != 0 || strings.Contains(errs.String(), "SENTINEL") {
		t.Fatalf("contract flag leaked or applied to spend: %s %s", &out, &errs)
	}
}
