// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"errors"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/change"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

// TokenCUSUMWeekdayStratum matches aligned daily UTC windows by weekday.
const TokenCUSUMWeekdayStratum = "utc-day-of-week"

const (
	weekdayCUSUMDay  = int64(24 * time.Hour)
	weekdayCUSUMWeek = 7 * weekdayCUSUMDay
	weekdayCUSUMKeys = 7
)

// TokenCUSUMSeasonality declares the compatible population for each reference.
type TokenCUSUMSeasonality struct {
	Key           string `json:"key"`
	Timezone      string `json:"timezone"`
	Cadence       string `json:"cadence"`
	KeyConvention string `json:"key_convention"`
	ReferenceUnit string `json:"reference_unit"`
}

func scanWeekdayTokenCUSUM(input []summary.Envelope, o ScanOptions, options TokenCUSUMOptions) (TokenCUSUMScanReport, error) {
	if options.Stratum != TokenCUSUMWeekdayStratum {
		return TokenCUSUMScanReport{}, errors.New("unsupported token CUSUM stratum")
	}
	if o.BaselineDuration != 0 {
		return TokenCUSUMScanReport{}, errors.New("seasonal token CUSUM requires a baseline count, not a duration")
	}
	base, err := Scan(input, o)
	if err != nil {
		return TokenCUSUMScanReport{}, err
	}
	frames, duration, err := scanFrames(input, o.Expected)
	if err != nil {
		return TokenCUSUMScanReport{}, err
	}
	if err := validateWeekdayCUSUMCadence(duration); err != nil {
		return TokenCUSUMScanReport{}, err
	}
	config := change.CUSUMConfig{
		Reference: base.RequestedBaseline, ScaleFloor: options.ScaleFloor, MinShift: options.MinShift,
	}
	analysis := weekdayCUSUMAnalysis(config)
	if err := scoreWeekdayCUSUM(&analysis, frames, base, o.Recent, config, tokenCUSUMQualityIssues(input, frames)); err != nil {
		return TokenCUSUMScanReport{}, err
	}
	return TokenCUSUMScanReport{ScanReport: base, TokenCUSUM: analysis}, nil
}

func validateWeekdayCUSUMCadence(duration int64) error {
	if duration != weekdayCUSUMDay {
		return errors.New("utc-day-of-week token CUSUM requires one-day summary windows")
	}
	// scanFrames already authenticates epoch alignment through Envelope.Validate.
	return nil
}

func weekdayCUSUMAnalysis(config change.CUSUMConfig) TokenCUSUMAnalysis {
	return TokenCUSUMAnalysis{
		Status: change.CUSUMInsufficient, Unit: "observed_tokens", Clock: "collector_processing_time",
		Reference: config.Reference, ScaleFloor: config.ScaleFloor, MinShift: config.MinShift,
		Windows: []TokenCUSUMWindow{},
		Seasonality: &TokenCUSUMSeasonality{
			Key: TokenCUSUMWeekdayStratum, Timezone: "UTC", Cadence: "24h",
			KeyConvention: "Sunday=0 through Saturday=6", ReferenceUnit: "preceding matching weekly windows",
		},
		Notes: []string{
			"Calibration is pending; these scores are diagnostics rather than alert verdicts.",
			"The measurement is observed input plus output tokens, not cost.",
			"Each UTC weekday has an independent reference and statistic; no observations are pooled across weekdays.",
			"Missing or ineligible weekly occurrences reset that weekday's reference and statistic; no refractory period.",
			"The existing scan readiness veto and 512-file reader limit remain in effect.",
			"Expected producers declare disjoint ownership; complete intervals do not prove every physical request arrived.",
		},
	}
}

func scoreWeekdayCUSUM(analysis *TokenCUSUMAnalysis, frames []scanFrame, base ScanReport, recent int,
	config change.CUSUMConfig, quality map[int64]string) error {
	var detectors [weekdayCUSUMKeys]*change.CUSUM
	for key := range detectors {
		detector, err := change.NewCUSUM(config, change.CUSUMCalibration{})
		if err != nil {
			return err
		}
		detectors[key] = detector
	}
	end := base.AsOf - base.AsOf%weekdayCUSUMDay
	recentStart := max(int64(0), end-int64(recent)*weekdayCUSUMDay)
	historyStart := max(int64(0), recentStart-int64(config.Reference)*weekdayCUSUMWeek)
	next := 0
	allScored := true
	for start := historyStart; start < end; start += weekdayCUSUMDay {
		for next < len(frames) && frames[next].start < start {
			next++
		}
		frame := scanFrame{start: start, end: start + weekdayCUSUMDay}
		present := next < len(frames) && frames[next].start == start
		if present {
			frame = frames[next]
		}
		total, reason := weekdayCUSUMTotal(frame, present, base, quality)
		key := int(time.Unix(0, start).UTC().Weekday())
		bucket := change.CUSUMBucket{Index: uint64(start / weekdayCUSUMWeek), Eligible: reason == ""}
		if bucket.Eligible {
			bucket.Value = float64(*total)
		}
		result, err := detectors[key].Step(bucket)
		if err != nil {
			return err
		}
		if start >= recentStart {
			window := weekdayCUSUMWindow(frame, total, reason, result, key)
			analysis.Windows = append(analysis.Windows, window)
			allScored = allScored && result.Status == change.CUSUMUncalibrated
		}
	}
	if allScored && len(analysis.Windows) > 0 {
		analysis.Status = change.CUSUMUncalibrated
	}
	return nil
}

func weekdayCUSUMTotal(frame scanFrame, present bool, base ScanReport, quality map[int64]string) (*uint64, string) {
	if base.Status == "not_ready" {
		return nil, "The existing scan readiness checks did not pass."
	}
	if !present {
		return nil, "The matching weekday summary window is missing."
	}
	if frame.emitted > base.AsOf {
		return nil, "A selected snapshot was emitted after the reference time; check clocks."
	}
	if issue := quality[frame.start]; issue != "" {
		return nil, issue
	}
	return tokenCUSUMTotal(frame)
}

func weekdayCUSUMWindow(frame scanFrame, total *uint64, reason string, result change.CUSUMResult, key int) TokenCUSUMWindow {
	window := TokenCUSUMWindow{
		Start: frame.start, End: frame.end, Total: total, Status: result.Status,
		Samples: result.Samples, Reason: reason, StratumKey: &key,
	}
	if result.Status == change.CUSUMUncalibrated {
		window.Diagnostic = &TokenCUSUMScore{
			Median: result.Median, MAD: result.MAD, Scale: result.Scale,
			Residual: result.Residual, Statistic: result.Statistic,
		}
	} else if window.Reason == "" {
		window.Reason = "Not enough preceding consecutive eligible matching weekly windows for the configured reference."
	}
	return window
}
