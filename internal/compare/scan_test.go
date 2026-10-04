// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func scanSeries(t testing.TB, n int) []summary.Envelope {
	t.Helper()
	var docs []summary.Envelope
	for i := range n {
		e := fixture(t, int64(i+1)*minute, fixtureSource{Producer: "app"})
		e.Counters["requests"], e.Counters["input_tokens"] = 100, 1000
		f, _ := frequentitems.New("micro", sketchhash.UserV1, sketchhash.HMACSHA25664)
		for k := range uint64(10) {
			if err := f.AddHash(k+1, 100); err != nil {
				t.Fatal(err)
			}
		}
		data, _ := f.MarshalBinary()
		e.Sketches = map[string]summary.Payload{"top_users": {Kind: "frequent_items", Data: data}}
		e.Counters["topk_contract.v1.top_users.00000000000000000000000000000000"] = 0
		docs = append(docs, e)
	}
	return docs
}

func scanOptions(n int) ScanOptions {
	o := DefaultScanOptions()
	o.Expected = []string{"app"}
	o.Now = int64(n+1) * minute
	return o
}

func TestScanQuietAndScalarChanges(t *testing.T) {
	for _, tc := range []string{"flat", "noise", "slow drift", "volume up", "volume down", "intensity", "coverage"} {
		t.Run(tc, func(t *testing.T) {
			docs := scanSeries(t, 25)
			last := &docs[24]
			want := ""
			switch tc {
			case "noise":
				for i := range docs {
					docs[i].Counters["requests"] += uint64(i % 7)
				}
			case "slow drift":
				for i := range docs {
					docs[i].Counters["requests"] += uint64(i)
				}
			case "volume up":
				last.Counters["requests"] = 400
				want = "attempt_volume"
			case "volume down":
				last.Counters["requests"] = 0
				want = "attempt_volume"
			case "intensity":
				last.Counters["input_tokens"] = 4000
				want = "tokens_per_attempt"
			case "coverage":
				last.Counters["missing_token_usage"] = 40
				want = "missing_usage_share"
			}
			r, err := Scan(docs, scanOptions(25))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, f := range r.Windows[0].Findings {
				found = found || f.Signal == want
			}
			if want == "" && r.UnusualWindows != 0 || want != "" && !found {
				t.Fatalf("%s: %+v", tc, r)
			}
			if want == "missing_usage_share" {
				for _, f := range r.Windows[0].Findings {
					if f.Signal == "tokens_per_attempt" {
						t.Fatal("coverage became usage change")
					}
				}
			}
		})
	}
}

