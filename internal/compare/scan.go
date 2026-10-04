// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const minScanBaseline = 6
const maxScanCandidates = 4096
const maxScanComparisons = 2_000_000

type ScanOptions struct {
	Expected                                                   []string
	Baseline, Recent, Persist, Top                             int
	BaselineDuration, Now, MaxAge                              int64
	MinAttempts                                                uint64
	Z, RelativeChange, ShareFloor, ShareChange, CoverageChange float64
	ShowHashes                                                 bool
}

func DefaultScanOptions() ScanOptions {
	return ScanOptions{Baseline: 24, Recent: 1, Persist: 1, Top: 20, MinAttempts: 100,
		Z: 4, RelativeChange: .5, ShareFloor: .25, ShareChange: .1, CoverageChange: .05}
}

type ScanRange struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

type ScanSignal struct {
	Signal         string     `json:"signal"`
	Measurement    string     `json:"measurement,omitempty"`
	Status         string     `json:"status"`
	Reason         string     `json:"reason,omitempty"`
	Current        *ScanRange `json:"current,omitempty"`
	WeightBounds   *Interval  `json:"weight_bounds,omitempty"`
	TotalWeight    int64      `json:"total_weight,omitempty"`
	Median         float64    `json:"baseline_median"`
	MAD            float64    `json:"baseline_mad"`
	BaselineUpper  float64    `json:"baseline_upper"`
	Threshold      float64    `json:"threshold"`
	RequiredChange float64    `json:"minimum_change"`
	Score          float64    `json:"robust_score"`
	Samples        int        `json:"baseline_samples"`
}

type ScanFinding struct {
	ScanSignal
	ID        string `json:"id"`
	Item      string `json:"item,omitempty"`
	Hash      string `json:"hash,omitempty"`
	Direction string `json:"direction"`
	key       uint64
}

type ScannedWindow struct {
	Start              int64         `json:"start_unix_nano"`
	End                int64         `json:"end_unix_nano"`
	Status             string        `json:"status"`
	Signals            []ScanSignal  `json:"signals"`
	Findings           []ScanFinding `json:"findings"`
	OmittedFindings    int           `json:"omitted_findings"`
	PendingPersistence int           `json:"pending_persistence"`
}

type ScanReport struct {
	Schema            string          `json:"schema"`
	Status            string          `json:"status"`
	AsOf              int64           `json:"as_of_unix_nano"`
	BaselineStart     int64           `json:"baseline_start_unix_nano"`
	BaselineEnd       int64           `json:"baseline_end_unix_nano"`
	BaselineWindows   int             `json:"baseline_windows"`
	RequestedBaseline int             `json:"requested_baseline_windows"`
	UnusualWindows    int             `json:"unusual_windows"`
	Windows           []ScannedWindow `json:"windows"`
	Thresholds        ScanThresholds  `json:"thresholds"`
	Notes             []string        `json:"notes"`
}

type ScanThresholds struct {
	Z              float64 `json:"robust_score"`
	RelativeChange float64 `json:"relative_change"`
	ShareFloor     float64 `json:"share_floor"`
	ShareChange    float64 `json:"share_change"`
	CoverageChange float64 `json:"coverage_change"`
	MinAttempts    uint64  `json:"min_attempts"`
	Persist        int     `json:"persistence_windows"`
	MaxAge         int64   `json:"max_age_nano"`
}

type scanFrame struct {
	start, end, emitted int64
	complete            bool
	counters            map[string]uint64
	sketches            map[string]*frequentitems.Sketch
	upperBounds         map[string]int64
}

var scanMeasurements = []string{"top_sessions", "top_sessions_requests", "top_users", "top_users_requests", "top_prompts", "top_prompts_requests", "top_tool_errors"}

func ValidateScanOptions(o ScanOptions) error {
	if o.Baseline < minScanBaseline || o.Baseline > 384 || o.Recent < 1 || o.Recent > 128 || o.Persist < 1 || o.Persist > 32 || o.Top < 1 || o.Top > 100 || o.MinAttempts == 0 || o.MinAttempts > math.MaxInt64 || o.Now < 0 || o.BaselineDuration < 0 || o.MaxAge < 0 {
		return errors.New("invalid scan limits; use scan --help")
	}
	for _, v := range []float64{o.Z, o.RelativeChange, o.ShareFloor, o.ShareChange, o.CoverageChange} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			return errors.New("scan thresholds must be finite and positive")
		}
	}
	if o.Z > 100 || o.RelativeChange > 100 || o.ShareFloor > 1 || o.ShareChange > 1 || o.CoverageChange > 1 {
		return errors.New("scan thresholds exceed supported ranges")
	}
	return nil
}

