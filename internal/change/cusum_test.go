// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package change

import (
	"math"
	"slices"
	"testing"
)

func TestCUSUMUsesOnlyEarlierRollingReference(t *testing.T) {
	config := CUSUMConfig{Reference: 4, ScaleFloor: 1, MinShift: 2}
	quiet := []float64{10, 12, 14, 16}
	first := detector(t, config, CUSUMCalibration{})
	second := detector(t, config, CUSUMCalibration{})
	for i, value := range quiet {
		for _, d := range []*CUSUM{first, second} {
			got := step(t, d, uint64(i), value)
			if got.Status != CUSUMInsufficient || got.Samples != i {
				t.Fatalf("warm-up %d: %+v", i, got)
			}
		}
	}
	spike := step(t, first, 4, 30)
	other := step(t, second, 4, 1000)
	scale := 2.9652 // The scale is rounded to float64 before division.
	residual := 17 / scale
	if spike.Status != CUSUMUncalibrated || spike.Samples != 4 || spike.Median != 13 || spike.MAD != 2 ||
		spike.Scale != scale || spike.Residual != residual || spike.Statistic != residual-1 {
		t.Fatalf("unexpected first score: %+v", spike)
	}
	if other.Median != spike.Median || other.MAD != spike.MAD || other.Scale != spike.Scale ||
		other.Statistic <= spike.Statistic {
		t.Fatalf("changing the current bucket changed its own baseline: %+v vs %+v", spike, other)
	}
	next := step(t, first, 5, 15)
	if next.Median != 15 || next.MAD != 2 || next.Residual != 0 || next.Statistic != spike.Statistic-1 {
		t.Fatalf("reference did not evict the oldest and admit the previous bucket: %+v", next)
	}
}

func TestCUSUMRestartsAfterGapsAndExclusions(t *testing.T) {
	d := detector(t, CUSUMConfig{Reference: 2, ScaleFloor: 1, MinShift: 2}, CUSUMCalibration{})
	step(t, d, 10, 10)
	step(t, d, 11, 10)
	if got := step(t, d, 12, 14); got.Statistic != 3 {
		t.Fatalf("pre-gap statistic: %+v", got)
	}
	if got := step(t, d, 14, 500); got.Status != CUSUMInsufficient || got.Samples != 0 {
		t.Fatalf("implicit gap did not drop evidence: %+v", got)
	}
	step(t, d, 15, 500)
	got, err := d.Step(CUSUMBucket{Index: 16, Value: math.NaN()})
	if err != nil || got.Status != CUSUMInsufficient || got.Samples != 0 {
		t.Fatalf("excluded bucket did not break the reference: %+v, %v", got, err)
	}
	for i := uint64(17); i <= 18; i++ {
		if result := step(t, d, i, 10); result.Status != CUSUMInsufficient {
			t.Fatalf("warm-up after exclusion at %d: %+v", i, result)
		}
	}
	if got := step(t, d, 19, 12); got.Status != CUSUMUncalibrated || got.Median != 10 || got.Statistic != 1 {
		t.Fatalf("prior segments leaked into post-gap score: %+v", got)
	}
}