func TestScanReadinessAndCompatibility(t *testing.T) {
	for _, tc := range []string{"short", "partial", "gap", "stale", "missing producer", "incompatible", "future", "current"} {
		t.Run(tc, func(t *testing.T) {
			docs := scanSeries(t, 25)
			o := scanOptions(25)
			switch tc {
			case "short":
				docs = docs[20:]
			case "partial":
				docs[24].ObservedEnd--
			case "gap":
				docs = append(docs[:20], docs[21:]...)
			case "stale":
				o.Now += 10 * minute
			case "missing producer":
				o.Expected = []string{"app", "missing-private"}
			case "incompatible":
				docs[12].KeyID = "PRIVATE_SENTINEL"
			case "future":
				docs[24].EmittedAt = o.Now + minute
			case "current":
				o.Now--
			}
			r, err := Scan(docs, o)
			if tc == "incompatible" {
				if err == nil || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
					t.Fatal("incompatible contract accepted or leaked")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc == "current" {
				if r.Windows[0].Start == docs[24].WindowStart {
					t.Fatal("open window judged")
				}
				return
			}
			if r.Status == "evaluated" {
				t.Fatalf("%s silently healthy: %+v", tc, r)
			}
		})
	}
}

func TestScanBoundAwareProminenceAndPersistence(t *testing.T) {
	docs := scanSeries(t, 27)
	for i := 25; i < len(docs); i++ {
		f, _ := frequentitems.New("micro", sketchhash.UserV1, sketchhash.HMACSHA25664)
		_ = f.AddHash(999, 900)
		_ = f.AddHash(1, 100)
		data, _ := f.MarshalBinary()
		docs[i].Sketches["top_users"] = summary.Payload{Kind: "frequent_items", Data: data}
	}
	o := scanOptions(27)
	o.Persist = 2
	o.Recent = 2
	r, err := Scan(docs, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.UnusualWindows != 1 || len(r.Windows[0].Findings) != 0 {
		t.Fatalf("persistence: %+v", r)
	}
	var prominent bool
	for _, f := range r.Windows[1].Findings {
		prominent = prominent || f.Signal == "newly_prominent"
	}
	if !prominent {
		t.Fatal("missing prominent contributor")
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), `"hash"`) || strings.Contains(string(data), "demo-key") {
		t.Fatal("private metadata exposed")
	}
	r2, _ := Scan(docs, o)
	data2, _ := json.Marshal(r2)
	if string(data) != string(data2) {
		t.Fatal("report and finding IDs must be deterministic")
	}
}

func TestScanOptions(t *testing.T) {
	for _, mutate := range []func(*ScanOptions){
		func(o *ScanOptions) { o.Z = math.NaN() }, func(o *ScanOptions) { o.Persist = 0 },
		func(o *ScanOptions) { o.Recent = 9999 }, func(o *ScanOptions) { o.MinAttempts = 0 },
		func(o *ScanOptions) { o.ShareFloor = 2 }, func(o *ScanOptions) { o.Now = -1 },
	} {
		o := scanOptions(25)
		mutate(&o)
		if _, err := Scan(scanSeries(t, 25), o); err == nil {
			t.Fatal("invalid option accepted")
		}
	}
}

func TestScanUsesBoundsAndAllCandidates(t *testing.T) {
	docs := scanSeries(t, 25)
	f, _ := frequentitems.New("micro", sketchhash.UserV1, sketchhash.HMACSHA25664)
	for key := uint64(1); key <= 3000; key++ {
		if err := f.AddHash(key, 1+int64(key%5)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.AddHash(9001, 5000); err != nil {
		t.Fatal(err)
	}
	if f.MaxError() == 0 {
		t.Fatal("fixture must exercise uncertainty")
	}
	lower, upper := f.LowerBoundHash(9001), f.UpperBoundHash(9001)
	if lower >= upper {
		t.Fatal("fixture lacks nontrivial candidate bounds")
	}
	data, _ := f.MarshalBinary()
	docs[24].Sketches["top_users"] = summary.Payload{Kind: "frequent_items", Data: data}
	o := scanOptions(25)
	o.Top = 1
	o.ShareChange = .001
	o.Z = .1
	o.RelativeChange = .001
	o.ShareFloor = (float64(lower) + float64(upper)) / 2 / float64(f.TotalWeight())
	r, err := Scan(docs, o)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range r.Windows[0].Findings {
		if finding.key == 9001 {
			t.Fatal("point estimate crossed floor but lower bound did not", finding)
		}
	}
	o.ShareFloor = .25
	r, err = Scan(docs, o)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, finding := range r.Windows[0].Findings {
		found = found || finding.key == 9001
	}
	if !found {
		t.Fatal("display cap must not cap candidate discovery", r)
	}
}

func TestScanZeroBaselineMissingCountersAndReplay(t *testing.T) {
	docs := scanSeries(t, 25)
	for i := range docs {
		docs[i].Counters["requests"] = 0
		docs[i].Counters["input_tokens"] = 0
	}
	docs[24].Counters["requests"] = 300
	r, err := Scan(docs, scanOptions(25))
	if err != nil {
		t.Fatal(err)
	}
	if r.UnusualWindows != 1 {
		t.Fatal("zero baseline should support a guarded volume spike")
	}
	docs = scanSeries(t, 25)
	for i := range docs {
		delete(docs[i].Counters, "missing_token_usage")
	}
	r, err = Scan(docs, scanOptions(25))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "limited" {
		t.Fatal("absent coverage became healthy")
	}
	docs = scanSeries(t, 25)
	docs = append(docs, docs[24])
	r, err = Scan(docs, scanOptions(25))
	if err != nil || r.UnusualWindows != 0 {
		t.Fatal("duplicate snapshot counted twice", err)
	}
	docs[25].Counters = map[string]uint64{"requests": 300, "input_tokens": 1000, "output_tokens": 0, "missing_token_usage": 0, "topk_contract.v1.top_users.00000000000000000000000000000000": 0}
	if _, err := Scan(docs, scanOptions(25)); err == nil {
		t.Fatal("conflicting replay accepted")
	}
}

func TestScanBaselineExcludesRecentAndPersistenceBreaks(t *testing.T) {
	docs := scanSeries(t, 28)
	for _, i := range []int{25, 27} {
		docs[i].Counters["input_tokens"] = 4000
	}
	o := scanOptions(28)
	o.Recent = 3
	o.Persist = 2
	r, err := Scan(docs, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.UnusualWindows != 0 {
		t.Fatal("quiet intervening window must reset persistence")
	}
	o.Persist = 1
	r, err = Scan(docs, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.UnusualWindows != 2 || r.BaselineEnd != docs[25].WindowStart {
		t.Fatal("evaluation leaked into baseline", r)
	}
	for _, w := range r.Windows {
		for _, s := range w.Signals {
			if s.Signal == "tokens_per_attempt" && s.Median != 10 {
				t.Fatal("future contamination")
			}
		}
	}
}

func BenchmarkScan30Windows(b *testing.B) {
	docs := scanSeries(b, 30)
	o := scanOptions(30)
	o.Recent = 2
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := Scan(docs, o); err != nil {
			b.Fatal(err)
		}
	}
}

func TestScanFindingIDsAreScopedAndStable(t *testing.T) {
	docs := scanSeries(t, 25)
	docs[24].Counters["requests"] = 400
	o := scanOptions(25)
	first, err := Scan(docs, o)
	if err != nil || len(first.Windows[0].Findings) == 0 {
		t.Fatal(first, err)
	}
	id := first.Windows[0].Findings[0].ID
	docs = append(docs, docs[0])
	again, err := Scan(docs, o)
	if err != nil || again.Windows[0].Findings[0].ID != id {
		t.Fatal("replay changed finding identity", err)
	}
	for i := range docs {
		docs[i].ScopeID = "different-private-application"
	}
	other, err := Scan(docs, o)
	if err != nil || other.Windows[0].Findings[0].ID == id {
		t.Fatal("unrelated applications share alert identity", err)
	}
	b, _ := json.Marshal(other)
	if strings.Contains(string(b), "different-private-application") {
		t.Fatal("scope leaked")
	}
}

func TestScanQuietSeriesAcrossDeterministicVariation(t *testing.T) {
	for seed := uint64(1); seed <= 32; seed++ {
		docs := scanSeries(t, 30)
		x := seed
		for i := range docs {
			x = x*6364136223846793005 + 1
			n := uint64(100) + x%31
			docs[i].Counters["requests"] = n
			docs[i].Counters["input_tokens"] = n * (10 + x%3)
		}
		o := scanOptions(30)
		o.Recent = 6
		r, err := Scan(docs, o)
		if err != nil || r.Status != "evaluated" || r.UnusualWindows != 0 {
			t.Fatalf("quiet seed %d: %+v %v", seed, r, err)
		}
	}
}

func FuzzScanSummary(f *testing.F) {
	seed := scanSeries(f, 1)[0]
	encoded, err := seed.MarshalBinary()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add([]byte("private-invalid-summary"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 128<<10 {
			return
		}
		e, err := summary.Parse(data)
		if err != nil || e.WindowDuration > math.MaxInt64/8 || max(e.WindowStart+e.WindowDuration, e.EmittedAt) > math.MaxInt64-7*e.WindowDuration {
			return
		}
		series := make([]summary.Envelope, 7)
		for i := range series {
			series[i] = e
			shift := int64(i) * e.WindowDuration
			series[i].WindowStart += shift
			series[i].ObservedStart += shift
			series[i].ObservedEnd += shift
			series[i].EmittedAt += shift
		}
		o := DefaultScanOptions()
		o.Expected = []string{e.ProducerID}
		o.Now = max(series[6].WindowStart+e.WindowDuration, series[6].EmittedAt)
		r, err := Scan(series, o)
		if string(data) == string(encoded) && (err != nil || r.Status != "evaluated" || len(r.Windows) != 1) {
			t.Fatal("seed failed to reach scoring", err, r.Status)
		}
		if err == nil {
			if _, err := json.Marshal(r); err != nil {
				t.Fatal("non-finite report", err)
			}
		}
	})
}
