// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sessionOptions() SessionOptions {
	return SessionOptions{Period: "2026-09-01T00:00:00Z/2026-09-04T00:00:00Z", BudgetUSD: "1", Top: 20, Now: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
}

func sessionRow(id, session, day, cost string) map[string]string {
	r := sample(id, day, "sensitive-key", 100, 20)
	r["session_id"] = session
	r["spend"] = cost
	r["rejection_kind"] = ""
	r["agent_id"] = "sentinel-agent-private"
	r["team_id"] = "sentinel-team-private"
	return r
}

func TestSessionBudgetExactScopeAndDistributions(t *testing.T) {
	rows := []map[string]string{
		sessionRow("1", "same", "2026-09-01", "0.8"), sessionRow("2", "same", "2026-09-02", "0.4"),
		sessionRow("3", "second", "2026-09-02", "2"), sessionRow("4", "third", "2026-09-02", "3"), sessionRow("5", "fourth", "2026-09-03", "0.5"),
	}
	rows[1]["api_key"] = "second-key"
	rows[1]["agent_id"] = "second-agent"
	rows[1]["team_id"] = "second-team"
	rej := sessionRow("6", "second", "2026-09-02", "0")
	rej["rejection_kind"] = "session_budget"
	rej["status"] = "failure"
	rows = append(rows, rej)
	r, err := ReviewSessions(writeRows(t, rows), sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Sessions != 4 || r.ModelCalls != 5 || r.Exceeding != 3 || *r.NoRecordedRejection != 2 || r.SharedCounters != 1 || r.LongSpans != 1 || r.PossiblyClipped != 0 || r.Clipped != 0 || r.Censored != 1 {
		t.Fatalf("counts: %+v", r)
	}
	if r.Spend.N != 4 || *r.Spend.P50 != "1.2" || *r.Spend.P95 != "3" || *r.Spend.Max != "3" || r.Calls.N != 4 || *r.Calls.P50 != "1" || *r.Calls.Max != "2" {
		t.Fatalf("distributions: %+v %+v", r.Spend, r.Calls)
	}
	if r.SharedCounterReview[0].Keys != 2 || r.SharedCounterReview[0].Agents != 2 {
		t.Fatal(r.SharedCounterReview)
	}
	for _, top := range []int{1, 2, 100} {
		opts := sessionOptions()
		opts.Top = top
		v, e := ReviewSessions(writeRows(t, rows), opts)
		if e != nil || v.Exceeding != 3 || *v.NoRecordedRejection != 2 {
			t.Fatalf("top changes counts: %+v %v", v, e)
		}
	}
}

func TestSessionBudgetMissingAndRejectionOnly(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "partial", "2026-09-02", "2"), sessionRow("2", "partial", "2026-09-02", ""), sessionRow("3", "known", "2026-09-02", "0.1"), sessionRow("4", "only-rejected", "2026-09-02", "0")}
	rows[3]["status"] = "failure"
	rows[3]["rejection_kind"] = "session_iterations"
	delete(rows[0], "rejection_kind")
	r, err := ReviewSessions(writeRows(t, rows), sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Sessions != 2 || r.RejectionOnlySessions != 1 || r.Spend.N != 1 || r.Calls.N != 2 || *r.Spend.Max != "0.1" || r.Exceeding != 0 || r.PartialExceeding != 1 || r.NoRecordedRejection != nil || r.ClassifiedRows != 3 || r.Quality.SpendMissing != 1 {
		t.Fatalf("%+v", r)
	}
}

