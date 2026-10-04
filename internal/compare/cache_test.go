// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"encoding/json"
	"maps"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func cacheWindows(t *testing.T) ([]summary.Envelope, []summary.Envelope, Options) {
	t.Helper()
	a, err := ReadWindow("../../examples/cache/data/before", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReadWindow("../../examples/cache/data/after", nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, b, Options{Expected: []string{"app"}, Top: 20}
}

func cacheAnswer(t *testing.T, a, b []summary.Envelope, o Options) (Question, Report) {
	t.Helper()
	prior, _ := json.Marshal([][]summary.Envelope{a, b})
	r, err := Investigate(a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal([][]summary.Envelope{a, b})
	if string(prior) != string(after) {
		t.Fatal("investigation mutated snapshots")
	}
	for i, id := range []string{"volume", "contributors", "sessions", "coverage", "users", "usage_source", "cache"} {
		if len(r.Questions) <= i || r.Questions[i].ID != id {
			t.Fatal("investigation question indexes changed", r.Questions)
		}
	}
	return r.Questions[6], r.Evidence
}

func TestCacheShareChanges(t *testing.T) {
	for _, tc := range []struct {
		name, direction string
		before, after   uint64
		delta           float64
	}{
		{"drop", "decreased", 600, 200, -40},
		{"improvement", "increased", 200, 600, 40},
		{"unchanged", "unchanged", 600, 600, 0},
		{"explicit zero", "decreased", 600, 0, -60},
		{"both zero", "unchanged", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, o := cacheWindows(t)
			a[0].Counters["cache_read_input_tokens"], b[0].Counters["cache_read_input_tokens"] = tc.before, tc.after
			q, _ := cacheAnswer(t, a, b, o)
			c := q.Cache
			if q.Question != "Did caching get worse?" || q.Status != "observed" || c == nil {
				t.Fatal(q)
			}
			if c.BeforeInputTokens != 1000 || c.AfterInputTokens != 1000 ||
				c.BeforeCacheReadInputTokens != tc.before || c.AfterCacheReadInputTokens != tc.after ||
				c.BeforeShare != float64(tc.before)/1000 || c.AfterShare != float64(tc.after)/1000 ||
				c.DeltaPercentagePoints != tc.delta || c.Direction != tc.direction {
				t.Fatalf("incorrect cache share: %+v", c)
			}
		})
	}
}

func TestCacheCannotDetermine(t *testing.T) {
	cases := []string{"zero denominator", "older", "missing requests", "missing input", "missing cache", "missing input report", "optional detail absent", "partial interval", "missing producer", "no attempts", "impossible empty snapshot", "aggregate subset"}
	for _, field := range []string{"input", "cache_read_input"} {
		for _, state := range []string{"reported", "missing", "invalid", "conflict", "subset_violation"} {
			cases = append(cases, "absent:token_observations."+field+"."+state)
			if state != "reported" {
				cases = append(cases, "nonzero:token_observations."+field+"."+state)
			}
		}
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			a, b, o := cacheWindows(t)
			o.AllowPartial = true
			switch {
			case strings.HasPrefix(name, "absent:"):
				key := strings.TrimPrefix(name, "absent:")
				delete(a[0].Counters, key)
				delete(b[0].Counters, key)
			case strings.HasPrefix(name, "nonzero:"):
				b[0].Counters[strings.TrimPrefix(name, "nonzero:")] = 1
			default:
				switch name {
				case "zero denominator":
					b[0].Counters["input_tokens"], b[0].Counters["cache_read_input_tokens"] = 0, 0
				case "older":
					for _, window := range [][]summary.Envelope{a, b} {
						for key := range window[0].Counters {
							if strings.HasPrefix(key, "token_observations.") {
								delete(window[0].Counters, key)
							}
						}
					}
				case "missing requests", "missing input", "missing cache":
					key := map[string]string{"missing requests": "requests", "missing input": "input_tokens", "missing cache": "cache_read_input_tokens"}[name]
					delete(a[0].Counters, key)
					delete(b[0].Counters, key)
				case "missing input report":
					b[0].Counters["token_observations.input.reported"] = 9
				case "optional detail absent":
					b[0].Counters["token_observations.cache_read_input.reported"] = 9
				case "partial interval":
					b[0].ObservedEnd--
				case "missing producer":
					o.Expected = append(o.Expected, "missing")
				case "no attempts", "impossible empty snapshot":
					b[0].Counters["requests"] = 0
					b[0].Counters["token_observations.input.reported"] = 0
					b[0].Counters["token_observations.cache_read_input.reported"] = 0
					if name == "no attempts" {
						b[0].Counters["input_tokens"], b[0].Counters["cache_read_input_tokens"] = 0, 0
					}
				case "aggregate subset":
					b[0].Counters["cache_read_input_tokens"] = 1001
				}
			}
			q, _ := cacheAnswer(t, a, b, o)
			if q.Status != "cannot_determine" || q.Cache != nil || q.Answer == "" {
				t.Fatal("unknown or invalid usage became a share", q)
			}
		})
	}
}

