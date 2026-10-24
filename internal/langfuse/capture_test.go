// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type captureCase struct {
	Name           string     `json:"name"`
	Manifest       string     `json:"manifest"`
	ManifestSHA256 string     `json:"manifest_sha256"`
	Projection     string     `json:"projection"`
	ObservationKey string     `json:"observation_key"`
	RawSHA256      string     `json:"raw_sha256"`
	RawBytes       int64      `json:"raw_bytes"`
	Expected       FileReport `json:"expected"`
}

func captureCases(t *testing.T) []captureCase {
	t.Helper()
	data, err := os.ReadFile("testdata/captured/goldens.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Cases []captureCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &golden); err != nil || len(golden.Cases) != 12 {
		t.Fatal("invalid captured goldens")
	}
	return golden.Cases
}

func captureManifest(t *testing.T, c captureCase) (Manifest, File) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/captured", c.Manifest))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != c.ManifestSHA256 {
		t.Fatal("native manifest bytes changed")
	}
	m, err := ParseManifest(bytes.NewReader(data))
	if err != nil {
		t.Fatal("unmodified native manifest rejected", err)
	}
	if m.ProjectID != "synthetic-capture-project" || len(m.Files) != 2 {
		t.Fatal("unexpected native manifest structure")
	}
	for _, f := range m.Files {
		if f.Key == c.ObservationKey {
			if f.RowCount != 14 || f.SizeBytes != c.RawBytes || f.Table != "observations_v2" {
				t.Fatal("native descriptor differs")
			}
			return m, f
		}
	}
	t.Fatal("native observation descriptor absent")
	return Manifest{}, File{}
}

// This helper decodes ONLY checked-in six-field safe projections, never raw
// exports. Its output is a transformed fixture, not a native-capture byte copy.
func encodeCaptureProjection(t *testing.T, name, kind string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata/captured", name))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 14 {
		t.Fatal("safe period must have 14 rows")
	}
	header := []string{"id", "project_id", "type", "start_time", "usage_details", "total_cost"}
	var csvData bytes.Buffer
	w := csv.NewWriter(&csvData)
	if err := w.Write(header); err != nil {
		t.Fatal(err)
	}
	for _, line := range lines {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(line, &row); err != nil || len(row) != len(header) {
			t.Fatal("invalid safe projection")
		}
		values := make([]string, len(header))
		for i, key := range header {
			raw, ok := row[key]
			if !ok {
				t.Fatal("safe accounting field absent")
			}
			var text string
			if json.Unmarshal(raw, &text) == nil {
				values[i] = text
			} else {
				values[i] = string(raw)
			}
		}
		if err := w.Write(values); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	switch kind {
	case "JSONL":
		return data
	case "JSON":
		return append(append([]byte("["), bytes.Join(lines, []byte(","))...), ']')
	case "CSV":
		return csvData.Bytes()
	default:
		t.Fatal("unexpected captured format")
		return nil
	}
}

func TestCapturedManifestsAndSafeAccountingGoldens(t *testing.T) {
	formats := map[string]int{}
	for _, c := range captureCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			_, native := captureManifest(t, c)
			formats[native.Format]++
			data := encodeCaptureProjection(t, c.Projection, native.FileType)
			if native.Compressed {
				data = compressed(t, data)
			}
			// Only the transformed body's length differs. The native descriptor
			// and original length remain independently asserted above.
			safe := native
			safe.SizeBytes = int64(len(data))
			got, err := ReadFile(bytes.NewReader(data), safe)
			if err != nil {
				t.Fatal("safe captured projection rejected", err)
			}
			if !reflect.DeepEqual(got, c.Expected) {
				t.Fatalf("safe accounting differs: got %+v want %+v", got, c.Expected)
			}
		})
	}
	if len(formats) != 6 {
		t.Fatal("six captured formats required")
	}
	for _, n := range formats {
		if n != 2 {
			t.Fatal("two native windows required per format")
		}
	}
}

func TestCaptureFixtureProvenance(t *testing.T) {
	data, err := os.ReadFile("testdata/captured/PROVENANCE.json")
	if err != nil {
		t.Fatal(err)
	}
	var provenance struct {
		Seal  string `json:"capture_seal_sha256"`
		Files map[string]struct {
			Bytes  int    `json:"bytes"`
			SHA256 string `json:"sha256"`
		} `json:"fixture_files"`
	}
	if json.Unmarshal(data, &provenance) != nil || provenance.Seal != "0c6973c2eb4874153553c099c45abb5e1010866e122275835ed2701a70a2a0a3" || len(provenance.Files) != 15 {
		t.Fatal("invalid capture provenance")
	}
	for name, expected := range provenance.Files {
		data, err := os.ReadFile(filepath.Join("testdata/captured", name))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		if len(data) != expected.Bytes || hex.EncodeToString(hash[:]) != expected.SHA256 {
			t.Fatal("fixture provenance differs", name)
		}
	}
}
