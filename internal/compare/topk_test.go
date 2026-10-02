// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func addTopK(t testing.TB, e *summary.Envelope, name string, domain sketchhash.Domain, weights ...int64) string {
	t.Helper()
	f, err := frequentitems.New("micro", domain, sketchhash.HMACSHA25664)
	if err != nil {
		t.Fatal(err)
	}
	for i, weight := range weights {
		if err := f.AddHash(uint64(i+1), weight); err != nil {
			t.Fatal(err)
		}
	}
	data, err := f.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	e.Sketches[name] = summary.Payload{Kind: "frequent_items", Data: data}
	if name == "top_prompts" {
		return ""
	}
	weight := "tokens"
	if strings.HasSuffix(name, "_requests") || strings.HasSuffix(name, ".requests") {
		weight = "requests"
	}
	contract, err := json.Marshal(struct {
		Field  struct{ Domain, FromAttribute string }
		Weight string
	}{Field: struct{ Domain, FromAttribute string }{string(domain), "PRIVATE_ATTRIBUTE_SENTINEL"}, Weight: weight})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contract)
	marker := "topk_contract.v1." + name + "." + hex.EncodeToString(digest[:16])
	e.Counters[marker] = 0
	return marker
}

func question(t testing.TB, r Investigation, id string) Question {
	t.Helper()
	for _, q := range r.Questions {
		if q.ID == id {
			return q
		}
	}
	t.Fatalf("missing question %s", id)
	return Question{}
}

func topKPair(t testing.TB, name string, before, after []int64) ([]summary.Envelope, []summary.Envelope, Options) {
	t.Helper()
	a := fixture(t, minute, fixtureSource{Producer: "app"})
	b := fixture(t, 2*minute, fixtureSource{Producer: "app"})
	a.Counters["requests"], b.Counters["requests"] = 10, 10
	a.Counters["input_tokens"], b.Counters["input_tokens"] = 1000, 1000
	addTopK(t, &a, name, sketchhash.SessionV1, before...)
	addTopK(t, &b, name, sketchhash.SessionV1, after...)
	return []summary.Envelope{a}, []summary.Envelope{b}, Options{Expected: []string{"app"}, Top: 100, ShowHashes: true}
}

func TestTopKKnownNamesAndUnits(t *testing.T) {
	for _, name := range []string{"top_prompts", "top_users", "top_sessions", "top_docs", "top_mcp_sessions", "top_mcp_methods", "top_mcp_resources"} {
		for _, suffix := range []string{"", "_requests"} {
			t.Run(name+suffix, func(t *testing.T) {
				a, b := windows(t)
				for _, docs := range [][]summary.Envelope{a, b} {
					for i := range docs {
						addTopK(t, &docs[i], name+suffix, sketchhash.UserV1, 3, 1)
					}
				}
				r, err := Compare(a, b, options())
				if err != nil {
					t.Fatal(err)
				}
				want := "attributed-reported-tokens"
				if suffix != "" {
					want = "attributed-model-attempts"
				} else if name == "top_prompts" {
					want = "configured-weight"
				}
				found := false
				for _, c := range r.Concentration {
					if c.Name == name+suffix {
						found = true
						if c.WeightUnit != want || c.BeforeWeight != 8 || c.AfterWeight != 8 {
							t.Fatal(c)
						}
					}
				}
				if !found || r.OmittedMeasurements != 0 {
					t.Fatal(r)
				}
				data, _ := json.Marshal(r)
				if strings.Contains(string(data), "topk_contract") || strings.Contains(string(data), "SENTINEL") {
					t.Fatal("metadata reported as quantity or raw extraction rule leaked")
				}
			})
		}
	}
}