func TestCachePerSnapshotQuality(t *testing.T) {
	for _, name := range []string{"complete", "opposing input reports", "opposing cache reports", "masked subset", "masked subset violation", "partial interval", "missing producer"} {
		t.Run(name, func(t *testing.T) {
			a, b, o := cacheWindows(t)
			for _, window := range []*[]summary.Envelope{&a, &b} {
				doc := (*window)[0]
				doc.ProducerID, doc.Counters = "other", maps.Clone(doc.Counters)
				*window = append(*window, doc)
			}
			o.Expected, o.AllowPartial = []string{"app", "other"}, true
			switch name {
			case "opposing input reports":
				b[0].Counters["token_observations.input.reported"], b[1].Counters["token_observations.input.reported"] = 9, 11
			case "opposing cache reports":
				b[0].Counters["token_observations.cache_read_input.reported"], b[1].Counters["token_observations.cache_read_input.reported"] = 9, 11
			case "masked subset":
				b[0].Counters["cache_read_input_tokens"] = 1001
			case "masked subset violation":
				// Aggregate 400 <= 2000 says nothing about individual attempts.
				b[0].Counters["token_observations.cache_read_input.subset_violation"] = 1
			case "partial interval":
				b[1].ObservedEnd--
			case "missing producer":
				b = b[:1]
			}
			q, _ := cacheAnswer(t, a, b, o)
			if name == "complete" {
				if q.Cache == nil || q.Cache.BeforeInputTokens != 2000 || q.Cache.AfterCacheReadInputTokens != 400 || q.Cache.DeltaPercentagePoints != -40 {
					t.Fatal("producer aggregation changed share", q)
				}
			} else if q.Status != "cannot_determine" || q.Cache != nil {
				t.Fatal("producer summation masked invalid or incomplete observations", q)
			}
		})
	}
}

func TestCacheIndependentOfOutputAndProviderOrigin(t *testing.T) {
	for _, name := range []string{"unknown origin", "missing output", "no output counters", "tool failure", "inferred origin"} {
		t.Run(name, func(t *testing.T) {
			a, b, o := cacheWindows(t)
			for _, window := range [][]summary.Envelope{a, b} {
				switch name {
				case "missing output":
					window[0].Counters["output_tokens"] = 0
					window[0].Counters["missing_token_usage"] = 10
				case "no output counters":
					delete(window[0].Counters, "output_tokens")
					delete(window[0].Counters, "missing_token_usage")
				case "tool failure":
					window[0].Counters["tool_errors"] = 10
				case "inferred origin":
					for _, field := range []string{"input", "output"} {
						for _, source := range []string{"provider_reported", "inferred", "unavailable", "unknown"} {
							window[0].Counters["usage_provenance.v1."+field+"."+source] = 0
						}
						window[0].Counters["usage_provenance.v1."+field+".inferred"] = 10
					}
				}
			}
			q, _ := cacheAnswer(t, a, b, o)
			if q.Status != "observed" || q.Cache == nil || !strings.Contains(q.Answer, "recorded") || !strings.Contains(q.Answer, "not a request hit rate") || !strings.Contains(q.Answer, "billing") {
				t.Fatal("cache requires only relevant input coverage and must limit its claims", q)
			}
		})
	}
}

func TestCacheReplayAndIntegerPrecision(t *testing.T) {
	a, b, o := cacheWindows(t)
	want, _ := cacheAnswer(t, a, b, o)
	a = append(a, a[0])
	b = append(b, b[0])
	got, _ := cacheAnswer(t, a, b, o)
	if !reflect.DeepEqual(want, got) {
		t.Fatal("duplicate snapshots changed cache share")
	}
	a, b, o = cacheWindows(t)
	a[0].Counters["input_tokens"], b[0].Counters["input_tokens"] = math.MaxInt64, math.MaxInt64
	a[0].Counters["cache_read_input_tokens"], b[0].Counters["cache_read_input_tokens"] = math.MaxInt64, math.MaxInt64-1
	q, _ := cacheAnswer(t, a, b, o)
	if q.Cache == nil || q.Cache.Direction != "decreased" || q.Cache.DeltaPercentagePoints >= 0 {
		t.Fatal("float rounding hid a real decrease", q)
	}
}