func TestSessionLiteralIdentityAndStableTies(t *testing.T) {
	rows := []map[string]string{sessionRow("1", " A ", "2026-09-02", "1"), sessionRow("2", "a", "2026-09-02", "1"), sessionRow("3", "A", "2026-09-02", "1"), sessionRow("4", "é", "2026-09-02", "1"), sessionRow("5", "e\u0301", "2026-09-02", "1")}
	rows[0]["spend"] = "0.4"
	row := sessionRow("6", " A ", "2026-09-02", "0.6")
	row["api_key"] = "different-key"
	rows = append(rows, row)
	path := writeRows(t, rows)
	a, err := ReviewSessions(path, sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReviewSessions(path, sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if a.Sessions != 5 || a.SharedCounters != 1 || a.Largest[0].Calls != 2 || !reflect.DeepEqual(a, b) {
		t.Fatalf("literal identity/ties changed: %+v %+v", a, b)
	}
}

func TestSessionAutomaticPeriodAndClipping(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "edge", "2026-09-01", "1"), sessionRow("2", "first", "2026-09-02", "1"), sessionRow("3", "middle", "2026-09-03", "1"), sessionRow("4", "last", "2026-09-04", "1"), sessionRow("5", "edge2", "2026-09-05", "1")}
	opts := sessionOptions()
	opts.Period = ""
	r, err := ReviewSessions(writeRows(t, rows), opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.Period.Start.Format(time.DateOnly) != "2026-09-02" || r.Period.End.Format(time.DateOnly) != "2026-09-05" || r.Sessions != 3 || r.PossiblyClipped != 0 || r.Clipped != 0 || !r.AutomaticPeriod || r.ObservedSpan {
		t.Fatal(r)
	}
	r, err = ReviewSessions(writeRows(t, rows[:1]), opts)
	if err != nil || !r.ObservedSpan || r.Sessions != 1 || r.PossiblyClipped != 1 || r.Period.End.Sub(r.Period.Start) != time.Nanosecond {
		t.Fatal(r, err)
	}
}

func TestSessionClippingEvidence(t *testing.T) {
	rowAt := func(id, session, at string) map[string]string {
		row := sessionRow(id, session, "2026-09-02", "1")
		row["startTime"], row["endTime"] = at, at
		return row
	}
	for _, tc := range []struct {
		name, activity, outside, outsideID string
		clipped, possible                  bool
	}{
		{"interior", "12:00:00", "", "", false, false},
		{"left-edge", "00:00:00", "", "", false, true},
		{"left-window-inclusive", "01:00:00", "", "", false, true},
		{"left-window-outside", "01:00:00.000000001", "", "", false, false},
		{"right-window-inclusive", "23:00:00", "", "", false, true},
		{"right-window-outside", "22:59:59.999999999", "", "", false, false},
		{"same-before", "12:00:00", "2026-09-01T23:59:59Z", "s", true, false},
		{"same-at-end", "12:00:00", "2026-09-03T00:00:00Z", "s", true, false},
		{"covered-left", "00:30:00", "2026-09-01T23:59:59Z", "other", false, false},
		{"covered-right", "23:30:00", "2026-09-03T00:00:00Z", "other", false, false},
		{"uncovered-opposite-edge", "23:30:00", "2026-09-01T23:59:59Z", "other", false, true},
		{"literal-not-normalized", "12:00:00", "2026-09-01T23:59:59Z", "S", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []map[string]string{rowAt("selected", "s", "2026-09-02T"+tc.activity+"Z")}
			if tc.outside != "" {
				rows = append(rows, rowAt("outside", tc.outsideID, tc.outside))
			}
			// File order must not change the evidence, including an outside row
			// appearing before a session is admitted to the selected-period map.
			for range 2 {
				opts := sessionOptions()
				opts.Period = "2026-09-02"
				r, err := ReviewSessions(writeRows(t, rows), opts)
				if err != nil || len(r.Largest) != 1 {
					t.Fatal(r, err)
				}
				s := r.Largest[0]
				if s.Clipped != tc.clipped || s.PossiblyClipped != tc.possible || (r.Clipped == 1) != tc.clipped || (r.PossiblyClipped == 1) != tc.possible {
					t.Fatalf("clipping: %+v", r)
				}
				if len(rows) == 2 {
					rows[0], rows[1] = rows[1], rows[0]
				}
			}
		})
	}
}

