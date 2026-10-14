// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/inspect"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	collectortrace "go.opentelemetry.io/proto/slim/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/slim/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func sample(id, day, key string, input, output int) map[string]string {
	return map[string]string{"request_id": id, "call_type": "completion", "api_key": key, "model": "secret-model-alias", "startTime": day + "T12:00:00Z", "endTime": day + "T12:00:01Z", "prompt_tokens": fmt.Sprint(input), "completion_tokens": fmt.Sprint(output), "total_tokens": fmt.Sprint(input + output), "spend": "0.012345678901", "status": "success", "session_id": "shared-session"}
}

func writeRows(t *testing.T, rows []map[string]string) string {
	t.Helper()
	b, e := json.Marshal(rows)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "spend.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	return path
}

func options(t *testing.T) Options {
	t.Helper()
	t.Setenv("SPEND_TEST_SECRET", "synthetic-spend-test-secret-32-bytes")
	return Options{BeforePeriod: "2026-09-01", AfterPeriod: "2026-09-02", GroupBy: "key", Top: 20, SecretEnv: "SPEND_TEST_SECRET", Now: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
}

func TestInvestigationQualityAndComposition(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "key-a", 100, 20), sample("2", "2026-09-01", "key-b", 100, 20), sample("3", "2026-09-02", "key-a", 500, 100), sample("4", "2026-09-02", "key-b", 100, 20)}
	opts := options(t)
	r, e := Investigate(writeRows(t, rows), opts)
	if e != nil {
		t.Fatal(e)
	}
	if r.Before.Tokens != 240 || r.After.Tokens != 720 || r.Volume == nil || r.Volume.TokensPerRequestContribution != 480 || r.Increase == nil || r.Increase.Share.Lower != 1 {
		t.Fatalf("unexpected arithmetic: %+v", r)
	}
	if r.Before.RecordedSpend != "0.024691357802" || r.Before.Distinct < 1.9 || r.Before.Distinct > 2.1 {
		t.Fatal(r.Before)
	}
	zero := sample("5", "2026-09-02", "key-b", 0, 0)
	zero["status"] = "failure"
	partial := sample("6", "2026-09-02", "key-a", 10, 2)
	partial["status"] = "failure"
	rows = append(rows, zero, partial)
	r, e = Investigate(writeRows(t, rows), opts)
	if e != nil {
		t.Fatal(e)
	}
	if r.After.Tokens != 732 || r.After.Quality.ZeroUnknown != 1 || r.After.Quality.Failed != 2 || r.After.Quality.Unclear != 2 || r.After.Quality.Missing != 0 || r.Volume != nil {
		t.Fatalf("quality: %+v", r.After)
	}
	if r.ZeroFilled != "cannot determine from this export" {
		t.Fatal(r.ZeroFilled)
	}
}

func TestPrivacyAndSessionScope(t *testing.T) {
	opts := options(t)
	opts.GroupBy = "session"
	opts.ShowHashes = true
	rows := []map[string]string{sample("1", "2026-09-01", "key-a", 100, 20), sample("2", "2026-09-01", "key-b", 100, 20), sample("3", "2026-09-02", "key-a", 300, 40), sample("4", "2026-09-02", "key-b", 100, 20)}
	for _, r := range rows {
		r["messages"] = "planted source code: secret_fn()"
		r["response"] = "/private/customer/project.py"
		r["user"] = "alice@example.invalid"
		r["team_id"] = "sensitive-team"
	}
	path := writeRows(t, rows)
	before, _ := os.ReadFile(path)
	r, e := Investigate(path, opts)
	if e != nil {
		t.Fatal(e)
	}
	if r.Before.Distinct < 1.9 || len(r.Rankings.Movers) != 2 {
		t.Fatal("sessions from different keys were joined")
	}
	b, _ := json.Marshal(r)
	for _, s := range []string{"key-a", "key-b", "shared-session", "secret-model-alias", "secret_fn", "project.py", "alice@", "sensitive-team", path, os.Getenv(opts.SecretEnv)} {
		if strings.Contains(string(b), s) {
			t.Fatal("sensitive value leaked")
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("source mutated")
	}
}

func TestIdentityNamespaceMatchesSketchkitHash(t *testing.T) {
	opts := options(t)
	key := []byte(os.Getenv(opts.SecretEnv))
	h, e := identity(key, "key", "hello")
	if e != nil {
		t.Fatal(e)
	}
	secret, e := sketchhash.SecretFromEnv(opts.SecretEnv)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal([]string{"litellm-spend/v1", "key", "hello"})
	expected, e := sketchhash.Hash64(secret, sketchhash.UserV1, b)
	if e != nil {
		t.Fatal(e)
	}
	if binary.BigEndian.Uint64(h[:8]) != expected {
		t.Fatal("HMAC construction drift")
	}
	other, _ := identity(key, "user", "hello")
	if h == other {
		t.Fatal("identity namespaces collide")
	}
}

func TestMalformedTotalsAndCosts(t *testing.T) {
	for _, tc := range []struct {
		field, value string
		check        func(Quality) bool
	}{
		{"total_tokens", "121", func(q Quality) bool { return q.TotalMismatch == 1 }},
		{"prompt_tokens", "-1", func(q Quality) bool { return q.Invalid == 1 && q.Missing == 1 }},
		{"completion_tokens", "", func(q Quality) bool { return q.Missing == 1 }},
		{"cache_read_input_tokens", "101", func(q Quality) bool { return q.Invalid == 1 }},
		{"spend", "NaN", func(q Quality) bool { return q.SpendInvalid == 1 }},
		{"spend", "1e99", func(q Quality) bool { return q.SpendInvalid == 1 }},
		{"spend", "", func(q Quality) bool { return q.SpendMissing == 1 }},
		{"spend", "0.0000", func(q Quality) bool { return q.SpendZeroUnknown == 1 }},
	} {
		t.Run(tc.field+tc.value, func(t *testing.T) {
			rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "a", 100, 20)}
			rows[1][tc.field] = tc.value
			r, e := Investigate(writeRows(t, rows), options(t))
			if e != nil {
				t.Fatal(e)
			}
			if !tc.check(r.After.Quality) {
				t.Fatal(r.After.Quality)
			}
		})
	}
	for _, s := range []string{"0", "0.000000000001", "1.23e-2", "1000000000000000000", "0.30000000000000004", "5e-324"} {
		n, ok := costValue(s)
		if !ok {
			t.Fatal(s)
		}
		if n.Sign() < 0 {
			t.Fatal(s)
		}
	}
}

