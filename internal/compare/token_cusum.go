// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"math"

	"github.com/llm-measurement/fleetdiff/internal/change"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const maxExactCUSUMTokens = uint64(1 << 53)

// TokenCUSUMOptions freezes source-unit scaling and the standardized shift.
// The scan baseline count supplies the reference length. A declared stratum
// uses preceding matching occurrences instead of adjacent windows.
type TokenCUSUMOptions struct {
	ScaleFloor float64
	MinShift   float64
	Stratum    string
}

// TokenCUSUMScanReport adds opt-in diagnostics without changing legacy findings.
type TokenCUSUMScanReport struct {
	ScanReport
	TokenCUSUM TokenCUSUMAnalysis `json:"token_cusum"`
}

// TokenCUSUMAnalysis deliberately supplies no threshold or alert verdict.
type TokenCUSUMAnalysis struct {
	Status      change.CUSUMStatus     `json:"status"`
	Unit        string                 `json:"unit"`
	Clock       string                 `json:"clock"`
	Reference   int                    `json:"reference_windows"`
	ScaleFloor  float64                `json:"scale_floor"`
	MinShift    float64                `json:"minimum_shift"`
	Windows     []TokenCUSUMWindow     `json:"windows"`
	Notes       []string               `json:"notes"`
	Seasonality *TokenCUSUMSeasonality `json:"seasonality,omitempty"`
}

// TokenCUSUMWindow retains exact observed totals separately from robust scores.
type TokenCUSUMWindow struct {
	Start      int64              `json:"start_unix_nano"`
	End        int64              `json:"end_unix_nano"`
	Status     change.CUSUMStatus `json:"status"`
	Reason     string             `json:"reason,omitempty"`
	Total      *uint64            `json:"observed_tokens,omitempty"`
	Samples    int                `json:"reference_samples"`
	Diagnostic *TokenCUSUMScore   `json:"diagnostic,omitempty"`
	StratumKey *int               `json:"stratum_key,omitempty"`
}

// TokenCUSUMScore uses only preceding complete consecutive matching buckets.
type TokenCUSUMScore struct {
	Median    float64 `json:"median"`
	MAD       float64 `json:"mad"`
	Scale     float64 `json:"scale"`
	Residual  float64 `json:"residual"`
	Statistic float64 `json:"statistic"`
}

// ScanTokenCUSUM composes the existing scan/combiner with an uncalibrated,
// token-total diagnostic, optionally separated into declared seasonal strata.
// It does not reprice tokens or rank keys.
func ScanTokenCUSUM(input []summary.Envelope, o ScanOptions, options TokenCUSUMOptions) (TokenCUSUMScanReport, error) {
	if options.Stratum != "" {
		return scanWeekdayTokenCUSUM(input, o, options)
	}
	base, err := Scan(input, o)
	if err != nil {
		return TokenCUSUMScanReport{}, err
	}
	config := change.CUSUMConfig{
		Reference: base.RequestedBaseline, ScaleFloor: options.ScaleFloor, MinShift: options.MinShift,
	}
	detector, err := change.NewCUSUM(config, change.CUSUMCalibration{})
	if err != nil {
		return TokenCUSUMScanReport{}, err
	}
	analysis := TokenCUSUMAnalysis{
		Status: change.CUSUMInsufficient, Unit: "observed_tokens", Clock: "collector_processing_time",
		Reference: config.Reference, ScaleFloor: config.ScaleFloor, MinShift: config.MinShift,
		Windows: []TokenCUSUMWindow{},
		Notes: []string{
			"Calibration is pending; these scores are diagnostics rather than alert verdicts.",
			"The measurement is observed input plus output tokens, not cost.",
			"One compatible nonseasonal stratum; exclude declared incidents before evaluation.",
			"Missing or ineligible positions reset the rolling reference and statistic; no refractory period.",
			"Expected producers declare disjoint ownership; complete intervals do not prove every physical request arrived.",
		},
	}
	if base.Status == "not_ready" {
		for _, window := range base.Windows {
			analysis.Windows = append(analysis.Windows, TokenCUSUMWindow{
				Start: window.Start, End: window.End, Status: change.CUSUMInsufficient,
				Reason: "The existing scan readiness checks did not pass.",
			})
		}
		return TokenCUSUMScanReport{ScanReport: base, TokenCUSUM: analysis}, nil
	}
	frames, _, err := scanFrames(input, o.Expected)
	if err != nil {
		return TokenCUSUMScanReport{}, err
	}
	if err := scoreTokenCUSUM(&analysis, frames, base, detector, tokenCUSUMQualityIssues(input, frames)); err != nil {
		return TokenCUSUMScanReport{}, err
	}
	return TokenCUSUMScanReport{ScanReport: base, TokenCUSUM: analysis}, nil
}