func TestSessionClippingUsesActivityWithoutSpend(t *testing.T) {
	rows := []map[string]string{sessionRow("a", "s", "2026-09-02", ""), sessionRow("b", "s", "2026-09-02", "1")}
	rows[0]["startTime"], rows[0]["endTime"] = "2026-09-02T00:30:00Z", "2026-09-02T00:30:01Z"
	opts := sessionOptions()
	opts.Period = "2026-09-02"
	r, err := ReviewSessions(writeRows(t, rows), opts)
	if err != nil || r.PossiblyClipped != 1 {
		t.Fatal(r, err)
	}
	opts.CounterWindow = 10 * time.Minute
	r, err = ReviewSessions(writeRows(t, rows), opts)
	if err != nil || r.PossiblyClipped != 0 {
		t.Fatal(r, err)
	}
}

func TestSessionObservedSpan(t *testing.T) {
	rows := []map[string]string{sessionRow("a", "s", "2026-09-02", "1"), sessionRow("b", "s", "2026-09-02", "1")}
	rows[0]["startTime"], rows[0]["endTime"] = "2026-09-02T13:00:00Z", "2026-09-02T13:00:01Z"
	opts := sessionOptions()
	opts.Period = ""
	r, err := ReviewSessions(writeRows(t, rows), opts)
	if err != nil || !r.ObservedSpan || !r.AutomaticPeriod || r.ModelCalls != 2 || r.Period.Start.Format(time.RFC3339Nano) != "2026-09-02T12:00:00Z" || r.Period.End.Format(time.RFC3339Nano) != "2026-09-02T13:00:00.000000001Z" {
		t.Fatal(r, err)
	}
	opts.Now = time.Date(2026, 9, 2, 12, 30, 0, 0, time.UTC)
	if _, err := ReviewSessions(writeRows(t, rows), opts); err == nil {
		t.Fatal("future observed span accepted")
	}
	if _, err := ReviewSessions(writeRows(t, nil), opts); err == nil {
		t.Fatal("empty export accepted")
	}
}

func TestSessionLimitAdvice(t *testing.T) {
	for _, calls := range [][]uint64{{1, 1, 2}, {1, 2}, {2, 2, 2}, {0, 0, 1}} {
		states := map[[32]byte]*sessionState{}
		singletons := 0
		for i, n := range calls {
			states[[32]byte{byte(i)}] = &sessionState{Session: Session{Calls: n}}
			if n == 1 {
				singletons++
			}
		}
		message := sessionLimitError(states).Error()
		if strings.Contains(message, "session IDs may be generated per request") != (singletons*2 > len(calls)) {
			t.Fatal(message)
		}
	}
}

func TestSessionCoverageAndInvalidFields(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "", "2026-09-02", ""), sessionRow("2", "s", "2026-09-02", "bad"), sessionRow("3", "unsupported", "2026-09-02", "10")}
	rows[0]["prompt_tokens"] = ""
	rows[2]["call_type"] = "unknown-secret-operation"
	r, err := ReviewSessions(writeRows(t, rows), sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.UnattributedRows != 1 || r.UnsupportedRows != 1 || r.Quality.Missing != 1 || r.Quality.SpendInvalid != 1 || r.Quality.SpendMissing != 1 || r.Spend.N != 0 || r.ModelCalls != 2 {
		t.Fatalf("%+v", r)
	}
	for _, field := range []string{"rejection_kind", "startTime", "endTime"} {
		bad := sessionRow("1", "s", "2026-09-02", "1")
		bad[field] = "sentinel-agent-private"
		_, err := ReviewSessions(writeRows(t, []map[string]string{bad}), sessionOptions())
		if err == nil || strings.Contains(err.Error(), "sentinel-agent") {
			t.Fatalf("%s: %v", field, err)
		}
	}
}