func TestTopKIntersectionAcrossAllOriginalSnapshots(t *testing.T) {
	for _, mode := range []string{"old to new", "new to old", "independent producer", "superseded absent", "superseded present", "prompt replaced"} {
		t.Run(mode, func(t *testing.T) {
			a, b := windows(t)
			baseline, err := Compare(a, b, options())
			if err != nil {
				t.Fatal(err)
			}
			for i := range b {
				addTopK(t, &b[i], "top_sessions_requests", sketchhash.SessionV1, 3, 1)
			}
			switch mode {
			case "new to old":
				for i := range a {
					addTopK(t, &a[i], "top_sessions_requests", sketchhash.SessionV1, 3, 1)
					delete(b[i].Sketches, "top_sessions_requests")
					for marker := range b[i].Counters {
						if isTopKMarker(marker) {
							delete(b[i].Counters, marker)
						}
					}
				}
			case "independent producer":
				addTopK(t, &a[0], "top_sessions_requests", sketchhash.SessionV1, 3, 1)
			case "superseded absent", "superseded present":
				old := a[0]
				old.Sketches, old.Counters = maps.Clone(old.Sketches), maps.Clone(old.Counters)
				for i := range a {
					addTopK(t, &a[i], "top_sessions_requests", sketchhash.SessionV1, 3, 1)
				}
				a[0].Sequence = 2
				if mode == "superseded present" {
					addTopK(t, &old, "top_users", sketchhash.UserV1, 4)
				}
				a = append(a, old)
			case "prompt replaced":
				for i := range b {
					delete(b[i].Sketches, "top_prompts")
				}
			}
			original, _ := json.Marshal([][]summary.Envelope{a, b})
			r, err := Investigate(a, b, options())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(r.Evidence.Counters, baseline.Counters) || !reflect.DeepEqual(r.Evidence.Before, baseline.Before) || !reflect.DeepEqual(r.Evidence.After, baseline.After) {
				t.Fatal("base accounting or selected snapshots changed")
			}
			if !slices.Contains(r.Evidence.DroppedMeasurements, "top_sessions_requests") || question(t, r, "sessions").Status != "cannot_determine" {
				t.Fatal(r)
			}
			for _, c := range r.Evidence.Concentration {
				if c.Name == "top_sessions_requests" {
					t.Fatal("synthesized missing attribution")
				}
			}
			if mode == "prompt replaced" && question(t, r, "contributors").Status != "cannot_determine" {
				t.Fatal("missing prompt attribution became zero")
			}
			encoded, _ := json.Marshal([][]summary.Envelope{a, b})
			if !bytes.Equal(original, encoded) {
				t.Fatal("input mutated")
			}
			slices.Reverse(a)
			slices.Reverse(b)
			r2, err := Investigate(a, b, options())
			if err != nil || !reflect.DeepEqual(r, r2) {
				t.Fatal("input order changed projection", err)
			}
		})
	}
}

func TestTopKIncompatibleContractsNeverDisappear(t *testing.T) {
	for _, dropped := range []bool{false, true} {
		for _, change := range []string{"marker", "domain", "profile", "kind", "missing marker", "nonzero marker", "duplicate marker", "uppercase marker", "short marker", "raw marker", "orphan marker"} {
			t.Run(fmt.Sprintf("%s/dropped=%t", change, dropped), func(t *testing.T) {
				a, b := windows(t)
				for _, docs := range [][]summary.Envelope{a, b} {
					for i := range docs {
						addTopK(t, &docs[i], "top_users", sketchhash.UserV1, 8, 2)
					}
				}
				var marker string
				for name := range b[0].Counters {
					if isTopKMarker(name) {
						marker = name
					}
				}
				switch change {
				case "marker":
					delete(b[0].Counters, marker)
					b[0].Counters["topk_contract.v1.top_users."+strings.Repeat("a", 32)] = 0
				case "domain", "profile":
					domain, profile := sketchhash.SessionV1, frequentitems.ProfileMicro
					if change == "profile" {
						domain, profile = sketchhash.UserV1, frequentitems.ProfileSmall
					}
					f, err := frequentitems.New(profile, domain, sketchhash.HMACSHA25664)
					if err != nil {
						t.Fatal(err)
					}
					data, err := f.MarshalBinary()
					if err != nil {
						t.Fatal(err)
					}
					b[0].Sketches["top_users"] = summary.Payload{Kind: "frequent_items", Data: data}
				case "kind":
					b[0].Sketches["top_users"] = b[0].Sketches["distinct_users"]
				case "missing marker":
					delete(b[0].Counters, marker)
				case "nonzero marker":
					b[0].Counters[marker] = 1
				case "duplicate marker":
					b[0].Counters["topk_contract.v1.top_users."+strings.Repeat("b", 32)] = 0
				case "orphan marker":
					delete(b[0].Sketches, "top_users")
				default:
					delete(b[0].Counters, marker)
					suffix := "PRIVATE_SENTINEL"
					if change == "uppercase marker" {
						suffix = strings.Repeat("A", 32)
					}
					if change == "short marker" {
						suffix = "abcd"
					}
					b[0].Counters["topk_contract.v1.top_users."+suffix] = 0
				}
				if dropped {
					delete(a[1].Sketches, "top_users")
					for name := range a[1].Counters {
						if isTopKMarker(name) {
							delete(a[1].Counters, name)
						}
					}
				}
				r, err := Compare(a, b, options())
				if err == nil || !reflect.DeepEqual(r, Report{}) || strings.Contains(err.Error(), "SENTINEL") {
					t.Fatal("bad contract accepted or echoed", err)
				}
			})
		}
	}
}

