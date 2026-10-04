// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestInspectAccountingContract(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "inspect-contract", "v1")
	files, err := readInspectContractCorpus(dir)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Schema       string `json:"schema"`
		AccountingID string `json:"accounting_id"`
		Cases        []struct {
			Name     string `json:"name"`
			Input    string `json:"input"`
			Expected struct {
				Metrics           map[string]uint64 `json:"metrics"`
				ObservedCounters  map[string]uint64 `json:"observed_counters"`
				TokenObservations map[string]uint64 `json:"token_observations"`
				UsageProvenance   map[string]uint64 `json:"usage_provenance"`
			} `json:"expected"`
			Error bool `json:"error"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "genai-inspect-accounting/v1" || manifest.AccountingID != "genai-default-accounting/v1+usage-provenance/v1" {
		t.Fatal("unexpected accounting contract version")
	}
	if len(manifest.Cases) != 36 {
		t.Fatal("accounting contract cases are missing")
	}
	names, inputs := map[string]bool{}, map[string]bool{}
	for _, tc := range manifest.Cases {
		if tc.Name == "" || names[tc.Name] || inputs[tc.Input] || tc.Input == "manifest.json" || filepath.Ext(tc.Input) != ".json" || files[tc.Input] == nil {
			t.Fatal("duplicate case or invalid input reference")
		}
		names[tc.Name], inputs[tc.Input] = true, true
		t.Run(tc.Name, func(t *testing.T) {
			report, err := Inspect(filepath.Join(dir, tc.Input), nil, Options{InputFormat: "auto", Top: 10})
			if tc.Error {
				if len(tc.Expected.Metrics)+len(tc.Expected.ObservedCounters)+len(tc.Expected.TokenObservations)+len(tc.Expected.UsageProvenance) != 0 {
					t.Fatal("error cases must not promise partial-success counters")
				}
				if err == nil {
					t.Fatal("expected accounting error, not a partial success")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.Expected.Metrics) != 11 || len(tc.Expected.TokenObservations) != 25 || len(tc.Expected.UsageProvenance) != 8 {
				t.Fatal("successful cases require the complete fixed counter maps, including zeros")
			}
			// Compare whole maps without filling absent keys: omission of a zero
			// or addition of an unsupported field is a contract change too.
			if !reflect.DeepEqual(report.Metrics, tc.Expected.Metrics) {
				t.Errorf("metrics:\n got %v\nwant %v", report.Metrics, tc.Expected.Metrics)
			}
			wantObserved := tc.Expected.ObservedCounters
			if wantObserved == nil {
				wantObserved = tc.Expected.Metrics
			}
			if !reflect.DeepEqual(report.ObservedCounters, wantObserved) {
				t.Errorf("observed counters:\n got %v\nwant %v", report.ObservedCounters, wantObserved)
			}
			if len(wantObserved) != len(tc.Expected.Metrics) {
				t.Fatal("observed counters must be a complete map")
			}
			wantSaturations := []string{}
			for name, metric := range tc.Expected.Metrics {
				observed, present := wantObserved[name]
				if !present || metric != min(observed, uint64(math.MaxInt64)) {
					t.Fatalf("invalid observed/exported counter expectation: %s", name)
				}
				if observed > math.MaxInt64 {
					wantSaturations = append(wantSaturations, name)
				}
			}
			slices.Sort(wantSaturations)
			if !reflect.DeepEqual(report.MetricSaturations, wantSaturations) {
				t.Errorf("metric saturations: got %v, want %v", report.MetricSaturations, wantSaturations)
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				ObservedCounters map[string]uint64 `json:"observed_counters"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil || !reflect.DeepEqual(wire.ObservedCounters, wantObserved) {
				t.Fatal("observed_counters must always be present and exact in report JSON")
			}
			if !reflect.DeepEqual(report.TokenObservations, tc.Expected.TokenObservations) {
				t.Errorf("token observations:\n got %v\nwant %v", report.TokenObservations, tc.Expected.TokenObservations)
			}
			if !reflect.DeepEqual(report.UsageProvenance, tc.Expected.UsageProvenance) {
				t.Errorf("usage provenance:\n got %v\nwant %v", report.UsageProvenance, tc.Expected.UsageProvenance)
			}
		})
	}
	for name := range files {
		if filepath.Ext(name) == ".json" && name != "manifest.json" && !inputs[name] {
			t.Fatalf("unreferenced contract input: %s", name)
		}
	}
}

