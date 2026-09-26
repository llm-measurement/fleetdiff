// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package compare

import (
	"errors"
	"maps"
	"strings"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func hasUsageProvenance(input []summary.Envelope) bool {
	for _, doc := range input {
		for name := range doc.Counters {
			if strings.HasPrefix(name, "usage_provenance.v1.") {
				return true
			}
		}
	}
	return false
}

// Normalize only the known optional extension, in memory. The wire contract's
// strict compatibility checks still apply to all other counters and metadata.
func withUsageProvenance(input []summary.Envelope) ([]summary.Envelope, error) {
	if len(input) > MaxFiles {
		return nil, errors.New("invalid summary batch size")
	}
	result := make([]summary.Envelope, len(input))
	remaining := MaxInputBytes
	for i, doc := range input {
		for _, payload := range doc.Sketches {
			if len(payload.Data) > remaining {
				return nil, errors.New("summary batch exceeds size limit")
			}
			remaining -= len(payload.Data)
		}
		if err := doc.Validate(); err != nil {
			return nil, err
		}
		result[i] = doc
		present := false
		for name := range doc.Counters {
			if strings.HasPrefix(name, "usage_provenance.") {
				present = true
			}
		}
		if present {
			continue // Never repair partial or unknown-version declarations.
		}
		result[i].Counters = maps.Clone(doc.Counters)
		for _, field := range []string{"input", "output"} {
			for _, source := range []string{"provider_reported", "inferred", "unavailable", "unknown"} {
				var count uint64
				if source == "unknown" {
					count = doc.Counters["requests"]
				}
				result[i].Counters["usage_provenance.v1."+field+"."+source] = count
			}
		}
	}
	return result, nil
}