// Scan evaluates closed windows against a fixed preceding baseline. It never
// mutates envelopes or substitutes a zero for a missing window or measurement.
func Scan(input []summary.Envelope, o ScanOptions) (ScanReport, error) {
	if err := ValidateScanOptions(o); err != nil {
		return ScanReport{}, err
	}
	frames, duration, err := scanFrames(input, o.Expected)
	if err != nil {
		return ScanReport{}, err
	}
	if o.BaselineDuration > 0 {
		if o.BaselineDuration%duration != 0 {
			return ScanReport{}, errors.New("baseline duration must be a whole number of summary windows")
		}
		n := o.BaselineDuration / duration
		if n < minScanBaseline || n > 384 {
			return ScanReport{}, errors.New("baseline duration must cover 6 to 384 windows")
		}
		o.Baseline = int(n)
	}
	if o.MaxAge == 0 {
		o.MaxAge = math.MaxInt64
		if duration <= math.MaxInt64/3 {
			o.MaxAge = 3 * duration
		}
	}
	r := ScanReport{Schema: "fleetdiff-scan/v1", Status: "evaluated", AsOf: o.Now, RequestedBaseline: o.Baseline, Windows: []ScannedWindow{},
		Thresholds: ScanThresholds{o.Z, o.RelativeChange, o.ShareFloor, o.ShareChange, o.CoverageChange, o.MinAttempts, o.Persist, o.MaxAge},
		Notes:      []string{"The baseline is preceding observed history, not a seasonal model or a calibrated probability of anomaly.", "Shares describe attributed weight, excluding missing keys. Flags identify contributors for review, not loop causes or invoice amounts.", "Finding IDs support external alert deduplication. This command stores no alert state."}}
	closed := frames[:0]
	for _, f := range frames {
		if f.end <= o.Now {
			closed = append(closed, f)
		}
	}
	if len(closed) == 0 {
		r.Status = "not_ready"
		r.Notes = append(r.Notes, "No closed windows are available.")
		return r, nil
	}
	recentStart := max(0, len(closed)-o.Recent)
	checkStart := max(0, recentStart-o.Persist+1)
	baselineStart := max(0, checkStart-o.Baseline)
	base := closed[baselineStart:checkStart]
	r.BaselineWindows = len(base)
	if len(base) > 0 {
		r.BaselineStart = base[0].start
		r.BaselineEnd = base[len(base)-1].end
	}
	problem := ""
	switch {
	case len(base) < minScanBaseline:
		problem = "Not enough history: at least six preceding windows are required."
	case o.Now-closed[len(closed)-1].end > o.MaxAge:
		problem = "Summary history is stale; check collection and archive freshness."
	}
	for i := baselineStart; i < len(closed); i++ {
		if closed[i].emitted > o.Now {
			problem = "A selected snapshot was emitted after the reference time; check clocks."
		}
		if i > baselineStart && closed[i-1].end != closed[i].start {
			problem = "History has a missing window; restore the gap before comparing this period."
		}
		if i < checkStart && !closed[i].complete {
			problem = "The baseline has missing producers or partial observation intervals."
		}
	}
	if problem != "" {
		r.Status = "not_ready"
		r.Notes = append(r.Notes, problem)
		for _, f := range closed[recentStart:] {
			r.Windows = append(r.Windows, ScannedWindow{Start: f.start, End: f.end, Status: "not_ready", Signals: []ScanSignal{}, Findings: []ScanFinding{}})
		}
		return r, nil
	}
	inventory := slices.Clone(o.Expected)
	slices.Sort(inventory)
	identity, _ := json.Marshal([]any{input[0].ScopeID, input[0].AccountingID, input[0].KeyID, inventory})
	contextID := sha256.Sum256(identity)
	streaks := map[string]int{}
	work := 0
	for i := checkStart; i < len(closed); i++ {
		f := closed[i]
		w := ScannedWindow{Start: f.start, End: f.end, Status: "evaluated", Signals: []ScanSignal{}, Findings: []ScanFinding{}}
		var candidates []ScanFinding
		if !f.complete {
			w.Status = "incomplete"
			w.Signals = append(w.Signals, ScanSignal{Signal: "observation_coverage", Status: "unavailable", Reason: "Missing producers or partial observation intervals."})
		} else {
			w.Signals, candidates, err = scanSignals(base, f, o, &work)
			if err != nil {
				return ScanReport{}, err
			}
			for _, s := range w.Signals {
				if s.Status == "coverage_limited" {
					w.Status = "limited"
				}
			}
		}
		next := map[string]int{}
		for _, finding := range candidates {
			key := fmt.Sprintf("%s/%s/%d/%s", finding.Signal, finding.Measurement, finding.key, finding.Direction)
			next[key] = streaks[key] + 1
			if next[key] < o.Persist {
				w.PendingPersistence++
				continue
			}
			digest := sha256.Sum256([]byte(fmt.Sprintf("fleetdiff-scan/v1/%x/%d/%d/%s", contextID, f.start, duration, key)))
			finding.ID = fmt.Sprintf("%x", digest[:16])
			w.Findings = append(w.Findings, finding)
		}
		streaks = next
		if i < recentStart {
			continue
		}
		if w.Status != "evaluated" {
			r.Status = "limited"
		}
		if len(w.Findings) > 0 {
			r.UnusualWindows++
		}
		if len(w.Findings) > o.Top {
			w.OmittedFindings = len(w.Findings) - o.Top
			w.Findings = w.Findings[:o.Top]
		}
		r.Windows = append(r.Windows, w)
	}
	assignScanAliases(&r, o.ShowHashes)
	return r, nil
}