func TestSessionRejectionColumnPresence(t *testing.T) {
	for _, ext := range []string{"csv", "json", "jsonl"} {
		for _, present := range []bool{false, true} {
			r := sessionRow("1", "s", "2026-09-02", "2")
			delete(r, "rejection_kind")
			var b []byte
			if ext == "csv" {
				header := "request_id,call_type,startTime,prompt_tokens,completion_tokens,total_tokens,session_id,spend,status"
				line := "1,completion,2026-09-02T12:00:00Z,100,20,120,s,2,success"
				if present {
					header += ",rejection_kind"
					line += ","
				}
				b = []byte(header + "\n" + line + "\n")
			} else {
				var m map[string]any
				raw, _ := json.Marshal(r)
				_ = json.Unmarshal(raw, &m)
				if present {
					m["rejection_kind"] = nil
				}
				b, _ = json.Marshal(m)
				if ext == "json" {
					b = append(append([]byte{'['}, b...), ']')
				}
			}
			path := filepath.Join(t.TempDir(), "export."+ext)
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := ReviewSessions(path, sessionOptions())
			if err != nil {
				t.Fatal(err)
			}
			if (out.NoRecordedRejection != nil) != present {
				t.Fatalf("%s present=%v: %+v", ext, present, out)
			}
		}
	}
}

func TestSessionPrivacyAndErrors(t *testing.T) {
	r := sessionRow("secret-request", "secret-session", "2026-09-02", "1")
	r["user"] = "alice@example.invalid"
	r["messages"] = "private_code()"
	r["agent_id"] = "agent-sentinel-never-output"
	opts := sessionOptions()
	opts.ShowHashes = true
	out, err := ReviewSessions(writeRows(t, []map[string]string{r}), opts)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	for _, value := range []string{"secret-request", "secret-session", "agent-sentinel-never-output", "alice@example.invalid", "private_code()", "sensitive-key", "sentinel-team-private"} {
		if strings.Contains(string(b), value) {
			t.Fatalf("leaked %s", value)
		}
	}
	_, err = ReviewSessions(writeRows(t, []map[string]string{r, r}), opts)
	if err == nil || strings.Contains(err.Error(), "secret-request") {
		t.Fatal(err)
	}
	for _, cap := range []string{"-1", "0", "NaN", "1/2", "agent-sentinel-never-output", "1e999"} {
		opts.BudgetUSD = cap
		_, err = ReviewSessions(writeRows(t, []map[string]string{r}), opts)
		if err == nil || strings.Contains(err.Error(), "agent-sentinel") {
			t.Fatalf("invalid cap accepted: %q %v", cap, err)
		}
	}
}

func TestSessionDecimalNearestRank(t *testing.T) {
	values := []*big.Rat{}
	for i := 1; i <= 100; i++ {
		v, _ := costValue(fmt.Sprintf("0.%03d", i))
		values = append(values, v)
	}
	d := distribution(values)
	if d.N != 100 || *d.P50 != "0.05" || *d.P95 != "0.095" || *d.Max != "0.1" {
		t.Fatal(d)
	}
	if d := distribution(nil); d.N != 0 || d.P50 != nil {
		t.Fatal(d)
	}
}

func TestSessionUnsupportedRowsTaintCompleteSpend(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "same", "2026-09-02", "2"), sessionRow("2", "same", "2026-09-02", "100"), sessionRow("3", "other", "2026-09-02", "1")}
	rows[1]["call_type"] = "unrecognized-private-operation"
	r, err := ReviewSessions(writeRows(t, rows), sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Sessions != 2 || r.Spend.N != 1 || r.PartialExceeding != 1 || r.Exceeding != 0 || r.Largest[0].UnanalyzedRows != 1 || r.Largest[0].SpendComplete {
		t.Fatalf("unsupported traffic made a partial session complete: %+v", r)
	}
}