func TestTopKProjectionPreservesKeptMarkersAndUnknownContracts(t *testing.T) {
	a, b := windows(t)
	for _, docs := range [][]summary.Envelope{a, b} {
		for i := range docs {
			addTopK(t, &docs[i], "top_users", sketchhash.UserV1, 3)
			addTopK(t, &docs[i], "top_key.PRIVATE_SENTINEL.requests", sketchhash.UserV1, 1)
			addTopK(t, &docs[i], "top_key.PRIVATE_SENTINEL_requests.tokens", sketchhash.UserV1, 1)
			docs[i].Counters["PRIVATE_SENTINEL_COUNTER"] = 1
		}
	}
	addTopK(t, &b[0], "top_sessions", sketchhash.SessionV1, 4)
	pa, pb, dropped, err := projectTopK(a, b)
	if err != nil || !reflect.DeepEqual(dropped, []string{"top_sessions"}) {
		t.Fatal(dropped, err)
	}
	if err := summary.Compatible(pa[0], pb[0]); err != nil {
		t.Fatal(err)
	}
	for marker := range pb[0].Counters {
		if strings.HasPrefix(marker, "topk_contract.v1.top_sessions.") {
			t.Fatal("dropped marker retained")
		}
	}
	for _, measurement := range []string{"top_users", "top_key.PRIVATE_SENTINEL.requests", "top_key.PRIVATE_SENTINEL_requests.tokens"} {
		var marker string
		for name := range pb[0].Counters {
			if m, ok := markerMeasurement(name); ok && m == measurement {
				marker = name
			}
		}
		if marker == "" {
			t.Fatal("kept marker removed")
		}
		altered := pb[0]
		altered.Counters = maps.Clone(altered.Counters)
		delete(altered.Counters, marker)
		if err := summary.Compatible(pa[0], altered); err == nil {
			t.Fatal("compatibility no longer refuses missing marker")
		}
	}
	r, err := Compare(a, b, options())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "SENTINEL") {
		t.Fatal("unknown raw measurement echoed")
	}
	for _, mutation := range []string{"unknown sketch", "unknown counter", "unknown marker", "base counter"} {
		changed := slices.Clone(b)
		changed[0].Sketches, changed[0].Counters = maps.Clone(b[0].Sketches), maps.Clone(b[0].Counters)
		switch mutation {
		case "unknown sketch":
			delete(changed[0].Sketches, "top_key.PRIVATE_SENTINEL.requests")
		case "unknown counter":
			delete(changed[0].Counters, "PRIVATE_SENTINEL_COUNTER")
		case "unknown marker":
			for marker := range changed[0].Counters {
				if strings.Contains(marker, "top_key.") {
					delete(changed[0].Counters, marker)
				}
			}
		case "base counter":
			delete(changed[0].Counters, "requests")
		}
		if _, err := Compare(a, changed, options()); err == nil {
			t.Fatal("unknown/base contract relaxed", mutation)
		}
	}
}