func scanFrames(input []summary.Envelope, expected []string) ([]scanFrame, int64, error) {
	if len(input) == 0 {
		return nil, 0, errors.New("scan needs summary snapshots")
	}
	if err := validateWindow(input); err != nil {
		return nil, 0, errors.New("scan: " + summaryCause(err))
	}
	groups := map[int64][]summary.Envelope{}
	var starts []int64
	for _, doc := range input {
		if err := summary.Compatible(input[0], doc); err != nil {
			return nil, 0, errors.New("scan: " + summaryCause(err))
		}
		if _, ok := groups[doc.WindowStart]; !ok {
			starts = append(starts, doc.WindowStart)
		}
		groups[doc.WindowStart] = append(groups[doc.WindowStart], doc)
	}
	slices.Sort(starts)
	var frames []scanFrame
	for _, start := range starts {
		combined, err := summary.Combine(groups[start], expected)
		if err != nil {
			return nil, 0, errors.New("scan: " + summaryCause(err))
		}
		f := scanFrame{start: start, end: start + input[0].WindowDuration, complete: len(combined.Missing)+len(combined.Partial) == 0, counters: combined.Counters, sketches: map[string]*frequentitems.Sketch{}, upperBounds: map[string]int64{}}
		for _, doc := range groups[start] {
			f.emitted = max(f.emitted, doc.EmittedAt)
		}
		for _, name := range scanMeasurements {
			payload, ok := combined.Sketches[name]
			if !ok {
				continue
			}
			if payload.Kind != "frequent_items" {
				return nil, 0, errors.New("scan measurement has an unexpected sketch kind")
			}
			s, err := frequentitems.Parse(payload.Data)
			if err != nil {
				return nil, 0, errors.New("invalid scan sketch")
			}
			items, err := s.FrequentItems(frequentitems.NoFalseNegatives)
			if err != nil || len(items) > maxScanCandidates {
				return nil, 0, errors.New("scan sketch exceeds 4096 candidate limit")
			}
			f.sketches[name] = s
			upper := min(s.TotalWeight(), s.MaxError())
			for _, item := range items {
				upper = max(upper, min(s.TotalWeight(), item.UpperBound))
			}
			f.upperBounds[name] = upper
		}
		frames = append(frames, f)
	}
	return frames, input[0].WindowDuration, nil
}

func assignScanAliases(r *ScanReport, hashes bool) {
	keys := map[string][]uint64{}
	for _, w := range r.Windows {
		for _, f := range w.Findings {
			if f.Measurement == "" {
				continue
			}
			prefix := scanAliasPrefix(f.Measurement)
			if !slices.Contains(keys[prefix], f.key) {
				keys[prefix] = append(keys[prefix], f.key)
			}
		}
	}
	for _, ids := range keys {
		slices.Sort(ids)
	}
	for i := range r.Windows {
		for j := range r.Windows[i].Findings {
			f := &r.Windows[i].Findings[j]
			if f.Measurement == "" {
				continue
			}
			prefix := scanAliasPrefix(f.Measurement)
			f.Item = prefix + "-" + strconv.Itoa(slices.Index(keys[prefix], f.key)+1)
			if hashes {
				f.Hash = fmt.Sprintf("%016x", f.key)
			}
		}
	}
}

func scanAliasPrefix(name string) string {
	switch {
	case strings.Contains(name, "sessions"):
		return "session"
	case strings.Contains(name, "users"):
		return "user"
	case name == "top_tool_errors":
		return "error"
	default:
		return "prompt"
	}
}