func TestInspectContractCounterpart(t *testing.T) {
	counterpart, set := os.LookupEnv("INSPECT_CONTRACT_COUNTERPART")
	if !set {
		t.Skip("set INSPECT_CONTRACT_COUNTERPART to the collector's inspect-contract/v1 directory to compare copies")
	}
	if counterpart == "" {
		t.Fatal("INSPECT_CONTRACT_COUNTERPART is set but empty")
	}
	local, err := readInspectContractCorpus(filepath.Join("..", "..", "testdata", "inspect-contract", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	remote, err := readInspectContractCorpus(counterpart)
	if err != nil {
		t.Fatalf("cannot verify explicit INSPECT_CONTRACT_COUNTERPART: %v", err)
	}
	if len(local) != len(remote) {
		t.Fatalf("corpus inventories differ: local %d files, counterpart %d", len(local), len(remote))
	}
	for name, data := range local {
		if other, ok := remote[name]; !ok || !bytes.Equal(data, other) {
			t.Errorf("corpus differs: %s", name)
		}
	}
}

func readInspectContractCorpus(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("contract entry is not a regular file: %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		files[entry.Name()] = data
	}
	if len(files["SHA256SUMS"]) == 0 || len(files["manifest.json"]) == 0 {
		return nil, fmt.Errorf("contract requires SHA256SUMS and manifest.json")
	}
	checked := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(files["SHA256SUMS"])), "\n") {
		wantHex, name, ok := strings.Cut(line, "  ")
		if !ok || filepath.Base(name) != name || filepath.Ext(name) != ".json" || checked[name] {
			return nil, fmt.Errorf("invalid or duplicate contract checksum entry")
		}
		want, err := hex.DecodeString(wantHex)
		if err != nil || len(want) != sha256.Size {
			return nil, fmt.Errorf("invalid SHA-256 digest for %s", name)
		}
		data, present := files[name]
		if !present {
			return nil, fmt.Errorf("checksummed contract file is missing: %s", name)
		}
		got := sha256.Sum256(data)
		if !bytes.Equal(got[:], want) {
			return nil, fmt.Errorf("contract checksum mismatch: %s", name)
		}
		checked[name] = true
	}
	for name := range files {
		if filepath.Ext(name) == ".json" && !checked[name] {
			return nil, fmt.Errorf("contract JSON is not checksummed: %s", name)
		}
	}
	return files, nil
}

func TestInspectContractIntegrityRejectsInvalidCorpus(t *testing.T) {
	digest := sha256.Sum256([]byte("{}\n"))
	line := hex.EncodeToString(digest[:]) + "  manifest.json\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"missing manifest", map[string]string{"SHA256SUMS": line}},
		{"missing checksums", map[string]string{"manifest.json": "{}\n"}},
		{"changed content", map[string]string{"manifest.json": "{\"changed\":true}\n", "SHA256SUMS": line}},
		{"duplicate checksum", map[string]string{"manifest.json": "{}\n", "SHA256SUMS": line + line}},
		{"unlisted JSON", map[string]string{"manifest.json": "{}\n", "extra.json": "{}\n", "SHA256SUMS": line}},
		{"missing input", map[string]string{"manifest.json": "{}\n", "SHA256SUMS": line + strings.ReplaceAll(line, "manifest.json", "missing.json")}},
		{"path traversal", map[string]string{"manifest.json": "{}\n", "SHA256SUMS": strings.ReplaceAll(line, "manifest.json", "../manifest.json")}},
		{"invalid digest", map[string]string{"manifest.json": "{}\n", "SHA256SUMS": "not-a-sha256  manifest.json\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readInspectContractCorpus(dir); err == nil {
				t.Fatal("invalid corpus passed integrity validation")
			}
		})
	}
	if _, err := readInspectContractCorpus(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing explicit corpus directory must fail")
	}
}