func TestSpendPreservesSQLDecimalPrecision(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "a", 100, 20), sample("3", "2026-09-02", "b", 100, 20)}
	rows[1]["spend"] = "0.30000000000000004"
	rows[2]["spend"] = "0.1"
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil || r.After.RecordedSpend != "0.40000000000000004" || r.After.Quality.SpendInvalid != 0 {
		t.Fatalf("exact decimal sum: %s, invalid=%d, err=%v", r.After.RecordedSpend, r.After.Quality.SpendInvalid, err)
	}
	for _, bad := range []string{"1e-999", "1e999", "0.123456789012345678901", "-0.1"} {
		if _, ok := costValue(bad); ok {
			t.Fatal("accepted out-of-bounds spend")
		}
	}
}

func TestDuplicatesMissingIdentityAndUnsupportedOperations(t *testing.T) {
	opts := options(t)
	rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "", 100, 20)}
	r, e := Investigate(writeRows(t, rows), opts)
	if e != nil {
		t.Fatal(e)
	}
	if r.After.Quality.MissingIdentity != 1 || r.After.AttributedTokens != 0 {
		t.Fatal(r.After)
	}
	duplicate := sample("1", "2026-08-01", "x", 100, 20)
	rows = append(rows, duplicate)
	if _, e = Investigate(writeRows(t, rows), opts); e == nil || !strings.Contains(e.Error(), "duplicate") {
		t.Fatal(e)
	}
	rows[2]["request_id"] = "3"
	rows[2]["call_type"] = "embedding"
	r, e = Investigate(writeRows(t, rows), opts)
	if e != nil || r.ExcludedRows != 1 {
		t.Fatal(r, e)
	}
}

