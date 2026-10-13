// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package clauderecords

import (
	"errors"
	"math"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func periods(o Options, data [][]byte, key []byte) ([2]spend.Period, string, error) {
	var p [2]spend.Period
	selection := "explicit"
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.BeforePeriod != "" || o.AfterPeriod != "" {
		for i, s := range []string{o.BeforePeriod, o.AfterPeriod} {
			v, err := spend.ParsePeriod(s)
			if err != nil {
				return p, selection, errors.New("supply both before-period and after-period as UTC dates or RFC3339 start/end")
			}
			p[i] = v
		}
	} else {
		var earliest, latest time.Time
		_, _, err := read(data, key, func(r record) error {
			if earliest.IsZero() || r.time.Before(earliest) {
				earliest = r.time
			}
			if latest.IsZero() || r.time.After(latest) {
				latest = r.time
			}
			return nil
		})
		if err != nil {
			return p, "", err
		}
		if earliest.IsZero() {
			return p, "", errors.New("no recognized Claude API events; use the collector JSON logs recipe")
		}
		start := earliest.Truncate(24 * time.Hour)
		if !start.Equal(earliest) {
			start = start.AddDate(0, 0, 1)
		}
		end := latest.Truncate(24 * time.Hour)
		if today := o.Now.UTC().Truncate(24 * time.Hour); end.After(today) {
			end = today
		}
		if end.Sub(start) >= 14*24*time.Hour {
			p = [2]spend.Period{{Start: end.AddDate(0, 0, -14), End: end.AddDate(0, 0, -7)}, {Start: end.AddDate(0, 0, -7), End: end}}
			selection = "complete_utc_weeks"
		} else {
			if !earliest.Before(latest) || latest.UnixNano() > math.MaxInt64-2 {
				return p, "", errors.New("observed span cannot form two periods; supply explicit before-period and after-period")
			}
			// Include the last nanosecond and round outwards by at most one more
			// nanosecond so the two observed-span periods have equal duration.
			end = latest.Add(time.Nanosecond)
			if end.Sub(earliest)%2 != 0 {
				end = end.Add(time.Nanosecond)
			}
			middle := earliest.Add(end.Sub(earliest) / 2)
			p = [2]spend.Period{{Start: earliest, End: middle}, {Start: middle, End: end}}
			selection = "observed_span_halves"
		}
	}
	if p[0].End.After(p[1].Start) || p[0].End.Sub(p[0].Start) != p[1].End.Sub(p[1].Start) || p[1].End.After(o.Now) {
		return p, selection, errors.New("select equal completed periods, with before ending at or before after starts")
	}
	return p, selection, nil
}