func scoreTokenCUSUM(analysis *TokenCUSUMAnalysis, frames []scanFrame, base ScanReport, detector *change.CUSUM, quality map[int64]string) error {
	recentStart := base.Windows[0].Start
	allScored := true
	for _, frame := range frames {
		if frame.start < base.BaselineStart || frame.end > base.AsOf {
			continue
		}
		total, reason := tokenCUSUMTotal(frame)
		if issue := quality[frame.start]; issue != "" {
			total, reason = nil, issue
		}
		bucket := change.CUSUMBucket{Index: uint64(frame.start / (frame.end - frame.start)), Eligible: reason == ""}
		if bucket.Eligible {
			bucket.Value = float64(*total)
		}
		result, err := detector.Step(bucket)
		if err != nil {
			return err
		}
		if frame.start < recentStart {
			continue
		}
		window := TokenCUSUMWindow{
			Start: frame.start, End: frame.end, Total: total, Status: result.Status,
			Samples: result.Samples, Reason: reason,
		}
		if result.Status == change.CUSUMUncalibrated {
			window.Diagnostic = &TokenCUSUMScore{
				Median: result.Median, MAD: result.MAD, Scale: result.Scale,
				Residual: result.Residual, Statistic: result.Statistic,
			}
		} else {
			allScored = false
			if window.Reason == "" {
				window.Reason = "Not enough preceding consecutive eligible windows for the configured reference."
			}
		}
		analysis.Windows = append(analysis.Windows, window)
	}
	if allScored && len(analysis.Windows) > 0 {
		analysis.Status = change.CUSUMUncalibrated
	}
	return nil
}

func tokenCUSUMTotal(frame scanFrame) (*uint64, string) {
	if !frame.complete {
		return nil, "Missing producers or partial observation intervals."
	}
	requests, hasRequests := frame.counters["requests"]
	missing, hasMissing := frame.counters["missing_token_usage"]
	input, hasInput := frame.counters["input_tokens"]
	output, hasOutput := frame.counters["output_tokens"]
	if !hasRequests || !hasMissing || !hasInput || !hasOutput || missing != 0 {
		return nil, "Token totals require explicit counters and complete reported token usage."
	}
	if math.MaxUint64-input < output {
		return nil, "Observed token total exceeds the supported integer range."
	}
	total := input + output
	if requests == 0 && total != 0 {
		return nil, "Nonzero tokens without observed model attempts."
	}
	if total > maxExactCUSUMTokens {
		return &total, "Exact token total exceeds the diagnostic's float64 exact-integer range."
	}
	return &total, ""
}

func tokenCUSUMQualityIssues(input []summary.Envelope, frames []scanFrame) map[int64]string {
	type snapshotID struct {
		start           int64
		producer, epoch string
		sequence        uint64
	}
	selected := make(map[snapshotID]bool, len(input))
	for _, frame := range frames {
		for _, source := range frame.sources {
			selected[snapshotID{frame.start, source.ProducerID, source.Epoch, source.Sequence}] = true
		}
	}
	issues := map[int64]string{}
	// Use Combine's selected identities, but check each source separately so
	// opposing producer or epoch errors cannot cancel in aggregate counters.
	for _, doc := range input {
		id := snapshotID{doc.WindowStart, doc.ProducerID, doc.Epoch, doc.Sequence}
		if selected[id] && !tokenCUSUMQuality(doc.Counters) {
			issues[doc.WindowStart] = "Per-source token accounting or deduplication coverage is incomplete or not clean."
		}
	}
	return issues
}

func tokenCUSUMQuality(counters map[string]uint64) bool {
	if counters["requests"] == 0 && (counters["input_tokens"] != 0 || counters["output_tokens"] != 0) {
		return false
	}
	if counters["dedup_key_missing"] != 0 {
		return false
	}
	for _, field := range []string{"input", "output"} {
		prefix := "token_observations." + field + "."
		for _, state := range []string{"reported", "missing", "invalid", "conflict"} {
			value, present := counters[prefix+state]
			if !present || (state == "reported" && value != counters["requests"]) || (state != "reported" && value != 0) {
				return false
			}
		}
		if counters[prefix+"subset_violation"] != 0 {
			return false
		}
	}
	return true
}