func TestAutomaticPeriodsAndBoundaries(t *testing.T) {
	rows := []map[string]string{}
	for i := 0; i < 20; i++ {
		day := time.Date(2026, 9, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		rows = append(rows, sample(fmt.Sprint(i), day, "a", 100, 20))
	}
	opts := options(t)
	opts.BeforePeriod = ""
	opts.AfterPeriod = ""
	r, e := Investigate(writeRows(t, rows), opts)
	if e != nil {
		t.Fatal(e)
	}
	if r.Before.Period.Start.Format("2006-01-02") != "2026-09-06" || r.After.Period.End.Format("2006-01-02") != "2026-09-20" || r.Before.Requests != 7 || r.After.Requests != 7 {
		t.Fatal(r.Before, r.After)
	}
	if _, e = Investigate(writeRows(t, rows[:14]), opts); e == nil || !strings.Contains(e.Error(), "explicit") && !strings.Contains(e.Error(), "--before-period") {
		t.Fatal(e)
	}
	for _, pair := range [][2]string{{"2026-09-01", ""}, {"2026-09-02", "2026-09-01"}, {"2026-09-01", "2026-09-01"}, {"2026-09-01T00:00:00Z/2026-09-01T12:00:00Z", "2026-09-02"}, {"2026-10-10", "2026-10-11"}} {
		opts := options(t)
		opts.BeforePeriod, opts.AfterPeriod = pair[0], pair[1]
		if _, e = Investigate(writeRows(t, rows), opts); e == nil {
			t.Fatal("bad periods accepted", pair)
		}
	}
	p, e := ParsePeriod("2026-11-01T00:00:00-04:00/2026-11-02T00:00:00-05:00")
	if e != nil || p.End.Sub(p.Start) != 25*time.Hour {
		t.Fatal(p, e)
	}
}

func TestCollectorInspectParityForSpendRows(t *testing.T) {
	// Inspect is pinned to all 36 byte-identical collector accounting fixtures.
	// These mapped rows traverse its full public reader, not a second arithmetic implementation.
	for _, mutate := range []func(map[string]string){
		func(r map[string]string) {},
		func(r map[string]string) {
			r["prompt_tokens"] = "0"
			r["completion_tokens"] = "0"
			r["total_tokens"] = "0"
		},
		func(r map[string]string) { r["completion_tokens"] = "" },
		func(r map[string]string) { r["prompt_tokens"] = "-1" },
		func(r map[string]string) {
			r["cache_read_input_tokens"] = "80"
			r["cache_write_input_tokens"] = "10"
			r["reasoning_output_tokens"] = "5"
		},
		func(r map[string]string) { r["cache_read_input_tokens"] = "200" },
		func(r map[string]string) { r["status"] = "failure" },
	} {
		row := Row{Values: sample("1", "2026-09-01", "a", 100, 20)}
		mutate(row.Values)
		attrs := attributes(row)
		obs, e := accounting.Tokens(attrs)
		if e != nil {
			t.Fatal(e)
		}
		span := &tracepb.Span{}
		for k, v := range attrs {
			span.Attributes = append(span.Attributes, &commonpb.KeyValue{Key: k, Value: v})
		}
		req := &collectortrace.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{span}}}}}}
		b, e := protojson.Marshal(req)
		if e != nil {
			t.Fatal(e)
		}
		path := filepath.Join(t.TempDir(), "otlp.json")
		if e = os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		report, e := inspect.Inspect(path, nil, inspect.Options{Top: 10})
		if e != nil {
			t.Fatal(e)
		}
		for k, v := range obs.Metrics {
			if report.ObservedCounters[k] != v {
				t.Fatalf("counter mismatch %s", k)
			}
		}
		for k, v := range obs.Quality {
			if report.TokenObservations[k] != v {
				t.Fatalf("quality mismatch %s", k)
			}
		}
	}
}

func TestConcentrationBoundsAndNetDenominator(t *testing.T) {
	a, _ := frequentitems.New(frequentitems.ProfileMicro, sketchhash.UserV1, sketchhash.HMACSHA25664)
	b, _ := frequentitems.New(frequentitems.ProfileMicro, sketchhash.UserV1, sketchhash.HMACSHA25664)
	before, after := map[uint64]int64{}, map[uint64]int64{}
	for i := uint64(0); i < 2000; i++ {
		before[i] = int64(i%100 + 1)
		after[i] = int64(i%200 + 1)
		a.AddHash(i, before[i])
		b.AddHash(i, after[i])
	}
	a.AddHash(9000, 1000)
	b.AddHash(9000, 100000)
	before[9000] = 1000
	after[9000] = 100000
	c, e := compare.RankChanges("top_key", a, b, compare.Options{Top: 20, ShowHashes: true})
	if e != nil {
		t.Fatal(e)
	}
	if c.BeforeMaxError == 0 || c.AfterMaxError == 0 {
		t.Fatal("test must exercise approximate bounds")
	}
	for _, m := range c.Movers {
		var h uint64
		fmt.Sscanf(m.Hash, "%x", &h)
		truth := after[h] - before[h]
		if truth < m.Delta.Lower || truth > m.Delta.Upper {
			t.Fatal("bound missed truth")
		}
	}
	result := increase(c, uint64(a.TotalWeight()), uint64(b.TotalWeight()))
	if result == nil || result.Share.Lower > result.Share.Upper || math.IsNaN(result.Share.Upper) {
		t.Fatal(result)
	}
	exact := compare.Concentration{Movers: []compare.Mover{{Delta: compare.Interval{Lower: 150, Upper: 150}}}}
	if got := increase(exact, 100, 200); got.Share.Lower != 1.5 {
		t.Fatal("do not clip contributions to net increase", got)
	}
}

