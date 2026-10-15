// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const dailyHeader = "date,api_requests,successful_requests,failed_requests\n"

func TestDailyCountsReader(t *testing.T) {
	input := "failed_requests,date,successful_requests,api_requests\r\n1,2026-10-07,8,9\r\n0,2026-10-08,0,0\r\n"
	path := readerWrite(t, ".csv", input)
	got, err := readDailyCounts(path)
	if err != nil || !reflect.DeepEqual(got, map[string]dailyCounts{"2026-10-07": {9, 8, 1}, "2026-10-08": {0, 0, 0}}) {
		t.Fatalf("%v %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != input {
		t.Fatal("source changed")
	}
	for _, input := range []string{
		"", "date,api_requests,api_requests,failed_requests\n",
		"date,api_requests,user,failed_requests\n",
		dailyHeader + "2026-10-07,1,1\n",
		dailyHeader + "2026-10-07,1,1,0,SECRET_SENTINEL\n",
		dailyHeader + "2026-10-07,1,1,0\n2026-10-07,1,1,0\n",
		dailyHeader + "2026-02-30,1,1,0\n",
		dailyHeader + "1969-12-31,1,1,0\n",
		dailyHeader + "2200-01-01,1,1,0\n",
		dailyHeader + "2026-10-7,1,1,0\n",
	} {
		if _, err := parseDailyCounts([]byte(input)); err == nil {
			t.Fatalf("accepted invalid input %q", input)
		}
	}
	for _, value := range []string{"", "NULL", "-1", "+1", "1.0", "1e2", " 1", "9223372036854775808", "SECRET_SENTINEL"} {
		for col := 0; col < 3; col++ {
			values := []string{"1", "1", "0"}
			values[col] = value
			_, err := parseDailyCounts([]byte(dailyHeader + "2026-10-07," + strings.Join(values, ",") + "\n"))
			if err == nil || strings.Contains(err.Error(), "SECRET_SENTINEL") {
				t.Fatal("invalid value accepted or leaked")
			}
		}
	}
}

func TestDailyCountsBoundsAndSpecialFiles(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(dailyHeader + strings.Repeat("x", maxCoverageLine+1)),
		[]byte(strings.Repeat("x", maxCoverageBytes+1)),
	} {
		if _, err := parseDailyCounts(data); !errors.Is(err, errCoverageLimit) {
			t.Fatal(err)
		}
	}
	var many strings.Builder
	many.WriteString(dailyHeader)
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxCoverageDays; i++ {
		fmt.Fprintf(&many, "%s,1,1,0\n", start.AddDate(0, 0, i).Format(time.DateOnly))
	}
	if _, err := parseDailyCounts([]byte(many.String())); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&many, "%s,1,1,0\n", start.AddDate(0, 0, maxCoverageDays).Format(time.DateOnly))
	if _, err := parseDailyCounts([]byte(many.String())); !errors.Is(err, errCoverageLimit) {
		t.Fatal(err)
	}
	dir := t.TempDir()
	regular := readerWrite(t, ".csv", dailyHeader)
	link, fifo := filepath.Join(dir, "SECRET_LINK"), filepath.Join(dir, "SECRET_FIFO")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, link, fifo, filepath.Join(dir, "SECRET_MISSING")} {
		if _, err := readDailyCounts(path); err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
	}
	oversize := readerWrite(t, ".csv", strings.Repeat("x", maxCoverageBytes+1))
	if _, err := readDailyCounts(oversize); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatal(err)
	}
	if _, err := newCoverage(regular, Period{Start: start, End: start.AddDate(0, 0, maxCoverageDays+1)}); !errors.Is(err, errCoverageLimit) {
		t.Fatal(err)
	}
}

