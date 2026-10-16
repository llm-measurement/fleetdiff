// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/modelusage"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestCanonicalFixturesAndContract(t *testing.T) {
	docs, contract, err := fixtures()
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil || !bytes.Contains(readme, []byte(contract)) {
		t.Fatal("README does not display the computed contract")
	}
	dir := filepath.Join(t.TempDir(), "summaries")
	generatedContract, err := generate(dir)
	if err != nil || generatedContract != contract {
		t.Fatalf("generation failed: %v", err)
	}
	if _, err := generate(dir); err == nil {
		t.Fatal("generator overwrote an existing directory")
	}
	var input []summary.Envelope
	for name, doc := range docs {
		want, err := doc.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		for _, root := range []string{dir, "snapshots"} {
			got, err := os.ReadFile(filepath.Join(root, name))
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("canonical fixture differs: %s, %v", name, err)
			}
		}
		input = append(input, doc)
	}
	for _, root := range []string{dir, "snapshots"} {
		data, err := os.ReadFile(filepath.Join(root, "contract.txt"))
		if err != nil || string(data) != contract+"\n" {
			t.Fatal("expected contract differs from configured model map and routing")
		}
	}
	result, err := summary.Combine(input, []string{"demo-a", "demo-b"})
	if err != nil || result.Counters["requests"] != 9 || len(result.Sources) != 2 || len(result.Missing) != 0 || len(result.Partial) != 0 {
		t.Fatalf("latest/replay fixture changed: %+v, %v", result, err)
	}
	var covered, excluded uint64
	for id, row := range result.ModelUsage.Models {
		if modelusage.IsReserved(id) {
			excluded += row.Requests
		} else {
			covered += row.Complete.Requests
			excluded += row.Excluded.InvalidUsage + row.Excluded.MissingSplit + row.Excluded.ZeroOriginUnknown
		}
	}
	if covered != 5 || excluded != 4 {
		t.Fatalf("example coverage changed: covered %d, excluded %d", covered, excluded)
	}
	bin := result.ModelUsage.Models["claude-sonnet-5-5"].Histogram[1].Complete
	if bin.Requests != 2 || bin.InputTokens != 310000 || bin.InputMax == nil || *bin.InputMax != 160000 {
		t.Fatal("example no longer distinguishes cohort totals from request sizes")
	}
}
