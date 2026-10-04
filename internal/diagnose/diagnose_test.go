// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../examples/diagnose/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func check(t *testing.T, input string) Report {
	t.Helper()
	r, err := Diagnose("-", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hasFinding(r Report, id string) bool {
	for _, f := range r.Findings {
		if f.ID == id {
			return true
		}
	}
	return false
}

func TestFixtures(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		ids  []string
	}{
		{"safe", 0, nil},
		{"unsafe", 3, []string{"sensitive_slice", "hashed_source_overlap", "unsafe_operation_filter"}},
		{"unsupported", 4, []string{"unsupported_component", "unresolved_interpolation", "unknown_slice_source"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := check(t, fixture(t, tc.name))
			if r.ExitCode() != tc.code {
				t.Fatalf("code %d: %+v", r.ExitCode(), r)
			}
			for _, id := range tc.ids {
				if !hasFinding(r, id) {
					t.Errorf("missing %s", id)
				}
			}
		})
	}
}

func TestRawInputNeverEntersReport(t *testing.T) {
	const secret = "SENTINEL_PRIVATE_8c4a"
	input := strings.ReplaceAll(fixture(t, "unsafe"), "genaisketch", "genaisketch/"+secret)
	input = strings.ReplaceAll(input, "identity_label", secret)
	input += "extensions:\n  " + secret + ": {password: " + secret + "}\n"
	r := check(t, input)
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatal("input leaked")
	}
	if r.ExitCode() != 4 {
		t.Fatal("unknown component must override unsafe status")
	}
}
