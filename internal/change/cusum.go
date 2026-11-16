// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package change contains local change-detection algorithms.
package change

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
)

const (
	normalMADScale       = 1.4826
	maxCUSUMReference    = 512
	maxCUSUMQuietBuckets = 8192
	maxCUSUMThresholds   = 64
	maxCUSUMResamples    = 64
	maxCUSUMReplaySteps  = 8 << 20
)

// CUSUMConfig freezes the rolling reference and shift for one compatible stratum.
type CUSUMConfig struct {
	Reference  int     // Earlier consecutive eligible buckets required, including warm-up.
	ScaleFloor float64 // Positive floor in the source unit, even when MAD is zero.
	MinShift   float64 // Target upward shift in robust scale units; k is half this value.
}

// CUSUMBucket is one position in an already aligned, compatible series stratum.
// Index advances by one per expected position in that stratum, including missing
// positions. Eligible means known complete and not part of a declared incident.
// The caller must start a new detector on scope, key, schema or accounting changes.
type CUSUMBucket struct {
	Index    uint64
	Value    float64 // Nonnegative source value; ignored when ineligible.
	Eligible bool
}

// CUSUMStatus distinguishes unsupported results from evaluated non-crossings.
type CUSUMStatus string

const (
	CUSUMInsufficient CUSUMStatus = "insufficient"
	CUSUMUncalibrated CUSUMStatus = "uncalibrated"
	CUSUMCalibrated   CUSUMStatus = "calibrated" // Calibration only.
	CUSUMNoAlert      CUSUMStatus = "no_alert"
	CUSUMAlert        CUSUMStatus = "alert"
)

// CUSUMResult describes one bucket. Scoring fields are unavailable when Status
// is insufficient. An uncalibrated score is diagnostic, never an alert verdict.
type CUSUMResult struct {
	Status    CUSUMStatus
	Samples   int
	Median    float64
	MAD       float64
	Scale     float64
	Residual  float64
	Statistic float64 // Pre-reset statistic, capped at MaxFloat64 on overflow at an alert.
}

// CUSUM scores a single stream in chronological order. Use NewCUSUM to create it.
type CUSUM struct {
	config      CUSUMConfig
	calibration CUSUMCalibration
	reference   cusumReference
	score       float64
	previous    uint64
	hasPrevious bool
}

// NewCUSUM freezes the config and optional calibration. A zero calibration
// allows diagnostic scoring but no alert verdicts. Only reuse a calibration
// within the same compatible series stratum and index space.
func NewCUSUM(config CUSUMConfig, calibration CUSUMCalibration) (*CUSUM, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	if calibration.threshold > 0 && calibration.config != config {
		return nil, errors.New("CUSUM calibration does not match the detector config")
	}
	return &CUSUM{config: config, calibration: calibration, reference: newCUSUMReference(config.Reference)}, nil
}

// Step scores before admitting the current value to the reference. Missing or
// excluded buckets break both the reference and the statistic. A crossing
// resets only the statistic, with no refractory period. Errors do not consume
// the bucket. A calibrated detector accepts only buckets after its quiet data.
func (d *CUSUM) Step(bucket CUSUMBucket) (CUSUMResult, error) {
	if d.config.Reference == 0 {
		return CUSUMResult{}, errors.New("CUSUM must be initialized with NewCUSUM")
	}
	if d.calibration.threshold > 0 && bucket.Index <= d.calibration.quietThrough {
		return CUSUMResult{}, errors.New("CUSUM bucket must follow the calibration period")
	}
	if d.hasPrevious && bucket.Index <= d.previous {
		return CUSUMResult{}, errors.New("CUSUM bucket indices must strictly increase")
	}
	if err := bucket.validate(); err != nil {
		return CUSUMResult{}, err
	}
	if !bucket.Eligible || (d.hasPrevious && bucket.Index-d.previous != 1) {
		d.reference.reset()
		d.score = 0
	}
	result := CUSUMResult{Status: CUSUMInsufficient, Samples: len(d.reference.values)}
	if bucket.Eligible {
		if result.Samples == d.config.Reference {
			var err error
			result, err = d.scoreBucket(bucket.Value)
			if err != nil {
				return CUSUMResult{}, err
			}
		}
		d.reference.add(bucket.Value)
	}
	d.previous, d.hasPrevious = bucket.Index, true
	return result, nil
}

