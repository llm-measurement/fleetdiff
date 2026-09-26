// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const minute = int64(60_000_000_000)

type fixtureSource struct {
	Producer string
	Users    []uint64
	Prompts  []uint64
	Weights  []int64
}

func fixture(t testing.TB, start int64, source fixtureSource) summary.Envelope {
	t.Helper()
	h, err := hllpp.New("micro", sketchhash.UserV1, sketchhash.HMACSHA25664)
	if err != nil {
		t.Fatal(err)
	}
	f, err := frequentitems.New("micro", sketchhash.PromptV1, sketchhash.HMACSHA25664)
	if err != nil {
		t.Fatal(err)
	}
	var total uint64
	for i, user := range source.Users {
		h.AddHash(user * 0x9e3779b97f4a7c15)
		if err := f.AddHash(source.Prompts[i], source.Weights[i]); err != nil {
			t.Fatal(err)
		}
		total += uint64(source.Weights[i])
	}
	hb, err := h.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	fb, err := f.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return summary.Envelope{
		Version: 1, ProducerID: source.Producer, Epoch: "epoch-1", Sequence: 1,
		ScopeID: "demo", AccountingID: "synthetic-v1", KeyID: "demo-key-v1",
		WindowStart: start, WindowDuration: minute, ObservedStart: start,
		ObservedEnd: start + minute, EmittedAt: start + minute,
		Counters: map[string]uint64{"requests": uint64(len(source.Users)), "input_tokens": total, "output_tokens": 0, "missing_token_usage": 0},
		Sketches: map[string]summary.Payload{"distinct_users": {Kind: "hllpp", Data: hb}, "top_prompts": {Kind: "frequent_items", Data: fb}},
	}
}