func TestTopKOriginalValidationPrecedesProjection(t *testing.T) {
	for _, change := range []string{"inconsistent zero", "inconsistent high", "bad payload", "bad identifier", "bad counter", "counter count", "sketch count", "conflicting replay", "oversized payload"} {
		t.Run(change, func(t *testing.T) {
			a, b := windows(t)
			addTopK(t, &a[0], "top_sessions", sketchhash.SessionV1, 5, 5)
			p := a[0].Sketches["top_sessions"]
			switch change {
			case "inconsistent zero":
				p.Data = rewriteFIWeight(t, p.Data, 0)
			case "inconsistent high":
				p.Data = rewriteFIWeight(t, p.Data, 20)
			case "bad payload":
				p.Data = []byte("RAW_SENTINEL")
			case "bad identifier":
				a[0].KeyID = "/RAW_SENTINEL"
			case "bad counter":
				a[0].Counters["RAW_SENTINEL"] = math.MaxUint64
			case "counter count":
				for i := 0; i < 129; i++ {
					a[0].Counters[fmt.Sprintf("extra%d", i)] = 0
				}
			case "sketch count":
				for i := 0; i < 17; i++ {
					a[0].Sketches[fmt.Sprintf("extra%d", i)] = p
				}
			case "conflicting replay":
				duplicate := a[0]
				duplicate.Sketches = maps.Clone(duplicate.Sketches)
				addTopK(t, &duplicate, "top_sessions", sketchhash.SessionV1, 6, 5)
				a = append(a, duplicate)
			case "oversized payload":
				p.Data = make([]byte, MaxInputBytes+1)
			}
			a[0].Sketches["top_sessions"] = p
			r, err := Compare(a, b, options())
			if err == nil || !reflect.DeepEqual(r, Report{}) || strings.Contains(err.Error(), "SENTINEL") {
				t.Fatal("invalid dropped input accepted or echoed", err)
			}
		})
	}
}

func TestTopKUserDeltaAndAttributedShares(t *testing.T) {
	a, b, o := topKPair(t, "top_users", []int64{20, 80}, []int64{70, 30})
	r, err := Investigate(a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	q := question(t, r, "users")
	if q.Status != "observed" || len(q.Contributors) != 2 || !strings.Contains(q.Answer, "excluding activity without a key") {
		t.Fatal(q)
	}
	for _, c := range q.Contributors {
		if c.Before == nil || c.After == nil || c.Delta == nil || c.Flag != "" {
			t.Fatal(c)
		}
		if c.Hash == "0000000000000001" && (*c.Delta != (Interval{50, 50}) || c.BeforeShare.Lower != .2 || c.AfterShare.Lower != .7) {
			t.Fatal(c)
		}
	}
}

func TestTopKSessionFlagUsesStrictAfterLowerShare(t *testing.T) {
	for _, tc := range []struct {
		name      string
		after     []int64
		threshold float64
		explicit  bool
		flagged   bool
	}{
		{"default above", []int64{26, 74}, 0, false, true},
		{"default equal", []int64{25, 75}, 0, false, false},
		{"below", []int64{24, 76}, 0, false, false},
		{"custom equal", []int64{50, 50}, .5, true, false},
		{"custom above", []int64{51, 49}, .5, true, true},
		{"decimal below", []int64{29, 71}, .3, true, false},
		{"decimal equal", []int64{30, 70}, .3, true, false},
		{"decimal above", []int64{31, 69}, .3, true, true},
		{"explicit zero", []int64{1, 99}, 0, true, true},
		{"one", []int64{100}, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, o := topKPair(t, "top_sessions", []int64{99, 1}, tc.after)
			o.FlagShare, o.FlagShareSet = tc.threshold, tc.explicit
			r, err := Investigate(a, b, o)
			if err != nil {
				t.Fatal(err)
			}
			q := question(t, r, "sessions")
			if q.Status != "observed" {
				t.Fatal(q)
			}
			found := false
			for _, c := range q.Contributors {
				if c.Hash == "0000000000000001" {
					found = true
					if (c.Flag == "runaway_candidate") != tc.flagged {
						t.Fatal(c)
					}
				}
			}
			if !found {
				t.Fatal("missing tracked candidate")
			}
		})
	}
	weights := make([]int64, 2000)
	for i := range weights {
		weights[i] = 1
	}
	weights = append(weights, 1000)
	prior := slices.Clone(weights)
	prior[len(prior)-1] = 500
	a, b, o := topKPair(t, "top_sessions", prior, weights)
	f, err := frequentitems.Parse(b[0].Sketches["top_sessions"].Data)
	if err != nil {
		t.Fatal(err)
	}
	lower, upper := f.LowerBoundHash(2001), f.UpperBoundHash(2001)
	if lower == upper {
		t.Fatal("fixture has no uncertainty")
	}
	o.FlagShare = float64(lower+upper) / 2 / float64(f.TotalWeight())
	r, err := Investigate(a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range question(t, r, "sessions").Contributors {
		if c.Hash == fmt.Sprintf("%016x", 2001) {
			if c.Flag != "" || c.AfterShare.Lower >= o.FlagShare || c.AfterShare.Upper <= o.FlagShare {
				t.Fatal("estimate used instead of lower bound", c)
			}
			return
		}
	}
	t.Fatal("uncertain candidate missing")
}

func TestTopKPartialAndMissingUsage(t *testing.T) {
	for _, name := range []string{"top_sessions", "top_sessions_requests"} {
		for _, state := range []string{"missing usage", "partial", "missing producer", "zero attribution", "no usage counters"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				a, b, o := topKPair(t, name, []int64{1, 1}, []int64{8, 2})
				o.AllowPartial = true
				switch state {
				case "missing usage":
					b[0].Counters["missing_token_usage"] = 4
				case "partial":
					b[0].ObservedEnd--
				case "missing producer":
					o.Expected = append(o.Expected, "offline")
				case "zero attribution":
					for _, docs := range [][]summary.Envelope{a, b} {
						addTopK(t, &docs[0], name, sketchhash.SessionV1)
					}
				case "no usage counters":
					for _, docs := range [][]summary.Envelope{a, b} {
						delete(docs[0].Counters, "input_tokens")
					}
				}
				r, err := Investigate(a, b, o)
				if err != nil {
					t.Fatal(err)
				}
				q := question(t, r, "sessions")
				want := "limited"
				if name == "top_sessions_requests" && (state == "missing usage" || state == "no usage counters") {
					want = "observed"
				}
				if state == "zero attribution" {
					want = "cannot_determine"
				}
				if q.Status != want {
					t.Fatal(q)
				}
				flagged := false
				for _, c := range q.Contributors {
					flagged = flagged || c.Flag == "runaway_candidate"
				}
				if flagged != (want == "observed") {
					t.Fatal("flag disagrees with coverage", q)
				}
				if want == "observed" && (!strings.Contains(q.Answer, "model attempts") || !strings.Contains(q.Answer, "attributed tokens")) {
					t.Fatal(q.Answer)
				}
			})
		}
	}
}

