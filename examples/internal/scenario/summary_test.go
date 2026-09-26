// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scenario

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func TestSyntheticSummaryAnswersAndPrivacy(t *testing.T) {
	t.Setenv("FLEETDIFF_TEST_SECRET", "public-research-test-key-not-for-production")
	secret, err := sketchhash.SecretFromEnv("FLEETDIFF_TEST_SECRET")
	if err != nil {
		t.Fatal(err)
	}
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	checkPrivate := func(data []byte) {
		t.Helper()
		for _, value := range []string{"FC_PRIVATE_", "web_search", "fetch_page", `"timeout"`, `"not_found"`, `"tool_error"`} {
			if bytes.Contains(data, []byte(value)) {
				t.Fatal("raw fixture field in a public output")
			}
		}
	}
	var windows [2][]summary.Envelope
	for i, w := range s.Windows {
		for _, producer := range []string{"owned", "partner"} {
			e, err := s.Summary(w, producer, int64(i+1)*15_000_000_000, secret)
			if err != nil {
				t.Fatal(err)
			}
			data, err := e.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			checkPrivate(data)
			parsed, err := summary.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, payload := range parsed.Sketches {
				checkPrivate(payload.Data)
			}
			windows[i] = append(windows[i], parsed)
		}
	}
	for _, showHashes := range []bool{false, true} {
		r, err := compare.Compare(windows[0], windows[1], compare.Options{Expected: []string{"owned", "partner"}, Top: 10, ShowHashes: showHashes})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CheckReport(r); err != nil {
			t.Fatal(err)
		}
		if showHashes {
			if err := s.CheckSignatures(r, secret); err != nil {
				t.Fatal(err)
			}
		} else {
			for _, c := range r.Concentration {
				for _, m := range c.Movers {
					if m.Hash != "" {
						t.Fatal("default report exposes candidate hash")
					}
				}
			}
		}
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		checkPrivate(data)
	}
}