func windows(t *testing.T) ([]summary.Envelope, []summary.Envelope) {
	t.Helper()
	data, err := os.ReadFile("testdata/windows.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Before, After []fixtureSource }
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	build := func(start int64, sources []fixtureSource) []summary.Envelope {
		var result []summary.Envelope
		for _, s := range sources {
			result = append(result, fixture(t, start, s))
		}
		return result
	}
	return build(minute, v.Before), build(2*minute, v.After)
}

func options() Options {
	return Options{Expected: []string{"hosted", "selfhosted"}, Top: 20, ShowHashes: true}
}

func TestIndependentSystemsComparison(t *testing.T) {
	data, err := os.ReadFile("testdata/windows.json")
	if err != nil {
		t.Fatal(err)
	}
	var truth struct {
		Expected struct {
			BeforeRequests uint64           `json:"before_requests"`
			AfterRequests  uint64           `json:"after_requests"`
			BeforeTokens   uint64           `json:"before_tokens"`
			AfterTokens    uint64           `json:"after_tokens"`
			BeforeUsers    float64          `json:"before_distinct_users"`
			AfterUsers     float64          `json:"after_distinct_users"`
			Deltas         map[string]int64 `json:"prompt_deltas"`
		}
	}
	if err := json.Unmarshal(data, &truth); err != nil {
		t.Fatal(err)
	}
	a, b := windows(t)
	before, _ := json.Marshal(a)
	r, err := Compare(a, b, options())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Counters {
		if c.Name == "requests" && (c.Before != truth.Expected.BeforeRequests || c.After != truth.Expected.AfterRequests || c.Delta != 0) {
			t.Fatal(c)
		}
		if c.Name == "input_tokens" && (c.Before != truth.Expected.BeforeTokens || c.After != truth.Expected.AfterTokens || c.Delta != int64(truth.Expected.AfterTokens)-int64(truth.Expected.BeforeTokens)) {
			t.Fatal(c)
		}
	}
	if len(r.Distinct) != 1 || math.Abs(r.Distinct[0].Before.Estimate-truth.Expected.BeforeUsers) > .1 || math.Abs(r.Distinct[0].After.Estimate-truth.Expected.AfterUsers) > .1 {
		t.Fatalf("distinct: %+v", r.Distinct)
	}
	if r.Distinct[0].Before.NominalRSE <= 0 {
		t.Fatal("missing uncertainty")
	}
	want := map[uint64]int64{}
	for key, delta := range truth.Expected.Deltas {
		h, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		want[h] = delta
	}
	if len(r.Concentration) != 1 || r.Concentration[0].CandidateCount != 4 {
		t.Fatal(r.Concentration)
	}
	for _, m := range r.Concentration[0].Movers {
		h, err := strconv.ParseUint(m.Hash, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		if m.Delta.Lower != want[h] || m.Delta.Upper != want[h] {
			t.Fatalf("wrong delta: %+v", m)
		}
		delete(want, h)
	}
	if len(want) != 0 {
		t.Fatal("missing candidates")
	}
	encoded, _ := json.Marshal(a)
	if !bytes.Equal(before, encoded) {
		t.Fatal("input mutated")
	}
	slices.Reverse(a)
	slices.Reverse(b)
	r2, err := Compare(a, b, options())
	if err != nil || !reflect.DeepEqual(r, r2) {
		t.Fatal("arrival order changed report", err)
	}
}

func TestReplayRestartAndCoverage(t *testing.T) {
	a, b := windows(t)
	a = append(a, a[0])
	r, err := Compare(a, b, options())
	if err != nil {
		t.Fatal(err)
	}
	if r.Before.SelectedSnapshots != 2 {
		t.Fatal("replay selected twice")
	}
	a, b = windows(t)
	a[0].ObservedEnd = a[0].WindowStart + minute/2
	restart := fixture(t, minute, fixtureSource{Producer: "hosted", Users: []uint64{9}, Prompts: []uint64{10}, Weights: []int64{50}})
	restart.Epoch = "epoch-2"
	restart.ObservedStart = a[0].ObservedEnd
	a = append(a, restart)
	r, err = Compare(a, b, options())
	if err != nil || r.Before.SelectedSnapshots != 3 {
		t.Fatal("restart lost", err)
	}
	a[2].ObservedStart++
	if _, err = Compare(a, b, options()); err == nil {
		t.Fatal("partial window accepted without opt-in")
	}
	o := options()
	o.AllowPartial = true
	r, err = Compare(a, b, o)
	if err != nil || r.Complete || len(r.Before.PartialProducers) != 1 {
		t.Fatal("missing partial coverage", err)
	}
	a, b = windows(t)
	if _, err = Compare(a, b[:1], options()); err == nil {
		t.Fatal("missing producer accepted without opt-in")
	}
	r, err = Compare(a, b[:1], o)
	if err != nil || r.Complete || len(r.After.MissingProducers) != 1 {
		t.Fatal("missing producer not reported", err)
	}
	if _, err = Compare(a, nil, o); err == nil {
		t.Fatal("empty window treated as zero")
	}
}

func TestRejectsIncompatibleAndMalformedMeasurements(t *testing.T) {
	for name, change := range map[string]func(*summary.Envelope){
		"key":        func(e *summary.Envelope) { e.KeyID = "other" },
		"scope":      func(e *summary.Envelope) { e.ScopeID = "other" },
		"accounting": func(e *summary.Envelope) { e.AccountingID = "other" },
		"duration":   func(e *summary.Envelope) { e.WindowDuration = minute / 2; e.ObservedEnd = e.WindowStart + minute/2 },
		"payload": func(e *summary.Envelope) {
			e.Sketches["top_prompts"] = summary.Payload{Kind: "frequent_items", Data: []byte("SENTINEL")}
		},
		"missing exceeds requests": func(e *summary.Envelope) { e.Counters["missing_token_usage"] = 99 },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := windows(t)
			change(&b[0])
			if _, err := Compare(a, b, options()); err == nil {
				t.Fatal("accepted invalid input")
			}
		})
	}
	a, b := windows(t)
	if _, err := Compare(b, a, options()); err == nil {
		t.Fatal("reversed windows accepted")
	}
	if _, err := Compare(a, a, options()); err == nil {
		t.Fatal("same window accepted")
	}
	for _, top := range []int{0, -1, 101} {
		o := options()
		o.Top = top
		if _, err := Compare(a, b, o); err == nil {
			t.Fatal("invalid top")
		}
	}
	conflict := a[0]
	conflict.Counters = map[string]uint64{"requests": 8, "input_tokens": 900, "output_tokens": 0, "missing_token_usage": 0}
	if _, err := Compare(append(a, conflict), b, options()); err == nil {
		t.Fatal("conflicting snapshot accepted")
	}
}

