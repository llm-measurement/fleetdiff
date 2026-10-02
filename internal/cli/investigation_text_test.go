// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestInvestigationTextPreservesJSONAndUncertainty(t *testing.T) {
	r := compare.Investigation{Version: 1, Questions: []compare.Question{
		{ID: "sessions", Question: "Which sessions need investigation?", Status: "limited", Contributors: []compare.Contributor{
			{Item: "item-1", Measurement: "top_sessions", Before: &compare.Interval{Lower: 1, Upper: 1}, After: &compare.Interval{Lower: 2, Upper: 4}, AfterShare: &compare.Share{Lower: .2, Upper: .4}},
			{Item: "item-1", Measurement: "top_sessions_requests", Before: &compare.Interval{Lower: 0, Upper: 0}, After: &compare.Interval{Lower: 3, Upper: 3}},
		}},
		{ID: "users", Status: "cannot_determine", Answer: "Add user-weight sketches."},
		{ID: "usage_source", Status: "cannot_determine", Answer: "Provider origin is unknown."},
	}}
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	renderInvestigation(&out, r, .4)
	after, err := json.Marshal(r)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("text rendering changed the JSON contract")
	}
	for _, want := range []string{"(partial coverage)", "1 -> [2, 4]", "[20.00%, 40.00%]", "0 -> 3", "unknown", "top_sessions_requests", "top_sessions", "user_key topk_keys", "enable usage provenance"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "flagged for review") || strings.Contains(out.String(), "cannot_determine") || strings.Count(out.String(), "More answers with more data:") != 1 {
		t.Fatal("unexpected flag or repeated unknown answers", out.String())
	}
}
