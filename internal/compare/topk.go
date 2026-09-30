// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"errors"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

const DefaultFlagShare = 0.25

// Names determine units; top_key.FIELD.WEIGHT extensions remain unknown.
// Keep the legacy prompt unit unchanged for existing report consumers.
var topKUnits = map[string]string{
	"top_prompts": "configured-weight",
	"top_users":   "attributed-reported-tokens", "top_sessions": "attributed-reported-tokens",
	"top_docs": "attributed-reported-tokens", "top_mcp_sessions": "attributed-reported-tokens",
	"top_mcp_methods": "attributed-reported-tokens", "top_mcp_resources": "attributed-reported-tokens",
	"top_prompts_requests": "attributed-model-attempts", "top_users_requests": "attributed-model-attempts",
	"top_sessions_requests": "attributed-model-attempts", "top_docs_requests": "attributed-model-attempts",
	"top_mcp_sessions_requests": "attributed-model-attempts", "top_mcp_methods_requests": "attributed-model-attempts",
	"top_mcp_resources_requests": "attributed-model-attempts",
}

func flagShare(options Options) (float64, error) {
	v := options.FlagShare
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1 {
		return 0, errors.New("flag-share must be a finite decimal between 0 and 1")
	}
	if v == 0 && !options.FlagShareSet {
		v = DefaultFlagShare
	}
	return v, nil
}

func isTopKMarker(name string) bool { return strings.HasPrefix(name, "topk_contract.v1.") }

func markerMeasurement(name string) (string, bool) {
	if !isTopKMarker(name) {
		return "", false
	}
	rest := strings.TrimPrefix(name, "topk_contract.v1.")
	i := strings.LastIndexByte(rest, '.')
	if i <= 0 || len(rest[i+1:]) != 32 {
		return "", false
	}
	for _, c := range rest[i+1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return rest[:i], true
}

func validateTopK(e summary.Envelope) error {
	markers := map[string]bool{}
	for name, value := range e.Counters {
		if !isTopKMarker(name) {
			continue
		}
		measurement, ok := markerMeasurement(name)
		if !ok || value != 0 || markers[measurement] {
			return errors.New("invalid top-k contract marker")
		}
		if _, present := e.Sketches[measurement]; !present {
			return errors.New("invalid top-k contract marker")
		}
		markers[measurement] = true
	}
	for name, payload := range e.Sketches {
		if _, known := topKUnits[name]; !known {
			continue
		}
		if payload.Kind != "frequent_items" {
			return errors.New("known measurement has an unexpected sketch kind")
		}
		if name != "top_prompts" && !markers[name] {
			return errors.New("missing top-k contract marker")
		}
	}
	return nil
}

// Only called after both original batches pass validation. Check every present
// contract even if a third snapshot will cause the measurement to be dropped.
func projectTopK(before, after []summary.Envelope) ([]summary.Envelope, []summary.Envelope, []string, error) {
	counts := map[string]int{}
	contracts := map[string]summary.Envelope{}
	for _, window := range [][]summary.Envelope{before, after} {
		for _, doc := range window {
			for name := range topKUnits {
				payload, present := doc.Sketches[name]
				if !present {
					continue
				}
				counts[name]++
				// Compose the existing compatibility check over just this contract.
				probe := before[0]
				probe.Sketches = map[string]summary.Payload{name: payload}
				probe.Counters = map[string]uint64{}
				for marker, value := range doc.Counters {
					if measurement, ok := markerMeasurement(marker); ok && measurement == name {
						probe.Counters[marker] = value
					}
				}
				if previous, ok := contracts[name]; ok {
					if err := summary.Compatible(previous, probe); err != nil {
						return nil, nil, nil, err
					}
				} else {
					contracts[name] = probe
				}
			}
		}
	}
	dropped := []string{}
	for name, count := range counts {
		if count != len(before)+len(after) {
			dropped = append(dropped, name)
		}
	}
	slices.Sort(dropped)
	project := func(input []summary.Envelope) []summary.Envelope {
		if len(dropped) == 0 {
			return input
		}
		result := slices.Clone(input)
		for i := range result {
			result[i].Sketches = maps.Clone(input[i].Sketches)
			result[i].Counters = maps.Clone(input[i].Counters)
			for _, name := range dropped {
				delete(result[i].Sketches, name)
			}
			for marker := range result[i].Counters {
				if name, ok := markerMeasurement(marker); ok && slices.Contains(dropped, name) {
					delete(result[i].Counters, marker)
				}
			}
		}
		return result
	}
	return project(before), project(after), dropped, nil
}