func TestNontrivialBoundsAndCandidateLimits(t *testing.T) {
	build := func(start int64, after bool) (summary.Envelope, map[uint64]int64) {
		s := fixtureSource{Producer: "hosted"}
		truth := map[uint64]int64{}
		for i := uint64(1); i <= 2000; i++ {
			// Include low-weight keys seen in only one window as well as shared keys.
			if (!after && i%5 == 0) || (after && i%5 == 1) {
				continue
			}
			weight := int64(1 + i%7)
			if after {
				weight = int64(1 + (i*3)%11)
			}
			s.Users = append(s.Users, i)
			s.Prompts = append(s.Prompts, i)
			s.Weights = append(s.Weights, weight)
			truth[i] = weight
		}
		for _, item := range []struct {
			key    uint64
			weight int64
		}{{5001, 40000}, {5002, 30000}} {
			w := item.weight
			if after && item.key == 5001 {
				w = 60000
			}
			if after && item.key == 5002 {
				w = 15000
			}
			s.Users = append(s.Users, item.key)
			s.Prompts = append(s.Prompts, item.key)
			s.Weights = append(s.Weights, w)
			truth[item.key] = w
		}
		return fixture(t, start, s), truth
	}
	a, at := build(minute, false)
	b, bt := build(2*minute, true)
	o := options()
	o.Expected = []string{"hosted"}
	o.Top = 1
	r, err := Compare([]summary.Envelope{a}, []summary.Envelope{b}, o)
	if err != nil {
		t.Fatal(err)
	}
	c := r.Concentration[0]
	if c.BeforeMaxError == 0 || c.AfterMaxError == 0 || c.CandidateCount <= len(c.Movers) {
		t.Fatal("fixture failed to exercise bounded state and truncation")
	}
	if c.UntrackedDelta.Lower != -c.BeforeMaxError || c.UntrackedDelta.Upper != c.AfterMaxError {
		t.Fatal("untracked bound missing")
	}
	// The full candidate union, not the truncated display, defines untracked keys.
	candidates := map[uint64]bool{}
	for _, e := range []summary.Envelope{a, b} {
		s, err := frequentitems.Parse(e.Sketches["top_prompts"].Data)
		if err != nil {
			t.Fatal(err)
		}
		items, err := s.FrequentItems(frequentitems.NoFalseNegatives)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			candidates[item.Hash] = true
		}
	}
	if c.CandidateCount != len(candidates) {
		t.Fatal("reported candidate count differs from full query union")
	}
	truthKeys := map[uint64]bool{}
	for _, truth := range []map[uint64]int64{at, bt} {
		for key := range truth {
			truthKeys[key] = true
		}
	}
	var shared, beforeOnly, afterOnly int
	for key := range truthKeys {
		if candidates[key] {
			continue
		}
		delta := bt[key] - at[key]
		if delta < c.UntrackedDelta.Lower || delta > c.UntrackedDelta.Upper {
			t.Fatalf("untracked key %d: true delta %d outside %+v", key, delta, c.UntrackedDelta)
		}
		switch {
		case at[key] == 0:
			afterOnly++
		case bt[key] == 0:
			beforeOnly++
		default:
			shared++
		}
	}
	if shared == 0 || beforeOnly == 0 || afterOnly == 0 {
		t.Fatalf("missing untracked ground-truth cases: shared=%d before-only=%d after-only=%d", shared, beforeOnly, afterOnly)
	}
	o.Top = 100
	r, err = Compare([]summary.Envelope{a}, []summary.Envelope{b}, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.Concentration[0].UntrackedDelta != c.UntrackedDelta {
		t.Fatal("display limit changed untracked bound")
	}
	for _, m := range r.Concentration[0].Movers {
		h, err := strconv.ParseUint(m.Hash, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		delta := bt[h] - at[h]
		if delta < m.Delta.Lower || delta > m.Delta.Upper {
			t.Fatalf("truth outside bounds: %+v true=%d", m, delta)
		}
		if m.Delta.Lower != m.After.Lower-m.Before.Upper || m.Delta.Upper != m.After.Upper-m.Before.Lower {
			t.Fatal("interval subtraction wrong")
		}
	}
}

func TestSummaryFailureCausesDoNotEchoInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]summary.Envelope, []summary.Envelope, *Options) ([]summary.Envelope, []summary.Envelope)
		want   string
	}{
		{"unexpected before producer", func(a, b []summary.Envelope, o *Options) ([]summary.Envelope, []summary.Envelope) {
			a[0].ProducerID = "SENTINEL_PRODUCER"
			return a, b
		}, "before window: unexpected summary producer"},
		{"unexpected after producer", func(a, b []summary.Envelope, o *Options) ([]summary.Envelope, []summary.Envelope) {
			b[0].ProducerID = "SENTINEL_PRODUCER"
			return a, b
		}, "after window: unexpected summary producer"},
		{"within window contract", func(a, b []summary.Envelope, o *Options) ([]summary.Envelope, []summary.Envelope) {
			a[0].ScopeID = "SENTINEL_SCOPE"
			return a, b
		}, "before window: incompatible summary measurement contract"},
		{"between window contract", func(a, b []summary.Envelope, o *Options) ([]summary.Envelope, []summary.Envelope) {
			for i := range b {
				b[i].AccountingID = "SENTINEL_ACCOUNTING"
			}
			return a, b
		}, "windows: incompatible summary measurement contract"},
		{"invalid identifier", func(a, b []summary.Envelope, o *Options) ([]summary.Envelope, []summary.Envelope) {
			a[0].KeyID = "/SENTINEL_PATH"
			return a, b
		}, "before window: invalid summary identifier"},
		{"invalid expected producer", func(a, b []summary.Envelope, o *Options) ([]summary.Envelope, []summary.Envelope) {
			o.Expected = []string{"/SENTINEL_PATH"}
			return a, b
		}, "before window: invalid expected producer"},
		{"conflicting sequence", func(a, b []summary.Envelope, o *Options) ([]summary.Envelope, []summary.Envelope) {
			conflict := fixture(t, minute, fixtureSource{Producer: "hosted"})
			return append(a, conflict), b
		}, "before window: conflicting summary sequence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := windows(t)
			o := options()
			a, b = tc.change(a, b, &o)
			r, err := Compare(a, b, o)
			if err == nil || err.Error() != tc.want || !reflect.DeepEqual(r, Report{}) {
				t.Fatalf("want %q and no report, got %v, %+v", tc.want, err, r)
			}
		})
	}
}