func TestCacheEvidencePrivacy(t *testing.T) {
	a, b, o := cacheWindows(t)
	for _, window := range [][]summary.Envelope{a, b} {
		window[0].ProducerID, window[0].Epoch, window[0].ScopeID = "PRIVATE_SENTINEL", "PRIVATE_SENTINEL", "PRIVATE_SENTINEL"
		for _, key := range []string{"PRIVATE_SENTINEL", "token_observations.PRIVATE_SENTINEL.reported", "token_observations.input.PRIVATE_SENTINEL", "token_observations.cache_read_input.reported.PRIVATE_SENTINEL"} {
			window[0].Counters[key] = 1
		}
	}
	o.Expected = []string{"PRIVATE_SENTINEL"}
	r, err := Investigate(a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(r)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_SENTINEL") || strings.Contains(string(encoded), `"hash"`) {
		t.Fatal("private metadata or arbitrary counter names escaped")
	}
	quality := 0
	for _, c := range r.Evidence.Counters {
		if strings.HasPrefix(c.Name, "token_observations.") {
			quality++
			if c.Unit != "observations" {
				t.Fatal("wrong quality counter unit", c)
			}
		}
	}
	if quality != 10 || r.Evidence.OmittedMeasurements != 4 {
		t.Fatal("quality evidence must use the exact reviewed allowlist", r.Evidence)
	}
	// The same unknown names must remain private in validation diagnostics.
	b[0].Counters["PRIVATE_SENTINEL"] = math.MaxUint64
	if _, err := Investigate(a, b, o); err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
		t.Fatal("invalid counter was accepted or disclosed", err)
	}
}

func TestCacheUsesWindowTotalsAndLatestSnapshots(t *testing.T) {
	a, b, o := cacheWindows(t)
	// A smaller earlier cumulative snapshot is selected out, not added again.
	earlier := a[0]
	earlier.Counters = maps.Clone(a[0].Counters)
	for key, value := range earlier.Counters {
		earlier.Counters[key] = value / 2
	}
	earlier.ObservedEnd -= minute / 2
	a[0].Sequence = 2
	a = append(a, earlier)
	q, r := cacheAnswer(t, a, b, o)
	if q.Cache == nil || q.Cache.BeforeInputTokens != 1000 || q.Cache.BeforeShare != .6 || r.Before.SelectedSnapshots != 1 {
		t.Fatal("cumulative snapshot was double counted", q, r.Before)
	}
	a, b, o = cacheWindows(t)
	for _, window := range []*[]summary.Envelope{&a, &b} {
		doc := (*window)[0]
		doc.ProducerID, doc.Counters = "other", maps.Clone(doc.Counters)
		doc.Counters["input_tokens"], doc.Counters["cache_read_input_tokens"] = 9000, 9000
		*window = append(*window, doc)
	}
	o.Expected = append(o.Expected, "other")
	q, _ = cacheAnswer(t, a, b, o)
	if q.Cache == nil || q.Cache.BeforeShare != .96 || q.Cache.AfterShare != .92 || q.Cache.DeltaPercentagePoints != -4 {
		t.Fatal("used per-producer average instead of per-window token ratio", q)
	}
}

func TestCacheQualityOnBeforeAndSupersededSnapshots(t *testing.T) {
	for _, superseded := range []bool{false, true} {
		a, b, o := cacheWindows(t)
		if superseded {
			earlier := a[0]
			earlier.Counters = maps.Clone(a[0].Counters)
			earlier.Counters["token_observations.cache_read_input.reported"] = 9
			a[0].Sequence = 2
			a = append(a, earlier)
		} else {
			a[0].Counters["token_observations.input.invalid"] = 1
		}
		q, _ := cacheAnswer(t, a, b, o)
		if q.Status != "cannot_determine" || q.Cache != nil {
			t.Fatal("only after/selected snapshot quality was checked", q)
		}
	}
}

func TestCacheCountersMustBeTypedAndExactlyNamed(t *testing.T) {
	a, b, o := cacheWindows(t)
	data, err := a[0].MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"10"`, `-1`, `1.5`, `null`, `true`} {
		invalid := strings.Replace(string(data), `"token_observations.input.reported":10`, `"token_observations.input.reported":`+value, 1)
		if _, err := summary.Parse([]byte(invalid)); err == nil {
			t.Fatalf("accepted incorrectly typed quality counter: %s", value)
		}
	}
	for _, window := range [][]summary.Envelope{a, b} {
		delete(window[0].Counters, "token_observations.cache_read_input.invalid")
		window[0].Counters["token_observations.cache_read_input.invalid.PRIVATE_SENTINEL"] = 0
	}
	q, r := cacheAnswer(t, a, b, o)
	data, err = json.Marshal(r)
	if err != nil || q.Cache != nil || q.Status != "cannot_determine" || strings.Contains(string(data), "PRIVATE_SENTINEL") {
		t.Fatal("unknown quality name supplied a false zero or leaked", q)
	}
}
