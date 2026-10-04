// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"errors"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
)

func median(values []float64) float64 {
	x := slices.Clone(values)
	slices.Sort(x)
	n := len(x)
	if n%2 == 1 {
		return x[n/2]
	}
	return x[n/2-1]/2 + x[n/2]/2
}

func baselineSignal(name, measurement string, values []float64, current ScanRange, scaleFloor, minChange float64, o ScanOptions) ScanSignal {
	m := median(values)
	deviations := make([]float64, len(values))
	for i, v := range values {
		deviations[i] = math.Abs(v - m)
	}
	mad := median(deviations)
	scale := max(1.4826*mad, scaleFloor)
	change := max(o.Z*scale, o.RelativeChange*math.Abs(m), minChange)
	return ScanSignal{Signal: name, Measurement: measurement, Status: "evaluated", Current: &current, Median: m, MAD: mad,
		BaselineUpper: slices.Max(values), Threshold: m + change, RequiredChange: change, Score: (current.Lower - m) / scale, Samples: len(values)}
}

func scanSignals(base []scanFrame, f scanFrame, o ScanOptions, work *int) ([]ScanSignal, []ScanFinding, error) {
	var signals []ScanSignal
	var findings []ScanFinding
	// Coverage is assessed before usage, and is never translated into savings.
	for _, name := range []string{"missing_usage_share", "attempt_volume", "tokens_per_attempt"} {
		current, ok := scalarValue(f, name, o.MinAttempts)
		values := make([]float64, 0, len(base))
		for _, b := range base {
			if value, present := scalarValue(b, name, o.MinAttempts); present {
				values = append(values, value)
			}
		}
		if !ok || len(values) != len(base) {
			signals = append(signals, ScanSignal{Signal: name, Status: "coverage_limited", Reason: "This signal needs complete counters, sufficient attempts, and compatible coverage in every baseline and current window.", Samples: len(values)})
			continue
		}
		floor, minimum := 1.0, 0.0
		if name == "missing_usage_share" {
			floor = .01
			minimum = o.CoverageChange
		}
		s := baselineSignal(name, "", values, ScanRange{current, current}, floor, minimum, o)
		if name == "attempt_volume" && max(current, s.Median) < float64(o.MinAttempts) {
			s.Status = "coverage_limited"
			s.Reason = "Not enough model attempts for the configured volume guard."
			signals = append(signals, s)
			continue
		}
		signals = append(signals, s)
		direction := ""
		if current-s.Median >= s.RequiredChange {
			direction = "up"
		}
		if name != "missing_usage_share" && s.Median-current >= s.RequiredChange {
			direction = "down"
		}
		if direction != "" {
			findings = append(findings, ScanFinding{ScanSignal: s, Direction: direction})
		}
	}
	for _, name := range scanMeasurements {
		s, ok := f.sketches[name]
		if !ok {
			signals = append(signals, ScanSignal{Signal: "attribution", Measurement: name, Status: "not_configured", Reason: "This ranking was not exported."})
			continue
		}
		isErrors := name == "top_tool_errors"
		isTokens := !isErrors && !strings.HasSuffix(name, "_requests")
		usable := true
		for _, w := range append(slices.Clone(base), f) {
			if w.sketches[name] == nil {
				usable = false
				break
			}
			if !isErrors && (w.counters["requests"] < o.MinAttempts || w.sketches[name].TotalWeight() == 0) {
				usable = false
			}
			if isTokens {
				if _, ok := scalarValue(w, "tokens_per_attempt", o.MinAttempts); !ok {
					usable = false
				}
			}
		}
		if !usable {
			signals = append(signals, ScanSignal{Signal: "attribution", Measurement: name, Status: "coverage_limited", Reason: "Rankings need matched sketches, positive attributed weight, and complete relevant usage across the baseline."})
			continue
		}
		items, err := s.FrequentItems(frequentitems.NoFalseNegatives)
		if err != nil {
			return nil, nil, errors.New("cannot query scan sketch")
		}
		*work += len(items) * len(base)
		if *work > maxScanComparisons {
			return nil, nil, errors.New("scan candidate comparison budget exceeded; select fewer windows or smaller sketches")
		}
		// Score all retained candidates before limiting display. A baseline query
		// also bounds keys absent from its retained candidates via MaxError.
		slices.SortFunc(items, func(a, b frequentitems.Item) int {
			if a.Hash < b.Hash {
				return -1
			}
			if a.Hash > b.Hash {
				return 1
			}
			return 0
		})
		var top frequentitems.Item
		for _, item := range items {
			if item.LowerBound > top.LowerBound {
				top = item
			}
		}
		if !isErrors && (strings.Contains(name, "sessions") || strings.Contains(name, "users")) {
			values := make([]float64, 0, len(base))
			clears := true
			for _, b := range base {
				bs := b.sketches[name]
				upper := b.upperBounds[name]
				values = append(values, float64(upper)/float64(bs.TotalWeight()))
				clears = clears && new(big.Rat).SetFrac64(top.LowerBound, s.TotalWeight()).Cmp(new(big.Rat).SetFrac64(upper, bs.TotalWeight())) > 0
			}
			current := ScanRange{float64(top.LowerBound) / float64(s.TotalWeight()), float64(top.UpperBound) / float64(s.TotalWeight())}
			signal := baselineSignal("top_share", name, values, current, .01, o.ShareChange, o)
			signal.WeightBounds = &Interval{Lower: top.LowerBound, Upper: min(s.TotalWeight(), top.UpperBound)}
			signal.TotalWeight = s.TotalWeight()
			signal.Threshold = max(signal.Threshold, signal.BaselineUpper, o.ShareFloor)
			signals = append(signals, signal)
			if clears && current.Lower > signal.Threshold && shareStrictlyAbove(top.LowerBound, s.TotalWeight(), o.ShareFloor) {
				findings = append(findings, ScanFinding{ScanSignal: signal, Direction: "up", key: top.Hash})
			}
		}
		signalName := "newly_prominent"
		if isErrors {
			signalName = "tool_error_surge"
		}
		for _, item := range items {
			values := make([]float64, 0, len(base))
			clears := true
			for _, b := range base {
				bs := b.sketches[name]
				upper := min(bs.TotalWeight(), bs.UpperBoundHash(item.Hash))
				if isErrors {
					values = append(values, float64(upper))
					clears = clears && item.LowerBound > upper
				} else {
					values = append(values, float64(upper)/float64(bs.TotalWeight()))
					left := new(big.Rat).SetFrac64(item.LowerBound, s.TotalWeight())
					right := new(big.Rat).SetFrac64(upper, bs.TotalWeight())
					clears = clears && left.Cmp(right) > 0
				}
			}
			current := ScanRange{float64(item.LowerBound), float64(min(s.TotalWeight(), item.UpperBound))}
			floor, minimum := 1.0, 10.0
			if !isErrors {
				current.Lower /= float64(s.TotalWeight())
				current.Upper /= float64(s.TotalWeight())
				floor = .01
				minimum = o.ShareChange
			}
			signal := baselineSignal(signalName, name, values, current, floor, minimum, o)
			signal.WeightBounds = &Interval{Lower: item.LowerBound, Upper: min(s.TotalWeight(), item.UpperBound)}
			signal.TotalWeight = s.TotalWeight()
			signal.Threshold = max(signal.Threshold, signal.BaselineUpper)
			if !isErrors {
				signal.Threshold = max(signal.Threshold, o.ShareFloor)
				if signal.BaselineUpper >= o.ShareFloor || !shareStrictlyAbove(item.LowerBound, s.TotalWeight(), o.ShareFloor) {
					continue
				}
			}
			if clears && current.Lower > signal.Threshold {
				findings = append(findings, ScanFinding{ScanSignal: signal, Direction: "up", key: item.Hash})
			}
		}
		signals = append(signals, ScanSignal{Signal: signalName, Measurement: name, Status: "evaluated", Samples: len(base), Reason: "Candidates were checked against per-key upper bounds in every baseline window."})
	}
	// A newly prominent leader already explains a top-share flag for the same
	// measurement and key. Keep one finding while retaining both signal checks.
	type leader struct {
		measurement string
		key         uint64
	}
	prominent := map[leader]bool{}
	for _, f := range findings {
		if f.Signal == "newly_prominent" {
			prominent[leader{f.Measurement, f.key}] = true
		}
	}
	findings = slices.DeleteFunc(findings, func(f ScanFinding) bool {
		return f.Signal == "top_share" && prominent[leader{f.Measurement, f.key}]
	})
	return signals, findings, nil
}

func shareStrictlyAbove(lower, total int64, threshold float64) bool {
	if total <= 0 {
		return false
	}
	cutoff, _ := new(big.Rat).SetString(strconv.FormatFloat(threshold, 'g', -1, 64))
	return new(big.Rat).SetFrac64(lower, total).Cmp(cutoff) > 0
}

func scalarValue(f scanFrame, name string, minAttempts uint64) (float64, bool) {
	n, ok := f.counters["requests"]
	if !ok {
		return 0, false
	}
	if name == "attempt_volume" {
		return float64(n), true
	}
	missing, ok := f.counters["missing_token_usage"]
	if !ok || missing > n || n < minAttempts {
		return 0, false
	}
	if name == "missing_usage_share" {
		return float64(missing) / float64(n), true
	}
	in, hasIn := f.counters["input_tokens"]
	out, hasOut := f.counters["output_tokens"]
	if !hasIn || !hasOut || missing != 0 {
		return 0, false
	}
	return (float64(in) + float64(out)) / float64(n), true
}