func TestCUSUMCalibratesAndDetectsPlantedChanges(t *testing.T) {
	config, quiet, opts := calibrationFixture()
	calibration, err := CalibrateCUSUM(config, quiet, opts)
	h, ok := calibration.Threshold()
	if err != nil || !ok || h != 4 || calibration.Status != CUSUMCalibrated {
		t.Fatalf("calibration = %+v, %v", calibration, err)
	}
	if calibration.Chronological != (CUSUMReplay{Eligible: 512, Alarms: 64}) ||
		calibration.Resampled.Eligible != 512*opts.Resamples || calibration.Resampled.Alarms == 0 ||
		calibration.Resampled.Alarms > calibration.Resampled.Eligible/opts.TargetSpacing {
		t.Fatalf("unexpected replay diagnostics: %+v", calibration)
	}
	again, err := CalibrateCUSUM(config, quiet, opts)
	if err != nil || again != calibration {
		t.Fatalf("fixed-seed calibration changed: %+v vs %+v, %v", calibration, again, err)
	}

	for _, tt := range []struct {
		name   string
		values []float64
		status []CUSUMStatus
		scores []float64
	}{
		{"no change and downward spike", []float64{10, 0, 10},
			[]CUSUMStatus{CUSUMNoAlert, CUSUMNoAlert, CUSUMNoAlert}, []float64{0, 0, 0}},
		{"upward spike hits threshold exactly", []float64{15, 10}, []CUSUMStatus{CUSUMAlert, CUSUMNoAlert}, []float64{4, 0}},
		{"step accumulates, then resets", []float64{13, 13, 13},
			[]CUSUMStatus{CUSUMNoAlert, CUSUMAlert, CUSUMNoAlert}, []float64{2, 4, 0}},
		{"no refractory period", []float64{15, 15}, []CUSUMStatus{CUSUMAlert, CUSUMAlert}, []float64{4, 4}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := detector(t, config, calibration)
			start := quiet[len(quiet)-1].Index + 1
			for i := range config.Reference {
				step(t, d, start+uint64(i), 10)
			}
			for i, value := range tt.values {
				got := step(t, d, start+uint64(config.Reference+i), value)
				if got.Status != tt.status[i] || got.Statistic != tt.scores[i] {
					t.Fatalf("scored bucket %d: %+v, want %s at %v", i, got, tt.status[i], tt.scores[i])
				}
			}
		})
	}
}

func TestCUSUMBlockReplayPreservesOrderAndCarriesOnlyWithinResamples(t *testing.T) {
	// The seeded paths are [1, 3, -4, 1, 3] and [3, 1, 3, 1, 1].
	// Only the second crosses 5, on an increment at the start of a block.
	opts := CUSUMCalibrationOptions{BlockLength: 2, Resamples: 2, Seed: 0}
	got := replayCUSUMBlocks([]float64{-4, -4, 1, 3, 1}, 5, opts)
	if want := (CUSUMReplay{Eligible: 10, Alarms: 1}); got != want {
		t.Fatalf("block replay = %+v, want %+v", got, want)
	}
}

func TestCUSUMCalibrationCannotScoreItsOwnPastOrAnotherConfig(t *testing.T) {
	config, quiet, opts := calibrationFixture()
	calibration, err := CalibrateCUSUM(config, quiet, opts)
	if err != nil || calibration.Status != CUSUMCalibrated {
		t.Fatalf("calibration = %+v, %v", calibration, err)
	}
	other := config
	other.ScaleFloor++
	if _, err := NewCUSUM(other, calibration); err == nil {
		t.Fatal("accepted calibration from another config")
	}
	d := detector(t, config, calibration)
	for _, i := range []int{0, len(quiet) - 1} {
		if _, err := d.Step(quiet[i]); err == nil {
			t.Fatalf("accepted live bucket %d inside the calibration period", i)
		}
	}
	if got := step(t, d, quiet[len(quiet)-1].Index+1, 10); got.Status != CUSUMInsufficient || got.Samples != 0 {
		t.Fatalf("calibration buckets seeded the evaluation: %+v", got)
	}
}

