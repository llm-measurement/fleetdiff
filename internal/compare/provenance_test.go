// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestInvestigationUsageSource(t *testing.T) {
	for _, tc := range []struct{ name, status string }{
		{"provider_reported", "observed"}, {"inferred", "limited"}, {"unavailable", "limited"},
		{"unknown", "cannot_determine"}, {"legacy", "cannot_determine"}, {"inconsistent", "cannot_determine"},
		{"missing counter", "cannot_determine"}, {"partial", "limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := windows(t)
			for _, docs := range [][]summary.Envelope{a, b} {
				for i := range docs {
					if tc.name == "legacy" {
						continue
					}
					for _, field := range []string{"input", "output"} {
						for _, source := range []string{"provider_reported", "inferred", "unavailable", "unknown"} {
							docs[i].Counters["usage_provenance.v1."+field+"."+source] = 0
						}
						state := tc.name
						if tc.name == "inconsistent" || tc.name == "missing counter" || tc.name == "partial" {
							state = "provider_reported"
						}
						docs[i].Counters["usage_provenance.v1."+field+"."+state] = docs[i].Counters["requests"]
						if tc.name == "inconsistent" {
							docs[i].Counters["usage_provenance.v1."+field+".unknown"] = 1
						}
						if tc.name == "missing counter" {
							delete(docs[i].Counters, "usage_provenance.v1."+field+".unknown")
						}
					}
				}
			}
			o := options()
			if tc.name == "partial" {
				b[0].ObservedEnd--
				o.AllowPartial = true
			}
			r, err := Investigate(a, b, o)
			if err != nil {
				t.Fatal(err)
			}
			q := r.Questions[len(r.Questions)-1]
			if q.ID != "usage_source" || q.Status != tc.status {
				t.Fatal(q)
			}
			if tc.status != "observed" && r.Questions[0].Volume != nil && !strings.Contains(r.Questions[0].Answer, "Provider origin is not established") {
				t.Fatal("volume lacks provenance warning")
			}
		})
	}
}

func TestLegacyProvenanceCompatibility(t *testing.T) {
	a, b := windows(t)
	original, err := a[0].MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	b, err = withUsageProvenance(b)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		for _, field := range []string{"input", "output"} {
			b[i].Counters["usage_provenance.v1."+field+".unknown"] = 0
			b[i].Counters["usage_provenance.v1."+field+".provider_reported"] = b[i].Counters["requests"]
		}
	}
	r, err := Investigate(a, b, options())
	if err != nil {
		t.Fatal(err)
	}
	if q := r.Questions[len(r.Questions)-1]; q.Status != "cannot_determine" {
		t.Fatal(q)
	}
	got, err := a[0].MarshalBinary()
	if err != nil || !reflect.DeepEqual(original, got) {
		t.Fatal("input mutated")
	}
	// Old and new producers in one window also retain strict identity checks.
	if len(a) < 2 {
		t.Fatal("fixture needs two producers")
	}
	mixed, err := withUsageProvenance(a[1:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compare(append(a[:1:1], mixed...), b, options()); err != nil {
		t.Fatal(err)
	}
	b[0].AccountingID = "different-contract"
	if _, err := Compare(a, b, options()); err == nil {
		t.Fatal("accounting mismatch accepted")
	}
}