func TestSessionFlagExactNearInt64Limit(t *testing.T) {
	for _, tc := range []struct {
		name         string
		lower, total int64
		threshold    float64
		flagged      bool
	}{
		{"rounded above but below decimal threshold", 2767011611056432486, 9223372036854775295, .3, false},
		{"above binary value but below decimal threshold", 2767011611056432487, 9223372036854775295, .3, false},
		{"immediately below decimal threshold", 2767011611056432588, 9223372036854775295, .3, false},
		{"immediately above decimal threshold", 2767011611056432589, 9223372036854775295, .3, true},
		{"exactly equal decimal threshold", 2767011611056432740, 9223372036854775800, .3, false},
		{"rounded equal but exactly above", 2305843009213693952, math.MaxInt64, .25, true},
		{"exactly equal", 2305843009213693951, math.MaxInt64 - 3, .25, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, o := topKPair(t, "top_sessions", []int64{1, 1}, []int64{tc.lower, tc.total - tc.lower})
			a[0].Counters["input_tokens"], b[0].Counters["input_tokens"] = 2, uint64(tc.total)
			o.FlagShare = tc.threshold
			r, err := Investigate(a, b, o)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range question(t, r, "sessions").Contributors {
				if c.Hash == "0000000000000001" {
					if (c.Flag == "runaway_candidate") != tc.flagged {
						t.Fatalf("flag crossed exact threshold: %+v", c)
					}
					if c.AfterShare.Lower != float64(tc.lower)/float64(tc.total) {
						t.Fatal("display ratio changed")
					}
					return
				}
			}
			t.Fatal("missing boundary candidate")
		})
	}
}

func TestFlagShareValidation(t *testing.T) {
	if got, err := flagShare(Options{}); err != nil || got != .25 {
		t.Fatal("zero options lost default")
	}
	a, b := windows(t)
	for _, value := range []float64{-.1, 1.1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		o := options()
		o.FlagShare = value
		if _, err := Compare(a, b, o); err == nil {
			t.Fatal("invalid threshold accepted")
		}
		if _, err := Investigate(a, b, o); err == nil {
			t.Fatal("invalid investigation threshold accepted")
		}
	}
}