func TestCoverageDaysAndUnknowns(t *testing.T) {
	path := readerWrite(t, ".csv", dailyHeader+"2026-10-01,1,1,0\n2026-10-02,3,2,1\n2026-10-03,0,0,0\n2026-10-05,100,100,0\n")
	period, err := ParsePeriod("2026-10-01T00:00:00Z/2026-10-05T12:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	b, err := newCoverage(path, period)
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{"2026-10-01T23:59:59Z", "2026-10-01T21:00:00-04:00", "2026-10-03T12:00:00Z"} {
		start, _ := timestamp(stamp)
		b.add(start)
	}
	r := b.result()
	for i, want := range []string{"matched", "missing_rows", "excess_rows", "daily_counts_missing", "partial_day"} {
		if r.Days[i].Status != want {
			t.Fatalf("day %d: %+v", i, r.Days[i])
		}
	}
	if *r.Days[1].MissingRows != 2 || r.Days[2].MissingRows != nil || r.Days[3].APIRequests != nil || r.Days[4].MissingRows != nil {
		t.Fatal(r)
	}
	if got, err := newCoverage(""); err != nil || got != nil || got.result() != nil {
		t.Fatal("disabled coverage changed behavior")
	}
}

func TestCoverageDoesNotAlterAccounting(t *testing.T) {
	t.Setenv("COVERAGE_TEST_KEY", "synthetic-coverage-key-never-a-credential")
	path := readerWrite(t, ".csv", dailyHeader+"2026-10-07,100,100,0\n2026-10-08,100,100,0\n")
	opts := Options{BeforePeriod: "2026-10-07", AfterPeriod: "2026-10-08", GroupBy: "key", SecretEnv: "COVERAGE_TEST_KEY", Top: 10, Now: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
	before, err := Investigate("../../examples/litellm-spend/synthetic.csv", opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.DailySpendPath = path
	after, err := Investigate("../../examples/litellm-spend/synthetic.csv", opts)
	if err != nil {
		t.Fatal(err)
	}
	if after.RowCoverage == nil || after.RowCoverage.Days[0].Status != "missing_rows" {
		t.Fatal(after.RowCoverage)
	}
	after.RowCoverage = nil
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("coverage changed accounting")
	}
	sessionOpts := SessionOptions{Period: "2026-10-03", BudgetUSD: "1", SecretEnv: "COVERAGE_TEST_KEY", Top: 10, Now: opts.Now}
	plain, err := ReviewSessions("../../examples/litellm-spend/session-budget.csv", sessionOpts)
	if err != nil {
		t.Fatal(err)
	}
	sessionOpts.DailySpendPath = path
	covered, err := ReviewSessions("../../examples/litellm-spend/session-budget.csv", sessionOpts)
	if err != nil || covered.RowCoverage == nil {
		t.Fatal(err)
	}
	covered.RowCoverage = nil
	if !reflect.DeepEqual(plain, covered) {
		t.Fatal("coverage changed session accounting")
	}
}

func TestCoverageCountsAllRowsBeforeAnalysisFilters(t *testing.T) {
	path := readerWrite(t, ".csv", readerHeader+",endTime,status\n"+
		"a,completion,2026-10-07T10:00:00Z,10,1,11,2026-10-07T10:00:01Z,success\n"+
		"b,completion,2026-10-07T11:00:00Z,0,0,0,2026-10-07T11:00:01Z,failure\n"+
		"c,embedding,2026-10-07T12:00:00Z,10,0,10,2026-10-07T12:00:01Z,success\n"+
		"d,responses,2026-10-08T10:00:00Z,10,1,11,2026-10-08T10:00:01Z,success\n"+
		"e,completion,2026-10-09T10:00:00Z,10,1,11,2026-10-09T10:00:01Z,success\n")
	daily := readerWrite(t, ".csv", dailyHeader+"2026-10-07,3,2,1\n2026-10-08,1,1,0\n")
	opts := Options{BeforePeriod: "2026-10-07", AfterPeriod: "2026-10-08", DailySpendPath: daily, Top: 10, Now: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
	r, err := Investigate(path, opts)
	if err != nil || r.RowCoverage == nil {
		t.Fatal(err)
	}
	if r.RowCoverage.Days[0].ExportedRows != 3 || r.RowCoverage.Days[1].ExportedRows != 1 || r.RowCoverage.Days[0].Status != "matched" || !r.Incomplete || r.Before.Requests != 2 {
		t.Fatalf("coverage accidentally used analyzed calls: %+v", r)
	}
	for _, period := range []string{"2026-10-07", "2026-10-07T10:00:00Z/2026-10-07T13:00:00Z"} {
		r, err := ReviewSessions(path, SessionOptions{Period: period, DailySpendPath: daily, Top: 10, Now: opts.Now})
		if err != nil || r.RowCoverage.Days[0].ExportedRows != 3 {
			t.Fatal(r.RowCoverage, err)
		}
		want := "matched"
		if strings.Contains(period, "/") {
			want = "partial_day"
		}
		if r.RowCoverage.Days[0].Status != want {
			t.Fatal(r.RowCoverage)
		}
	}
}

func TestCoverageWithEntirelyMissingSpendRows(t *testing.T) {
	path := readerWrite(t, ".csv", readerHeader+"\n")
	daily := readerWrite(t, ".csv", dailyHeader+"2026-10-07,3,2,1\n2026-10-08,1,1,0\n")
	opts := Options{BeforePeriod: "2026-10-07", AfterPeriod: "2026-10-08", DailySpendPath: daily, Top: 10, Now: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}
	r, err := Investigate(path, opts)
	if err != nil || r.RowCoverage == nil || r.AnalysisGap == "" || !r.Incomplete || *r.RowCoverage.Days[0].MissingRows != 3 {
		t.Fatal(r, err)
	}
	s, err := ReviewSessions(path, SessionOptions{Period: "2026-10-07", DailySpendPath: daily, Top: 10, Now: opts.Now})
	if err != nil || s.RowCoverage == nil || *s.RowCoverage.Days[0].MissingRows != 3 || s.SelectedRows != 0 {
		t.Fatal(s, err)
	}
	opts.DailySpendPath = ""
	if _, err := Investigate(path, opts); err == nil {
		t.Fatal("changed default empty-file behavior")
	}
}

func FuzzDailyCounts(f *testing.F) {
	f.Add([]byte(dailyHeader + "2026-10-07,2,1,1\n"))
	f.Add([]byte(dailyHeader + "\"unterminated\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		counts, err := parseDailyCounts(data)
		if err == nil && len(counts) > maxCoverageDays {
			t.Fatal("day cap bypassed")
		}
		if err != nil && err != errCoverageLimit && err != errCoverageCSV {
			t.Fatal("untrusted parse error escaped")
		}
	})
}
