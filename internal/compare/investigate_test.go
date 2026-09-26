// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestInvestigationAnswers(t *testing.T) {
	for _, tc := range []struct {
		name              string
		requests          uint64
		tokens            uint64
		volume, intensity float64
	}{
		{"more requests", 20, 2000, 1000, 0},
		{"larger requests", 10, 2000, 0, 1000},
		{"both", 20, 4000, 1500, 1500},
		{"offsetting", 20, 1000, 750, -750},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := fixture(t, minute, fixtureSource{Producer: "app", Users: []uint64{1}, Prompts: []uint64{2}, Weights: []int64{1000}})
			b := fixture(t, 2*minute, fixtureSource{Producer: "app", Users: []uint64{1}, Prompts: []uint64{2}, Weights: []int64{int64(tc.tokens)}})
			a.Counters["requests"], b.Counters["requests"] = 10, tc.requests
			r, err := Investigate([]summary.Envelope{a}, []summary.Envelope{b}, Options{Expected: []string{"app"}, Top: 20})
			if err != nil {
				t.Fatal(err)
			}
			q := r.Questions[0]
			if q.Status != "observed" || q.Volume == nil {
				t.Fatal(q)
			}
			v := q.Volume
			if v.RequestContribution != tc.volume || v.TokensPerRequestContribution != tc.intensity || math.Abs(v.RequestContribution+v.TokensPerRequestContribution-(float64(tc.tokens)-1000)) > 1e-8 {
				t.Fatal(v)
			}
			if r.Questions[1].Status != "observed" || r.Questions[2].Status != "cannot_determine" || r.Questions[3].Status != "observed" {
				t.Fatal(r.Questions)
			}
			if len(r.Questions[1].Contributors) != 1 || r.Questions[1].Contributors[0].AfterShare.Lower != 1 {
				t.Fatal(r.Questions[1])
			}
		})
	}
}

func TestInvestigationUnknownIsNotZero(t *testing.T) {
	for _, tc := range []string{"missing usage", "missing counter", "zero requests", "partial interval", "missing producer", "no prompt sketch", "zero weight"} {
		t.Run(tc, func(t *testing.T) {
			a, b := windows(t)
			o := options()
			o.AllowPartial = true
			switch tc {
			case "missing usage":
				b[0].Counters["missing_token_usage"] = 1
			case "missing counter":
				for _, docs := range [][]summary.Envelope{a, b} {
					for i := range docs {
						delete(docs[i].Counters, "output_tokens")
					}
				}
			case "zero requests":
				for i := range a {
					a[i].Counters["requests"] = 0
				}
			case "partial interval":
				b[0].ObservedEnd--
			case "missing producer":
				b = b[:1]
			case "no prompt sketch":
				for _, docs := range [][]summary.Envelope{a, b} {
					for i := range docs {
						delete(docs[i].Sketches, "top_prompts")
					}
				}
			case "zero weight":
				a = []summary.Envelope{fixture(t, minute, fixtureSource{Producer: "hosted"})}
				b = []summary.Envelope{fixture(t, 2*minute, fixtureSource{Producer: "hosted"})}
				o.Expected = []string{"hosted"}
			}
			r, err := Investigate(a, b, o)
			if err != nil {
				t.Fatal(err)
			}
			index := 0
			if tc == "no prompt sketch" || tc == "zero weight" {
				index = 1
			}
			if r.Questions[index].Status != "cannot_determine" || r.Questions[index].Volume != nil {
				t.Fatal(r.Questions[index])
			}
			if tc == "missing usage" || tc == "partial interval" || tc == "missing producer" {
				if r.Questions[3].Status != "limited" {
					t.Fatal(r.Questions[3])
				}
			}
		})
	}
}

func TestInvestigationValidationAndPrivacy(t *testing.T) {
	a, b := windows(t)
	if _, err := Investigate(a, a, options()); err == nil {
		t.Fatal("overlap accepted")
	}
	for _, docs := range [][]summary.Envelope{a, b} {
		for i := range docs {
			docs[i].ScopeID = "PRIVATE_SENTINEL"
		}
	}
	o := options()
	o.ShowHashes = false
	r, err := Investigate(a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE_SENTINEL") || strings.Contains(string(data), `"hash"`) {
		t.Fatal("private data in answers")
	}
}