func (d *CUSUM) scoreBucket(value float64) (CUSUMResult, error) {
	result, increment, err := d.reference.measure(value, d.config)
	if err != nil {
		return CUSUMResult{}, err
	}
	next, statistic, alert := advanceCUSUM(d.score, increment, d.calibration.threshold)
	if !finite(statistic) {
		return CUSUMResult{}, errors.New("uncalibrated CUSUM statistic exceeds float64 range")
	}
	result.Status = CUSUMUncalibrated
	if alert {
		result.Status = CUSUMAlert
	} else if d.calibration.threshold > 0 {
		result.Status = CUSUMNoAlert
	}
	result.Statistic = statistic
	d.score = next
	return result, nil
}

func advanceCUSUM(score, increment, threshold float64) (next, statistic float64, alert bool) {
	statistic = max(0, score+increment)
	if threshold > 0 && statistic >= threshold {
		return 0, min(statistic, math.MaxFloat64), true
	}
	return statistic, statistic, false
}

func (config CUSUMConfig) validate() error {
	if config.Reference < 2 || config.Reference > maxCUSUMReference {
		return errors.New("CUSUM reference length outside supported bounds")
	}
	if !finite(config.ScaleFloor) || config.ScaleFloor <= 0 {
		return errors.New("CUSUM scale floor must be positive and finite")
	}
	if !finite(config.MinShift) || config.MinShift/2 <= 0 {
		return errors.New("CUSUM minimum shift must have a positive finite half-shift")
	}
	return nil
}