func TestSessionStrictCapAndWindow(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "exact", "2026-09-02", "0.5"), sessionRow("2", "exact", "2026-09-02", "0.5")}
	rows[0]["startTime"] = "2026-09-02T00:00:00Z"
	rows[0]["endTime"] = "2026-09-02T00:00:01Z"
	rows[1]["startTime"] = "2026-09-02T01:00:00Z"
	rows[1]["endTime"] = "2026-09-02T01:00:01Z"
	r, err := ReviewSessions(writeRows(t, rows), sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Exceeding != 0 || r.LongSpans != 0 {
		t.Fatal(r)
	}
	rows[1]["startTime"] = "2026-09-02T01:00:00.001Z"
	rows[1]["spend"] = "0.50000000000000000001"
	r, err = ReviewSessions(writeRows(t, rows), sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Exceeding != 1 || r.LongSpans != 1 || *r.Spend.Max != "1.00000000000000000001" {
		t.Fatal(r)
	}
}

func TestSessionAggregateOverflowAndRejectionValidation(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "same", "2026-09-02", "1"), sessionRow("2", "other", "2026-09-02", "1")}
	for _, row := range rows {
		row["prompt_tokens"] = "9223372036854775807"
		row["completion_tokens"] = "0"
		row["total_tokens"] = "9223372036854775807"
	}
	if _, err := ReviewSessions(writeRows(t, rows), sessionOptions()); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
	row := sessionRow("1", "same", "2026-09-02", "1")
	row["rejection_kind"] = "session_budget"
	if _, err := ReviewSessions(writeRows(t, []map[string]string{row}), sessionOptions()); err == nil {
		t.Fatal("success classified as rejection")
	}
}

func TestSessionUnknownSpanAndPeriodWithoutCap(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "zero", "2026-09-02", "0"), sessionRow("2", "missing", "2026-09-02", ""), sessionRow("3", "one", "2026-09-02", "1")}
	opts := sessionOptions()
	opts.BudgetUSD = ""
	r, err := ReviewSessions(writeRows(t, rows), opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.BudgetUSD != nil || r.NoRecordedRejection != nil || r.FailureLoggingCompleteness != "unknown" {
		t.Fatal(r)
	}
	if r.Largest[0].SpendSpanSeconds == nil || *r.Largest[0].SpendSpanSeconds != 0 || r.Largest[1].SpendSpanSeconds != nil || r.Largest[2].SpendSpanSeconds != nil {
		t.Fatal(r.Largest)
	}
}

func TestSessionUnattributedSpendAndRejectionPopulations(t *testing.T) {
	rows := []map[string]string{sessionRow("1", "", "2026-09-02", "100"), sessionRow("2", "", "2026-09-02", ""), sessionRow("3", "", "2026-09-02", "bad"), sessionRow("4", "complete", "2026-09-02", "0.1"), sessionRow("5", "partial-censored", "2026-09-02", ""), sessionRow("6", "partial-censored", "2026-09-02", "0"), sessionRow("7", "rejection-only", "2026-09-02", "0"), sessionRow("8", "unsupported", "2026-09-02", "0")}
	for _, i := range []int{5, 6} {
		rows[i]["rejection_kind"] = "session_iterations"
		rows[i]["status"] = "failure"
	}
	rows[7]["status"] = "failure"
	rows[7]["call_type"] = "unsupported-operation"
	r, err := ReviewSessions(writeRows(t, rows), sessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.UnattributedSpend != "100" || r.UnattributedRows != 3 || r.UnattributedSpendRows != 1 || r.UnattributedMissingSpend != 1 || r.UnattributedInvalidSpend != 1 {
		t.Fatalf("unattributed: %+v", r)
	}
	if r.Censored != 2 || r.CensoredCallsN != 1 || r.CensoredSpendN != 0 || r.RejectionOnlySessions != 1 || r.Spend.N != 1 || r.Calls.N != 2 || r.UnclassifiedFailures != 1 {
		t.Fatalf("populations: %+v", r)
	}
}