func TestCUSUMCalibrationSeparatesInsufficientFromUncalibrated(t *testing.T) {
	config, quiet, opts := calibrationFixture()
	for _, tt := range []struct {
		name   string
		modify func([]CUSUMBucket, *CUSUMCalibrationOptions) []CUSUMBucket
		want   CUSUMStatus
	}{
		{"short", func(q []CUSUMBucket, _ *CUSUMCalibrationOptions) []CUSUMBucket { return q[:14] }, CUSUMInsufficient},
		{"gap", func(q []CUSUMBucket, _ *CUSUMCalibrationOptions) []CUSUMBucket {
			return slices.Delete(q, 20, 21)
		}, CUSUMInsufficient},
		{"declared incident", func(q []CUSUMBucket, _ *CUSUMCalibrationOptions) []CUSUMBucket {
			q[20].Eligible = false
			return q
		}, CUSUMInsufficient},
		{"too many alarms", func(q []CUSUMBucket, o *CUSUMCalibrationOptions) []CUSUMBucket {
			o.Thresholds = []float64{3}
			return q
		}, CUSUMUncalibrated},
		{"chronological spacing passes but resampled spacing misses", func(q []CUSUMBucket, o *CUSUMCalibrationOptions) []CUSUMBucket {
			// At h=3, chronological replay has 128 alarms in 512 windows;
			// this block length and seed yield 2064 in 8192 resampled windows.
			o.Thresholds, o.TargetSpacing, o.BlockLength = []float64{3}, 4, 2
			return q
		}, CUSUMUncalibrated},
		{"zero alarms", func(q []CUSUMBucket, _ *CUSUMCalibrationOptions) []CUSUMBucket {
			for i := range q {
				q[i].Value = 10
			}
			return q
		}, CUSUMUncalibrated},
	} {
		t.Run(tt.name, func(t *testing.T) {
			options := opts
			q := tt.modify(slices.Clone(quiet), &options)
			got, err := CalibrateCUSUM(config, q, options)
			if _, ok := got.Threshold(); err != nil || ok || got.Status != tt.want {
				t.Fatalf("calibration = %+v, %v; want %s without threshold", got, err, tt.want)
			}
			d := detector(t, config, got)
			for i := range config.Reference {
				step(t, d, uint64(i), 10)
			}
			if got := step(t, d, uint64(config.Reference), 100); got.Status != CUSUMUncalibrated || got.Statistic <= 0 {
				t.Fatalf("unsupported calibration yielded a verdict: %+v", got)
			}
		})
	}
}

func TestCUSUMRejectsInvalidInputWithoutConsumingIt(t *testing.T) {
	var uninitialized CUSUM
	if _, err := uninitialized.Step(CUSUMBucket{}); err == nil {
		t.Fatal("accepted a bucket without an initialized detector")
	}
	config := CUSUMConfig{Reference: 2, ScaleFloor: math.SmallestNonzeroFloat64, MinShift: 2}
	d := detector(t, config, CUSUMCalibration{})
	step(t, d, 0, 0)
	step(t, d, 1, 0)
	for _, bucket := range []CUSUMBucket{
		{Index: 0, Eligible: true},
		{Index: 1, Eligible: true},
		{Index: 2, Value: -1, Eligible: true},
		{Index: 2, Value: math.NaN(), Eligible: true},
		{Index: 2, Value: math.Inf(1), Eligible: true},
		{Index: 2, Value: math.MaxFloat64, Eligible: true}, // Residual would overflow.
	} {
		if _, err := d.Step(bucket); err == nil {
			t.Fatalf("accepted invalid bucket %+v", bucket)
		}
	}
	if got := step(t, d, 2, 0); got.Status != CUSUMUncalibrated || got.Median != 0 || got.Statistic != 0 {
		t.Fatalf("a rejected bucket affected replay: %+v", got)
	}
	for _, invalid := range []CUSUMConfig{
		{},
		{Reference: maxCUSUMReference + 1, ScaleFloor: 1, MinShift: 2},
		{Reference: 2, ScaleFloor: math.NaN(), MinShift: 2},
		{Reference: 2, ScaleFloor: 1, MinShift: math.SmallestNonzeroFloat64},
	} {
		if _, err := NewCUSUM(invalid, CUSUMCalibration{}); err == nil {
			t.Fatalf("accepted invalid config: %+v", invalid)
		}
	}
}

func TestCUSUMExtremeValuesStayFiniteOrFailExplicitly(t *testing.T) {
	config := CUSUMConfig{Reference: 4, ScaleFloor: 1, MinShift: 2}
	d := detector(t, config, CUSUMCalibration{})
	for i := range config.Reference {
		step(t, d, uint64(i), 0)
	}
	large := math.MaxFloat64 * 0.75
	if got := step(t, d, 4, large); got.Statistic != large {
		t.Fatalf("representable statistic: %+v", got)
	}
	if _, err := d.Step(CUSUMBucket{Index: 5, Value: large, Eligible: true}); err == nil {
		t.Fatal("accepted an overflowing uncalibrated statistic")
	}
	if got := step(t, d, 5, 0); got.Statistic != large {
		t.Fatalf("overflowing update affected replay: %+v", got)
	}
	if next, statistic, alert := advanceCUSUM(large, large, math.MaxFloat64); next != 0 ||
		statistic != math.MaxFloat64 || !alert {
		t.Fatalf("overflow at a finite crossing: %v, %v, %v", next, statistic, alert)
	}

	flat := detector(t, config, CUSUMCalibration{})
	for i := range config.Reference {
		step(t, flat, math.MaxUint64-uint64(config.Reference-i), math.MaxFloat64)
	}
	if got := step(t, flat, math.MaxUint64, math.MaxFloat64); got.Median != math.MaxFloat64 ||
		got.MAD != 0 || got.Scale != 1 || got.Statistic != 0 {
		t.Fatalf("overflow in flat maximum-value reference: %+v", got)
	}
}

