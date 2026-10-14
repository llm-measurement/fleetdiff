// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package clauderecords

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/boundedjson"
)

func testRow(day int) record {
	r := record{event: "claude_code.api_request", time: time.Date(2026, 10, day, 12, 0, 0, 0, time.UTC), attrs: map[string]value{}, resource: map[string]value{}}
	for k, v := range map[string]string{"input_tokens": "7", "cache_read_tokens": "11", "cache_creation_tokens": "5", "output_tokens": "4", "cost_usd": "0.00003435", "cost_usd_micros": "34", "model": "private-model", "user.id": "private-installation", "session.id": "private-session", "prompt.id": "private-prompt", "service.version": "2.1.296", "fleetdiff.capture.profile": "claude-code-logs/v1"} {
		r.attrs[k] = value{kind: "stringValue", text: v}
	}
	return r
}
func testEnvelope(rows ...record) []byte {
	if len(rows) > 500 {
		return append(append(testEnvelope(rows[:500]...), '\n'), testEnvelope(rows[500:]...)...)
	}
	attrs := func(m map[string]value) []any {
		out := []any{}
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			v := m[k]
			out = append(out, map[string]any{"key": k, "value": map[string]any{v.kind: v.text}})
		}
		return out
	}
	var resources []any
	for _, r := range rows {
		resources = append(resources, map[string]any{"resource": map[string]any{"attributes": attrs(r.resource)}, "scopeLogs": []any{map[string]any{"logRecords": []any{map[string]any{"timeUnixNano": strconv.FormatInt(r.time.UnixNano(), 10), "body": map[string]any{"stringValue": r.event}, "attributes": attrs(r.attrs)}}}}})
	}
	b, _ := json.Marshal(map[string]any{"resourceLogs": resources})
	return b
}
func testFile(t *testing.T, b []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func testOptions() Options {
	return Options{BeforePeriod: "2026-10-11", AfterPeriod: "2026-10-12", Top: 20, Now: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
}
func investigateFile(path string, opts Options) (Report, error) {
	return Investigate([]string{path}, opts)
}
func report(t *testing.T, rows ...record) Report {
	t.Helper()
	r, e := investigateFile(testFile(t, testEnvelope(rows...)), testOptions())
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestNativeTokenAndCostNormalization(t *testing.T) {
	r := report(t, testRow(11), testRow(12))
	w := r.Before
	if *w.Input.Value != 23 || *w.Output.Value != 4 || *w.Total.Value != 27 || *w.Cache.Read.Value != 11 || *w.Cache.Write.Value != 5 || *w.Cache.Uncached.Value != 12 {
		t.Fatalf("wrong inclusive mapping: %+v", w)
	}
	if *w.Cost != "0.00003435" || w.Quality["cost_micros_fallback"] != 0 {
		t.Fatalf("rounded native cost: %+v", w)
	}
	if w.DecimalMinusMicros == nil || *w.DecimalMinusMicros != "0.00000035" || w.PairedCosts != 1 {
		t.Fatal(w)
	}
	if r.Finality != "unknown" || r.Unit != "observed api_request records" || r.Decomposition == nil || len(r.ComparisonChecks) != 0 {
		t.Fatalf("wrong unit/checks: %+v", r)
	}
	if len(r.Rankings.Movers) != 1 || r.Rankings.Movers[0].Item != "installation-1" {
		t.Fatal(r.Rankings)
	}
}
func TestIncompleteInvalidAndZeroUsage(t *testing.T) {
	for _, tc := range []struct {
		name, field, replacement, quality string
		remove                            bool
	}{
		{"missing cache", "cache_read_tokens", "", "cache_read_tokens_missing", true},
		{"negative", "input_tokens", "-1", "input_tokens_invalid", false},
		{"fractional", "output_tokens", "1.5", "output_tokens_invalid", false},
		{"overflow", "output_tokens", "9223372036854775808", "output_tokens_invalid", false},
		{"row sum", "input_tokens", "9223372036854775807", "input_sum_invalid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := testRow(11), testRow(12)
			if tc.remove {
				delete(b.attrs, tc.field)
			} else {
				b.attrs[tc.field] = value{kind: "stringValue", text: tc.replacement}
			}
			r := report(t, a, b)
			if r.After.Total.Value != nil || r.After.Quality[tc.quality] != 1 || !slices.Contains(r.ComparisonChecks, "incomplete_token_components") || r.Decomposition != nil {
				t.Fatalf("invalid row treated as complete: %+v", r.After)
			}
		})
	}
	a, b := testRow(11), testRow(12)
	for _, k := range []string{"input_tokens", "cache_read_tokens", "cache_creation_tokens", "output_tokens"} {
		b.attrs[k] = value{kind: "intValue", text: "0"}
	}
	r := report(t, a, b)
	if r.After.Total.Value == nil || *r.After.Total.Value != 0 || r.After.Quality["zero_token_origin_unknown"] != 1 {
		t.Fatal(r.After)
	}
}
func TestDecimalAndMicrosCosts(t *testing.T) {
	for _, tc := range []struct {
		name, decimal, micros string
		remove                bool
		want                  string
		quality               string
	}{
		{"prefer decimal", "0.00003435", "34", false, "0.00003435", ""},
		{"fallback", "", "34", true, "0.000034", "cost_micros_fallback"},
		{"bad decimal no fallback", "SECRET", "34", false, "", "cost_invalid"},
		{"bad micros", "", "-1", true, "", "cost_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := testRow(12)
			b.attrs["cost_usd"] = value{kind: "stringValue", text: tc.decimal}
			b.attrs["cost_usd_micros"] = value{kind: "intValue", text: tc.micros}
			if tc.remove {
				delete(b.attrs, "cost_usd")
			}
			r := report(t, testRow(11), b)
			if tc.want == "" {
				if r.After.Cost != nil {
					t.Fatal(*r.After.Cost)
				}
			} else if r.After.Cost == nil || *r.After.Cost != tc.want {
				t.Fatal(r.After.Cost)
			}
			if tc.quality != "" && r.After.Quality[tc.quality] != 1 {
				t.Fatal(r.After.Quality)
			}
		})
	}
}
func TestAggregateOverflowFailsNotRowOverflow(t *testing.T) {
	a := testRow(11)
	for _, k := range []string{"cache_read_tokens", "cache_creation_tokens", "output_tokens"} {
		a.attrs[k] = value{kind: "intValue", text: "0"}
	}
	a.attrs["input_tokens"] = value{kind: "intValue", text: strconv.FormatInt(math.MaxInt64, 10)}
	_, err := investigateFile(testFile(t, testEnvelope(a, a, testRow(12))), testOptions())
	if err == nil || !strings.Contains(err.Error(), "supported range") {
		t.Fatal(err)
	}
}
func TestDuplicateExportsVersusRepeatedPrompts(t *testing.T) {
	a, b, c := testRow(11), testRow(12), testRow(12)
	c.time = c.time.Add(time.Second)
	path := testFile(t, testEnvelope(a, b, b, c))
	o := testOptions()
	r, err := investigateFile(path, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.After.Records != 3 || r.After.DuplicateCopies != 1 || r.After.Prompts != 1 || !slices.Contains(r.ComparisonChecks, "exact_export_copies_retained") {
		t.Fatal(r.After)
	}
	o.DeduplicateExact = true
	r, err = investigateFile(path, o)
	if err != nil {
		t.Fatal(err)
	}
	if r.After.Records != 2 || r.After.DuplicateCopies != 1 || r.After.MultipleRecordPrompts != 1 || !slices.Contains(r.ComparisonChecks, "records_per_prompt_changed") {
		t.Fatal(r.After)
	}
}
func TestRetryCompanionsAndSilentPeriod(t *testing.T) {
	a, e, x := testRow(11), testRow(12), testRow(12)
	e.event = "claude_code.api_error"
	x.event = "claude_code.api_retries_exhausted"
	r := report(t, a, e, x)
	if r.After.Records != 0 || r.After.Total.Value != nil || r.After.Cost != nil || r.After.ErrorEvents != 1 || r.After.ExhaustedEvents != 1 || r.Decomposition != nil {
		t.Fatalf("companions counted as usage: %+v", r.After)
	}
	if !slices.Contains(r.ComparisonChecks, "period_without_api_request_records") {
		t.Fatal(r.ComparisonChecks)
	}
}
func TestErrorCategoriesDoNotCollapse429And500(t *testing.T) {
	a, b, x, y := testRow(11), testRow(12), testRow(11), testRow(12)
	x.event = "claude_code.api_error"
	y.event = x.event
	x.attrs["status_code"] = value{kind: "intValue", text: "429"}
	y.attrs["status_code"] = value{kind: "intValue", text: "500"}
	r := report(t, a, b, x, y)
	if !slices.Contains(r.ComparisonChecks, "error_status_mix_changed") || r.Before.ErrorStatus["http_429"] != 1 || r.After.ErrorStatus["http_5xx"] != 1 || r.Decomposition != nil {
		t.Fatal(r)
	}
}

func TestIdentityPriorityLocalityAndScope(t *testing.T) {
	a, b := testRow(11), testRow(12)
	a.attrs["user.account_uuid"] = value{kind: "stringValue", text: "private-account"}
	b.resource["user.account_uuid"] = a.attrs["user.account_uuid"]
	r := report(t, a, b)
	if r.Rankings.Movers[0].Item != "account-1" || len(r.Rankings.Movers) != 1 {
		t.Fatal(r.Rankings)
	}
	b.attrs["user.account_uuid"] = value{kind: "stringValue", text: "other-private-account"}
	r = report(t, a, b)
	if r.After.Unattributed != 1 || r.After.PromptRecords != 0 {
		t.Fatal(r.After)
	}
	b = testRow(12)
	delete(b.attrs, "user.id")
	o := testOptions()
	o.GroupBy = "session"
	r, e := investigateFile(testFile(t, testEnvelope(a, b)), o)
	if e != nil {
		t.Fatal(e)
	}
	if r.After.Unattributed != 1 {
		t.Fatal(r.After)
	}
	t.Setenv("CLAUDE_TEST_KEY", "fixed-test-secret-at-least-32-bytes")
	o.SecretEnv = "CLAUDE_TEST_KEY"
	if _, e = investigateFile(testFile(t, testEnvelope(a)), o); e == nil {
		t.Fatal("unscoped persistent key accepted")
	}
	o.SourceScope = "app-a"
	o.ShowHashes = true
	a.attrs["user.id"] = value{kind: "stringValue", text: "x"}
	p := testFile(t, testEnvelope(a, testRow(12)))
	r, e = investigateFile(p, o)
	if e != nil {
		t.Fatal(e)
	}
	o.SourceScope = "app-b"
	s, e := investigateFile(p, o)
	if e != nil {
		t.Fatal(e)
	}
	if r.Rankings.Movers[0].Hash == s.Rankings.Movers[0].Hash {
		t.Fatal("scope did not separate identities")
	}
}

func TestUnverifiedOIDCAttributionIsNotInstallation(t *testing.T) {
	a, b := testRow(11), testRow(12)
	b.attrs["identity.source"] = value{kind: "stringValue", text: "gateway_oidc"}
	r := report(t, a, b)
	if r.After.Unattributed != 1 || r.After.PromptRecords != 0 || r.After.IdentityKinds["installation"] != 0 || r.After.Quality["unverified_identity_regime"] != 1 {
		t.Fatal(r.After)
	}
	b = testRow(12)
	b.attrs["user.account_uuid"] = value{kind: "stringValue", text: " "}
	b.attrs["user.email"] = value{kind: "stringValue", text: "private@example.invalid"}
	r = report(t, a, b)
	if r.After.IdentityKinds["email"] != 1 {
		t.Fatal(r.After)
	}
}
func TestComparisonMixChecks(t *testing.T) {
	a, b := testRow(11), testRow(12)
	b.attrs["service.version"] = value{kind: "stringValue", text: "PRIVATE_VERSION"}
	b.attrs["fleetdiff.capture.profile"] = value{kind: "stringValue", text: "OTHER_PROFILE"}
	b.attrs["user.email"] = value{kind: "stringValue", text: "private@example.invalid"}
	r := report(t, a, b)
	for _, want := range []string{"unverified_source_profile", "source_version_mix_changed", "capture_profile_mix_changed", "identity_source_mix_changed"} {
		if !slices.Contains(r.ComparisonChecks, want) {
			t.Fatalf("missing %s: %v", want, r.ComparisonChecks)
		}
	}
	j, _ := json.Marshal(r)
	for _, s := range []string{"PRIVATE_VERSION", "OTHER_PROFILE", "private@example.invalid", "private-model", "private-installation", "private-session", "private-prompt"} {
		if strings.Contains(string(j), s) {
			t.Fatalf("leaked %s", s)
		}
	}
}
func TestAliasOrderStableAcrossRandomKeys(t *testing.T) {
	a, b, c := testRow(11), testRow(12), testRow(12)
	b.attrs["user.id"] = value{kind: "stringValue", text: "second"}
	c.attrs["user.id"] = value{kind: "stringValue", text: "third"}
	p := testFile(t, testEnvelope(a, b, c))
	var want []string
	for i := 0; i < 10; i++ {
		r, e := investigateFile(p, testOptions())
		if e != nil {
			t.Fatal(e)
		}
		var got []string
		for _, m := range r.Rankings.Movers {
			got = append(got, m.Item)
		}
		if i == 0 {
			want = got
		} else if !reflect.DeepEqual(want, got) {
			t.Fatalf("unstable aliases: %v vs %v", want, got)
		}
	}
}
func TestRankingBoundsCoverExactTotals(t *testing.T) {
	var rows []record
	for i := 0; i < 650; i++ {
		r := testRow(11)
		r.attrs["user.id"] = value{kind: "stringValue", text: fmt.Sprint(i)}
		rows = append(rows, r)
		r = testRow(12)
		r.attrs["user.id"] = value{kind: "stringValue", text: fmt.Sprint(i)}
		r.attrs["output_tokens"] = value{kind: "intValue", text: "14"}
		rows = append(rows, r)
	}
	r := report(t, rows...)
	for _, m := range r.Rankings.Movers {
		if m.Before.Lower > 27 || m.Before.Upper < 27 || m.After.Lower > 37 || m.After.Upper < 37 || m.Delta.Lower > 10 || m.Delta.Upper < 10 {
			t.Fatal(m)
		}
	}
	if r.Rankings.AfterMaxError == 0 {
		t.Fatal("fixture did not exercise approximate ranking")
	}
}
func TestReaderRejectsUnsafeOrAmbiguousInput(t *testing.T) {
	for _, b := range [][]byte{[]byte(`{"resourceLogs":[],"resourceLogs":[]}`), []byte(`{"resourceLogs":[`), []byte(strings.Repeat("[", 40) + strings.Repeat("]", 40)), []byte(`{"error":"PRIVATE"}`), append(testEnvelope(testRow(11)), []byte("PRIVATE")...)} {
		_, e := investigateFile(testFile(t, b), testOptions())
		if e == nil || strings.Contains(e.Error(), "PRIVATE") {
			t.Fatal(e)
		}
	}
	r := testRow(11)
	r.time = time.Time{}
	if _, e := investigateFile(testFile(t, testEnvelope(r)), testOptions()); e == nil {
		t.Fatal("invalid timestamp accepted")
	}
	path := filepath.Join(t.TempDir(), "private-link")
	if e := os.Symlink(testFile(t, testEnvelope(testRow(11))), path); e != nil {
		t.Fatal(e)
	}
	if _, e := investigateFile(path, testOptions()); e == nil {
		t.Fatal("symlink accepted")
	}
}
func TestNumericRepresentationsUseExactIntegers(t *testing.T) {
	for _, kind := range []string{"stringValue", "intValue", "doubleValue"} {
		v := value{kind: kind, text: "9007199254740993"}
		n, ok := number(v)
		if !ok || n != 9007199254740993 {
			t.Fatalf("rounded %s: %d", kind, n)
		}
	}
	for _, text := range []string{"9e18", "9007199254740993", "4.0"} {
		if _, ok := number(value{kind: "doubleValue", text: text}); !ok {
			t.Fatal(text)
		}
	}
	for _, text := range []string{"NaN", "+Inf", "1e-999999999", "1e999999999", "0x1p2", "1/2", "9223372036854775808", "0.1", "-1"} {
		if _, ok := number(value{kind: "doubleValue", text: text}); ok {
			t.Fatalf("accepted %s", text)
		}
	}
	r := testRow(11)
	r.attrs["output_tokens"] = value{kind: "doubleValue", text: "4.0"}
	o := normalize(r)
	if o.total == nil || *o.total != 27 {
		t.Fatal(o)
	}
	if accounting.CostText(o.cost) != "0.00003435" {
		t.Fatal(o.cost)
	}
}
func FuzzClaudeEnvelope(f *testing.F) {
	f.Add(testEnvelope(testRow(11)))
	f.Add([]byte(`{"resourceLogs":[]}`))
	f.Add([]byte(`{"resourceLogs":[null]}`))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}
		v, e := boundedjson.Decode(b, 32, 250000)
		if e != nil {
			return
		}
		_, _, _ = envelope(v, []byte("test-key"), MaxRecords, func(r record) error { _ = normalize(r); return nil })
	})
}