func (bucket CUSUMBucket) validate() error {
	if bucket.Eligible && (!finite(bucket.Value) || bucket.Value < 0) {
		return errors.New("eligible CUSUM bucket value must be nonnegative and finite")
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

type cusumReference struct {
	values  []float64
	scratch []float64
	next    int
}

func newCUSUMReference(size int) cusumReference {
	return cusumReference{values: make([]float64, 0, size), scratch: make([]float64, size)}
}

func (r *cusumReference) reset() { r.values, r.next = r.values[:0], 0 }

func (r *cusumReference) add(value float64) {
	if len(r.values) < cap(r.values) {
		r.values = append(r.values, value)
		return
	}
	r.values[r.next] = value
	r.next = (r.next + 1) % len(r.values)
}

func (r *cusumReference) measure(value float64, config CUSUMConfig) (CUSUMResult, float64, error) {
	copy(r.scratch, r.values)
	center := median(r.scratch)
	for i, past := range r.values {
		r.scratch[i] = math.Abs(past - center)
	}
	mad := median(r.scratch)
	scale := max(normalMADScale*mad, config.ScaleFloor)
	residual := (value - center) / scale
	increment := residual - config.MinShift/2
	if !finite(scale) || !finite(increment) {
		return CUSUMResult{}, 0, errors.New("CUSUM robust score exceeds float64 range")
	}
	return CUSUMResult{Samples: len(r.values), Median: center, MAD: mad, Scale: scale, Residual: residual}, increment, nil
}

func median(values []float64) float64 {
	slices.Sort(values)
	middle := len(values) / 2
	if len(values)%2 != 0 {
		return values[middle]
	}
	lower, upper := values[middle-1], values[middle]
	return lower + (upper-lower)/2 // Both endpoints are nonnegative.
}

// CUSUMCalibrationOptions are fixed before replay. Thresholds must be positive,
// finite and strictly increasing. TargetSpacing is in eligible scored buckets.
type CUSUMCalibrationOptions struct {
	Thresholds    []float64
	TargetSpacing int
	BlockLength   int
	Resamples     int
	Seed          uint64
}

// CUSUMReplay counts exposure and crossings, including trailing exposure. For
// nonzero Alarms, Eligible/Alarms estimates spacing; zero alarms gives no finite estimate.
type CUSUMReplay struct {
	Eligible int
	Alarms   int
}

// CUSUMCalibration contains in-sample diagnostics, not held-out evidence.
// Replay counts are populated only for a fitted threshold.
type CUSUMCalibration struct {
	Status        CUSUMStatus
	Chronological CUSUMReplay
	Resampled     CUSUMReplay
	threshold     float64
	config        CUSUMConfig
	quietThrough  uint64
}

// Threshold returns the fitted crossing threshold, if any.
func (c CUSUMCalibration) Threshold() (float64, bool) { return c.threshold, c.threshold > 0 }

// CalibrateCUSUM selects the lowest candidate whose chronological and pooled
// resampled crossing rates are at most 1/TargetSpacing, with at least one
// resampled crossing. Quiet must be a predeclared contiguous eligible run,
// with at least two target spans and two full blocks left after warm-up.
// Contiguous blocks of causal residual increments retain their internal order;
// a resample starts at zero, carrying the statistic across block joins.
func CalibrateCUSUM(config CUSUMConfig, quiet []CUSUMBucket, opts CUSUMCalibrationOptions) (CUSUMCalibration, error) {
	if err := config.validate(); err != nil {
		return CUSUMCalibration{}, err
	}
	if err := opts.validate(); err != nil {
		return CUSUMCalibration{}, err
	}
	if len(quiet) > maxCUSUMQuietBuckets {
		return CUSUMCalibration{}, errors.New("CUSUM quiet bucket limit exceeded")
	}
	usable, err := checkCUSUMQuiet(quiet)
	if err != nil {
		return CUSUMCalibration{}, err
	}
	eligible := len(quiet) - config.Reference
	if !usable || eligible/2 < max(opts.BlockLength, opts.TargetSpacing) {
		return CUSUMCalibration{Status: CUSUMInsufficient}, nil
	}
	if eligible*(1+opts.Resamples)*len(opts.Thresholds) > maxCUSUMReplaySteps {
		return CUSUMCalibration{}, errors.New("CUSUM replay work limit exceeded")
	}
	increments, err := cusumQuietIncrements(config, quiet)
	if err != nil {
		return CUSUMCalibration{}, err
	}
	for _, threshold := range opts.Thresholds {
		chronological := cusumReplayState{}
		chronological.add(increments, threshold)
		if chronological.alarms > eligible/opts.TargetSpacing {
			continue
		}
		resampled := replayCUSUMBlocks(increments, threshold, opts)
		if resampled.Alarms > 0 && resampled.Alarms <= resampled.Eligible/opts.TargetSpacing {
			return CUSUMCalibration{
				Status: CUSUMCalibrated, threshold: threshold, config: config, quietThrough: quiet[len(quiet)-1].Index,
				Chronological: CUSUMReplay{Eligible: eligible, Alarms: chronological.alarms}, Resampled: resampled,
			}, nil
		}
	}
	return CUSUMCalibration{Status: CUSUMUncalibrated}, nil
}

func (opts CUSUMCalibrationOptions) validate() error {
	if opts.TargetSpacing < 2 || opts.BlockLength < 2 {
		return errors.New("CUSUM target spacing and block length must be at least two")
	}
	if opts.Resamples < 2 || opts.Resamples > maxCUSUMResamples {
		return errors.New("CUSUM resample count outside supported bounds")
	}
	if len(opts.Thresholds) == 0 || len(opts.Thresholds) > maxCUSUMThresholds {
		return errors.New("CUSUM threshold count outside supported bounds")
	}
	previous := 0.0
	for _, threshold := range opts.Thresholds {
		if !finite(threshold) || threshold <= previous {
			return errors.New("CUSUM thresholds must be positive, finite and strictly increasing")
		}
		previous = threshold
	}
	return nil
}

func checkCUSUMQuiet(quiet []CUSUMBucket) (bool, error) {
	usable := true
	for i, bucket := range quiet {
		if err := bucket.validate(); err != nil {
			return false, err
		}
		if i > 0 && bucket.Index <= quiet[i-1].Index {
			return false, errors.New("CUSUM quiet bucket indices must strictly increase")
		}
		if !bucket.Eligible || (i > 0 && bucket.Index-quiet[i-1].Index != 1) {
			usable = false
		}
	}
	return usable, nil
}

func cusumQuietIncrements(config CUSUMConfig, quiet []CUSUMBucket) ([]float64, error) {
	reference := newCUSUMReference(config.Reference)
	increments := make([]float64, 0, len(quiet)-config.Reference)
	for i, bucket := range quiet {
		if i >= config.Reference {
			_, increment, err := reference.measure(bucket.Value, config)
			if err != nil {
				return nil, err
			}
			increments = append(increments, increment)
		}
		reference.add(bucket.Value)
	}
	return increments, nil
}

type cusumReplayState struct {
	score  float64
	alarms int
}

func (r *cusumReplayState) add(increments []float64, threshold float64) {
	for _, increment := range increments {
		next, _, alert := advanceCUSUM(r.score, increment, threshold)
		r.score = next
		if alert {
			r.alarms++
		}
	}
}

func replayCUSUMBlocks(increments []float64, threshold float64, opts CUSUMCalibrationOptions) CUSUMReplay {
	// Use common draws for every candidate, independent of process-global randomness.
	rng := rand.New(rand.NewPCG(opts.Seed, 0))
	result := CUSUMReplay{Eligible: len(increments) * opts.Resamples}
	for range opts.Resamples {
		replay := cusumReplayState{}
		for remaining := len(increments); remaining > 0; remaining -= opts.BlockLength {
			start := rng.IntN(len(increments) - opts.BlockLength + 1)
			length := min(remaining, opts.BlockLength)
			replay.add(increments[start:start+length], threshold)
		}
		result.Alarms += replay.alarms
	}
	return result
}
