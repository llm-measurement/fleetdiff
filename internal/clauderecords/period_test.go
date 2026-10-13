// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package clauderecords

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDefaultPeriodsAndObservedSpan(t *testing.T) {
	opts := testOptions()
	opts.BeforePeriod, opts.AfterPeriod = "", ""
	rows := []record{testRow(11), testRow(12)}
	r, err := investigateFile(testFile(t, testEnvelope(rows...)), opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.PeriodSelection != "observed_span_halves" || r.Before.Records != 1 || r.After.Records != 1 || !r.Before.Period.Start.Equal(rows[0].time) || !r.After.Period.End.After(rows[1].time) {
		t.Fatal(r)
	}
	if r.Before.Period.End.Sub(r.Before.Period.Start) != r.After.Period.End.Sub(r.After.Period.Start) {
		t.Fatal("unequal observed halves")
	}
	// A record at the exact midpoint belongs to after, with no overlap.
	middle := testRow(11)
	middle.time = r.Before.Period.End
	r, err = investigateFile(testFile(t, testEnvelope(rows[0], middle, rows[1])), opts)
	if err != nil || r.Before.Records != 1 || r.After.Records != 2 {
		t.Fatal(r, err)
	}
	// Complete weeks use the same conservative interior-day rule as spend logs.
	rows = []record{testRow(1), testRow(2), testRow(8), testRow(9), testRow(15), testRow(16)}
	opts.Now = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	r, err = investigateFile(testFile(t, testEnvelope(rows...)), opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.PeriodSelection != "complete_utc_weeks" || r.Before.Period.Start.Day() != 2 || r.After.Period.Start.Day() != 9 || r.After.Period.End.Day() != 16 || r.Before.Records != 2 || r.After.Records != 2 || r.OutsidePeriods != 2 {
		t.Fatal(r)
	}
}

func TestDefaultPeriodsNeedUsableTimestampsAndExplicitOverride(t *testing.T) {
	opts := testOptions()
	opts.BeforePeriod, opts.AfterPeriod = "", ""
	if _, err := investigateFile(testFile(t, testEnvelope(testRow(11))), opts); err == nil || !strings.Contains(err.Error(), "explicit") {
		t.Fatal(err)
	}
	if _, err := investigateFile(testFile(t, testEnvelope(testRow(14), testRow(15))), opts); err == nil {
		t.Fatal("future fallback accepted")
	}
	opts.BeforePeriod = "2026-10-11"
	if _, err := investigateFile(testFile(t, testEnvelope(testRow(11), testRow(12))), opts); err == nil {
		t.Fatal("one explicit period accepted")
	}
	opts.AfterPeriod = "2026-10-12"
	r, err := investigateFile(testFile(t, testEnvelope(testRow(11), testRow(12), testRow(1))), opts)
	if err != nil || r.PeriodSelection != "explicit" || r.OutsidePeriods != 1 {
		t.Fatal(r, err)
	}
}

func TestRotatedFilesMatchOneFileAndDeduplicateAcrossFiles(t *testing.T) {
	t.Setenv("CLAUDE_ROTATION_KEY", "synthetic-rotation-regression-key-32bytes")
	opts := testOptions()
	opts.SecretEnv, opts.SourceScope, opts.ShowHashes = "CLAUDE_ROTATION_KEY", "synthetic-source", true
	a, b := testRow(11), testRow(12)
	paths := []string{testFile(t, testEnvelope(a)), testFile(t, testEnvelope(b)), testFile(t, nil)}
	got, err := Investigate(paths, opts)
	if err != nil {
		t.Fatal(err)
	}
	want, err := investigateFile(testFile(t, append(append(testEnvelope(a), '\n'), testEnvelope(b)...)), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputFiles != 3 {
		t.Fatal(got.InputFiles)
	}
	got.InputFiles = 1
	if !reflect.DeepEqual(got, want) {
		t.Fatal("rotation changed report")
	}
	paths = append(paths, paths[1])
	got, err = Investigate(paths, opts)
	if err != nil || got.After.Records != 2 || got.After.DuplicateCopies != 1 {
		t.Fatal(got.After, err)
	}
	opts.DeduplicateExact = true
	got, err = Investigate(paths, opts)
	if err != nil || got.After.Records != 1 || got.After.DuplicateCopies != 1 {
		t.Fatal(got.After, err)
	}
	opts.BeforePeriod, opts.AfterPeriod = "", ""
	got, err = Investigate(paths, opts)
	if err != nil || got.PeriodSelection != "observed_span_halves" || got.Before.Records != 1 || got.After.Records != 1 {
		t.Fatal(got, err)
	}
}

func TestRotatedInputLimitsAndSnapshot(t *testing.T) {
	if _, err := load(nil); err == nil {
		t.Fatal("empty file set")
	}
	if _, err := load(make([]string, MaxFiles+1)); err == nil {
		t.Fatal("file count unbounded")
	}
	dir := t.TempDir()
	small := testFile(t, testEnvelope(testRow(11)))
	large := filepath.Join(dir, "private-path-sentinel.jsonl")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxFileBytes); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := load([]string{small, large}); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("combined byte cap/privacy", err)
	}
	if err := os.Symlink(small, filepath.Join(dir, "symlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := load([]string{small, filepath.Join(dir, "symlink")}); err == nil {
		t.Fatal("second-file symlink accepted")
	}
	data, err := load([]string{small})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(small, testEnvelope(testRow(12)), 0600); err != nil {
		t.Fatal(err)
	}
	var day int
	_, _, err = read(data, nil, func(r record) error { day = r.time.Day(); return nil })
	if err != nil || day != 11 {
		t.Fatal("snapshot changed after rotation", day, err)
	}
	_, _, err = readFile(append(append(testEnvelope(testRow(11)), '\n'), testEnvelope(testRow(12))...), nil, 1, func(record) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "log records") {
		t.Fatal("record cap resets at envelope", err)
	}
	// Across files, ignored events count toward the same record cap too.
	ignored := []byte(`{"resourceLogs":[{"scopeLogs":[{"logRecords":[{"body":{"stringValue":"ignored"}}]}]}]}`)
	inputs := make([][]byte, MaxRecords+1)
	for i := range inputs {
		inputs[i] = ignored
	}
	if _, _, err := read(inputs, nil, func(record) error { return nil }); err == nil {
		t.Fatal("record cap resets across files")
	}
}