func TestEmptySecretAndRepeatableHashing(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "a", 100, 20), sample("2", "2026-09-02", "a", 100, 20)}
	path := writeRows(t, rows)
	opts := options(t)
	opts.ShowHashes = true
	a, e := Investigate(path, opts)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Investigate(path, opts)
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("repeat comparisons changed")
	}
	opts.SecretEnv = "ABSENT_SPEND_SECRET"
	t.Setenv(opts.SecretEnv, "")
	if _, e = Investigate(path, opts); e == nil {
		t.Fatal("empty secret accepted")
	}
	opts.SecretEnv = ""
	a, e = Investigate(path, opts)
	if e != nil {
		t.Fatal(e)
	}
	b, e = Investigate(path, opts)
	if e != nil || a.Rankings.Movers[0].Hash == b.Rankings.Movers[0].Hash {
		t.Fatal("ephemeral hashes reused")
	}
}

func TestModelAndCounterLimits(t *testing.T) {
	opts := options(t)
	rows := []map[string]string{sample("before", "2026-09-01", "a", 100, 20)}
	for i := 0; i < MaxModels; i++ {
		r := sample(fmt.Sprint(i), "2026-09-02", "a", 100, 20)
		r["model"] = fmt.Sprintf("private-model-%d", i)
		rows = append(rows, r)
	}
	if _, err := Investigate(writeRows(t, rows), opts); err == nil || !strings.Contains(err.Error(), "128 models") {
		t.Fatal("model state limit was not enforced", err)
	}
	rows = []map[string]string{sample("a", "2026-09-01", "a", math.MaxInt64, 0), sample("b", "2026-09-01", "a", 1, 0), sample("c", "2026-09-02", "a", 1, 0)}
	if _, err := Investigate(writeRows(t, rows), opts); err == nil {
		t.Fatal("overflow returned a report")
	}
}

func TestSpendMappingAgainstPinnedCollectorExpectations(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "inspect-contract", "v1")
	var manifest struct {
		Cases []struct {
			Name, Input string
			Expected    struct {
				Metrics           map[string]uint64 `json:"metrics"`
				TokenObservations map[string]uint64 `json:"token_observations"`
				UsageProvenance   map[string]uint64 `json:"usage_provenance"`
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, tc := range manifest.Cases {
		if tc.Name != "current-token-fields" && tc.Name != "subset-violations-retain-counts" && tc.Name != "subsets-without-parent-usage" && !(strings.HasPrefix(tc.Name, "litellm-") && strings.HasSuffix(tc.Name, "-stock")) {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tc.Input))
			if err != nil {
				t.Fatal(err)
			}
			req := &collectortrace.ExportTraceServiceRequest{}
			if err = protojson.Unmarshal(data, req); err != nil {
				t.Fatal(err)
			}
			metrics, quality, origin := map[string]uint64{}, map[string]uint64{}, map[string]uint64{}
			for _, rs := range req.ResourceSpans {
				for _, ss := range rs.ScopeSpans {
					for _, span := range ss.Spans {
						attrs := map[string]*commonpb.AnyValue{}
						for _, kv := range span.Attributes {
							attrs[kv.Key] = kv.Value
						}
						if !accounting.ModelAttempt(attrs) {
							continue
						}
						row := Row{Values: map[string]string{"call_type": "completion"}}
						for field, name := range map[string]string{"gen_ai.usage.input_tokens": "prompt_tokens", "gen_ai.usage.output_tokens": "completion_tokens", "gen_ai.usage.cache_read.input_tokens": "cache_read_input_tokens", "gen_ai.usage.cache_write.input_tokens": "cache_write_input_tokens", "gen_ai.usage.reasoning.output_tokens": "reasoning_output_tokens"} {
							if value := attrs[field]; value != nil {
								switch v := value.Value.(type) {
								case *commonpb.AnyValue_IntValue:
									row.Values[name] = strconv.FormatInt(v.IntValue, 10)
								case *commonpb.AnyValue_StringValue:
									row.Values[name] = v.StringValue
								case *commonpb.AnyValue_DoubleValue:
									row.Values[name] = strconv.FormatFloat(v.DoubleValue, 'g', -1, 64)
								}
							}
						}
						obs, err := accounting.Tokens(attributes(row))
						if err != nil {
							t.Fatal(err)
						}
						for dst, src := range map[*map[string]uint64]map[string]uint64{&metrics: obs.Metrics, &quality: obs.Quality, &origin: obs.Provenance} {
							if err = accounting.MergeCounts(*dst, src); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
			}
			for name, want := range tc.Expected.Metrics {
				if got := metrics[name]; got != want {
					t.Fatalf("%s: got %d want %d", name, got, want)
				}
			}
			for name, want := range tc.Expected.TokenObservations {
				if got := quality[name]; got != want {
					t.Fatalf("%s: got %d want %d", name, got, want)
				}
			}
			for name, want := range tc.Expected.UsageProvenance {
				if got := origin[name]; got != want {
					t.Fatalf("%s: got %d want %d", name, got, want)
				}
			}
		})
		checked++
	}
	if checked != 9 {
		t.Fatalf("expected 9 representable pinned cases, got %d", checked)
	}
}