func TestSummaryCauseRejectsUnreviewedMessages(t *testing.T) {
	fallback := summaryCause(errors.New("SENTINEL_UNKNOWN"))
	if strings.Contains(fallback, "SENTINEL") {
		t.Fatal("unknown error echoed")
	}
	for _, message := range []string{
		"unexpected summary producer: SENTINEL_PRODUCER",
		"SENTINEL_PATH: incompatible summary measurement contract",
		"invalid summary identifier\nSENTINEL_CONTENT",
		"unexpected summary producer ",
	} {
		if got := summaryCause(errors.New(message)); got != fallback {
			t.Fatalf("unreviewed error passed through: %q", got)
		}
	}
}

func TestReportDoesNotExportUntrustedNamesOrHashesByDefault(t *testing.T) {
	a, b := windows(t)
	for _, docs := range [][]summary.Envelope{a, b} {
		for i := range docs {
			docs[i].ScopeID = "SENTINEL_SCOPE"
			docs[i].AccountingID = "SENTINEL_ACCOUNTING"
			docs[i].KeyID = "SENTINEL_KEY"
			docs[i].Epoch = "SENTINEL_EPOCH"
			docs[i].Counters["SENTINEL_COUNTER"] = 1
			docs[i].Sketches["SENTINEL_SKETCH"] = docs[i].Sketches["distinct_users"]
		}
	}
	o := options()
	o.ShowHashes = false
	r, err := Compare(a, b, o)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SENTINEL", "hosted", "selfhosted", "000000000000000a", "\"hash\""} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	if r.OmittedMeasurements != 2 {
		t.Fatal("omitted measurements not counted")
	}
}

func TestCounterDeltaAtIntegerLimit(t *testing.T) {
	a, b := windows(t)
	for _, docs := range [][]summary.Envelope{a, b} {
		for i := range docs {
			docs[i].Counters["input_tokens"] = 0
		}
	}
	a[0].Counters["input_tokens"] = math.MaxInt64
	r, err := Compare(a, b, options())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Counters {
		if c.Name == "input_tokens" && c.Delta != -math.MaxInt64 {
			t.Fatal(fmt.Sprint(c))
		}
	}
}

func TestNominalRSEUsesNormalPrecision(t *testing.T) {
	for _, tc := range []struct {
		profile hllpp.Profile
		p       uint
	}{{hllpp.ProfileMicro, 12}, {hllpp.ProfileSmall, 14}, {hllpp.ProfileDefault, 15}} {
		t.Run(string(tc.profile), func(t *testing.T) {
			registers := 1 << tc.p
			want := 1.04 / math.Sqrt(float64(registers))
			for _, state := range []string{"empty", "sparse", "dense"} {
				t.Run(state, func(t *testing.T) {
					s, err := hllpp.New(tc.profile, sketchhash.UserV1, sketchhash.HMACSHA25664)
					if err != nil {
						t.Fatal(err)
					}
					if state != "empty" {
						s.AddHash(0x9e3779b97f4a7c15)
						if s.SparseCount() == 0 {
							t.Fatal("fixture did not remain sparse")
						}
					}
					if state == "dense" {
						s.ForceDense()
						if s.DenseNonZeroCount() == 0 {
							t.Fatal("fixture did not become dense")
						}
					}
					data, err := s.MarshalBinary()
					if err != nil {
						t.Fatal(err)
					}
					parsed, err := hllpp.Parse(data)
					if err != nil {
						t.Fatal(err)
					}
					if parsed.DenseRegisterBytes() != registers {
						t.Fatal("dense bytes no longer equal register count; update the RSE calculation")
					}
					if got := estimate(parsed).NominalRSE; math.Abs(got-want) > 1e-15 {
						t.Fatalf("nominal RSE = %g, want %g for p=%d", got, want, tc.p)
					}
				})
			}
		})
	}
}
