// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"errors"
	"math"
	"strconv"

	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

// These default mappings follow the collector accounting contract. Both repos
// execute testdata/inspect-contract/v1; changes require a reviewed corpus update.
const AccountingID = "genai-default-accounting/v1+usage-provenance/v1"
const metricPrefix = "gen_ai_sketch_"

var tokenFields = []struct {
	name    string
	sources []string
	metric  string
}{
	{"input", []string{"gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens"}, "input_tokens_total"},
	{"output", []string{"gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens"}, "output_tokens_total"},
	{"cache_read_input", []string{"gen_ai.usage.cache_read.input_tokens"}, "cache_read_input_tokens_total"},
	{"cache_write_input", []string{"gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.cache_creation.input_tokens"}, "cache_write_input_tokens_total"},
	{"reasoning_output", []string{"gen_ai.usage.reasoning.output_tokens"}, "reasoning_output_tokens_total"},
}

var errOverflow = errors.New("observed token value, counter, or sketch weight exceeds its supported range")

type tokenObservation struct {
	value                       uint64
	reported, invalid, conflict bool
}
type accounting struct{ metrics, quality, provenance map[string]uint64 }

func accountTokens(attrs map[string]*commonpb.AnyValue) (accounting, error) {
	a := accounting{map[string]uint64{metricPrefix + "requests_total": 1}, map[string]uint64{}, map[string]uint64{}}
	var observations [5]tokenObservation
	for i, field := range tokenFields {
		for _, source := range field.sources {
			v, present := attrs[source]
			if !present {
				continue
			}
			n, valid, err := tokenNumber(v)
			if err != nil {
				return accounting{}, err
			}
			if !valid {
				observations[i].invalid = true
				continue
			}
			if !observations[i].reported {
				observations[i].value = n
				observations[i].reported = true
			} else if observations[i].value != n {
				observations[i].conflict = true
			}
		}
	}
	for i, name := range []string{"input", "output"} {
		source := stringAttribute(attrs, "gen_ai_sketch.usage."+name+".provenance")
		if source != "provider_reported" && source != "inferred" && source != "unavailable" {
			source = "unknown"
		}
		if (source == "provider_reported" || source == "inferred") && (!observations[i].reported || observations[i].invalid || observations[i].conflict) {
			source = "unknown"
		}
		a.provenance[name+"/"+source] = 1
		if source == "unavailable" {
			observations[i].value = 0
			observations[i].reported = false
		}
	}
	for i, field := range tokenFields {
		o := observations[i]
		a.metrics[metricPrefix+field.metric] = o.value
		if o.reported {
			a.quality[field.name+"/reported"] = 1
		} else if i < 2 {
			a.quality[field.name+"/missing"] = 1
			a.metrics[metricPrefix+"missing_token_usage_total"] = 1
		}
		if o.invalid {
			a.quality[field.name+"/invalid"] = 1
		}
		if o.conflict {
			a.quality[field.name+"/conflict"] = 1
		}
		if i >= 2 {
			parent := observations[0]
			if i == 4 {
				parent = observations[1]
			}
			if parent.reported && o.reported && o.value > parent.value {
				a.quality[field.name+"/subset_violation"] = 1
			}
		}
	}
	total, err := addBounded(observations[0].value, observations[1].value)
	if err != nil {
		return accounting{}, err
	}
	a.metrics[metricPrefix+"total_tokens_total"] = total
	return a, nil
}

func tokenNumber(v *commonpb.AnyValue) (uint64, bool, error) {
	if v == nil {
		return 0, false, nil
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_IntValue:
		if x.IntValue < 0 {
			return 0, false, nil
		}
		return uint64(x.IntValue), true, nil
	case *commonpb.AnyValue_DoubleValue:
		n := x.DoubleValue
		if n < 0 || math.IsNaN(n) || math.Trunc(n) != n {
			return 0, false, nil
		}
		if n >= float64(math.MaxInt64) {
			return 0, false, errOverflow
		}
		return uint64(n), true, nil
	case *commonpb.AnyValue_StringValue:
		n, err := strconv.ParseUint(x.StringValue, 10, 64)
		if err != nil {
			return 0, false, nil
		}
		if n > math.MaxInt64 {
			return 0, false, errOverflow
		}
		return n, true, nil
	default:
		return 0, false, nil
	}
}

func stringAttribute(attrs map[string]*commonpb.AnyValue, key string) string {
	v := attrs[key]
	if v == nil {
		return ""
	}
	return v.GetStringValue()
}

func modelAttempt(attrs map[string]*commonpb.AnyValue) bool {
	switch stringAttribute(attrs, "gen_ai.operation.name") {
	case "chat", "generate_content", "text_completion", "embeddings":
		return true
	case "":
		return stringAttribute(attrs, "gen_ai.request.model") != ""
	default:
		return false
	}
}

func addBounded(a, b uint64) (uint64, error) {
	if b > math.MaxInt64 || a > math.MaxInt64-b {
		return 0, errOverflow
	}
	return a + b, nil
}

func mergeCounts(dst, src map[string]uint64) error {
	for k, v := range src {
		if math.MaxUint64-dst[k] < v {
			return errOverflow
		}
		dst[k] += v
	}
	return nil
}