func TestCUSUMCalibrationBoundsAndInputValidation(t *testing.T) {
	config, quiet, opts := calibrationFixture()
	for _, tt := range []struct {
		name   string
		modify func(*[]CUSUMBucket, *CUSUMCalibrationOptions)
	}{
		{"duplicate index", func(q *[]CUSUMBucket, _ *CUSUMCalibrationOptions) { (*q)[1].Index = (*q)[0].Index }},
		{"invalid quiet value", func(q *[]CUSUMBucket, _ *CUSUMCalibrationOptions) { (*q)[0].Value = math.Inf(1) }},
		{"quiet bucket limit", func(q *[]CUSUMBucket, _ *CUSUMCalibrationOptions) {
			*q = make([]CUSUMBucket, maxCUSUMQuietBuckets+1)
		}},
		{"resample limit", func(_ *[]CUSUMBucket, o *CUSUMCalibrationOptions) { o.Resamples = maxCUSUMResamples + 1 }},
		{"unordered candidates", func(_ *[]CUSUMBucket, o *CUSUMCalibrationOptions) { o.Thresholds = []float64{4, 3} }},
		{"infinite candidate", func(_ *[]CUSUMBucket, o *CUSUMCalibrationOptions) { o.Thresholds = []float64{math.Inf(1)} }},
		{"invalid block length", func(_ *[]CUSUMBucket, o *CUSUMCalibrationOptions) { o.BlockLength = 1 }},
		{"replay work limit", func(q *[]CUSUMBucket, o *CUSUMCalibrationOptions) {
			*q = make([]CUSUMBucket, maxCUSUMQuietBuckets)
			for i := range *q {
				(*q)[i] = CUSUMBucket{Index: uint64(i), Eligible: true}
			}
			o.Thresholds = make([]float64, maxCUSUMThresholds)
			for i := range o.Thresholds {
				o.Thresholds[i] = float64(i + 1)
			}
			o.Resamples = maxCUSUMResamples
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q, options := slices.Clone(quiet), opts
			tt.modify(&q, &options)
			if got, err := CalibrateCUSUM(config, q, options); err == nil {
				t.Fatalf("accepted invalid calibration input: %+v", got)
			}
		})
	}
}

func calibrationFixture() (CUSUMConfig, []CUSUMBucket, CUSUMCalibrationOptions) {
	config := CUSUMConfig{Reference: 3, ScaleFloor: 1, MinShift: 2}
	quiet := make([]CUSUMBucket, config.Reference+512)
	for i := range quiet {
		value := 10.0
		if i >= config.Reference && (i-config.Reference)%8 < 2 {
			value = 14
		}
		quiet[i] = CUSUMBucket{Index: uint64(100 + i), Value: value, Eligible: true}
	}
	opts := CUSUMCalibrationOptions{
		Thresholds: []float64{3, 4, 6}, TargetSpacing: 6, BlockLength: 8, Resamples: 16, Seed: 17,
	}
	return config, quiet, opts
}

func detector(t *testing.T, config CUSUMConfig, calibration CUSUMCalibration) *CUSUM {
	t.Helper()
	d, err := NewCUSUM(config, calibration)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func step(t *testing.T, d *CUSUM, index uint64, value float64) CUSUMResult {
	t.Helper()
	result, err := d.Step(CUSUMBucket{Index: index, Value: value, Eligible: true})
	if err != nil {
		t.Fatalf("bucket %d: %v", index, err)
	}
	return result
}
