// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"errors"
	"strings"
	"time"
)

type Period struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end_exclusive"`
}

func (p Period) contains(t time.Time) bool { return !t.Before(p.Start) && t.Before(p.End) }

func timestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1970 || t.Year() > 2200 {
		return time.Time{}, errors.New("startTime/endTime must be RFC3339 timestamps with a timezone, between 1970 and 2200; use the SQL export recipe")
	}
	return t.UTC(), nil
}

func ParsePeriod(s string) (Period, error) {
	if len(s) == 10 {
		t, err := time.Parse("2006-01-02", s)
		if err == nil && t.Year() >= 1970 && t.Year() <= 2199 {
			return Period{t, t.AddDate(0, 0, 1)}, nil
		}
	}
	a, b, ok := strings.Cut(s, "/")
	if ok {
		start, e1 := timestamp(a)
		end, e2 := timestamp(b)
		if e1 == nil && e2 == nil && start.Before(end) {
			return Period{start, end}, nil
		}
	}
	return Period{}, errors.New("period must be a UTC date or RFC3339 start/end interval")
}

func selectPeriods(before, after string, earliest, latest, now time.Time) (Period, Period, error) {
	var a, b Period
	if before != "" || after != "" {
		var err error
		if a, err = ParsePeriod(before); err != nil {
			return a, b, err
		}
		if b, err = ParsePeriod(after); err != nil {
			return a, b, err
		}
		if a.End.After(b.Start) || a.End.Sub(a.Start) != b.End.Sub(b.Start) {
			return a, b, errors.New("periods must have equal durations, with before ending at or before after starts")
		}
		if b.End.After(now) {
			return a, b, errors.New("select completed periods ending before now")
		}
		return a, b, nil
	}
	// The first/last observed dates can be partial. Interior days are the
	// conservative automatic selection; their presence does not certify export completeness.
	start := earliest.Truncate(24 * time.Hour)
	if !start.Equal(earliest) {
		start = start.AddDate(0, 0, 1)
	}
	end := latest.Truncate(24 * time.Hour)
	if today := now.UTC().Truncate(24 * time.Hour); end.After(today) {
		end = today
	}
	if earliest.IsZero() || end.Sub(start) < 14*24*time.Hour {
		return a, b, errors.New("fewer than 14 complete UTC days in the file; supply --before-period and --after-period (dates or RFC3339 start/end)")
	}
	return Period{end.AddDate(0, 0, -14), end.AddDate(0, 0, -7)}, Period{end.AddDate(0, 0, -7), end}, nil
}
