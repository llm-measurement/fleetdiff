// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"math"
	"testing"

	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

func integer(v int64) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v}}
}
func textValue(v string) *commonpb.AnyValue {
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}
}

func TestUnavailableAndAliasAccounting(t *testing.T) {
	attrs := map[string]*commonpb.AnyValue{
		"gen_ai.usage.input_tokens":             integer(0),
		"gen_ai.usage.output_tokens":            integer(12),
		"gen_ai_sketch.usage.input.provenance":  textValue("unavailable"),
		"gen_ai_sketch.usage.output.provenance": textValue("provider_reported"),
	}
	a, err := accountTokens(attrs)
	if err != nil {
		t.Fatal(err)
	}
	if a.Metrics[metricPrefix+"missing_token_usage_total"] != 1 || a.Metrics[metricPrefix+"output_tokens_total"] != 12 || a.Provenance["input/unavailable"] != 1 {
		t.Fatalf("unavailable accounting: %+v", a)
	}
	attrs["gen_ai.usage.input_tokens"] = textValue("bad")
	attrs["gen_ai.usage.prompt_tokens"] = integer(9)
	delete(attrs, "gen_ai_sketch.usage.input.provenance")
	attrs["gen_ai.usage.cache_read.input_tokens"] = integer(10)
	a, err = accountTokens(attrs)
	if err != nil {
		t.Fatal(err)
	}
	if a.Metrics[metricPrefix+"total_tokens_total"] != 21 || a.Quality["input/invalid"] != 1 || a.Quality["cache_read_input/subset_violation"] != 1 {
		t.Fatalf("alias/subset accounting: %+v", a)
	}
}

func TestTokenOverflowAndInvalids(t *testing.T) {
	for _, v := range []*commonpb.AnyValue{integer(-1), textValue("-1"), textValue("no"), {Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 0.5}}} {
		_, ok, err := tokenNumber(v)
		if ok || err != nil {
			t.Fatalf("invalid should be missing: %v %v", ok, err)
		}
	}
	for _, v := range []*commonpb.AnyValue{textValue("9223372036854775808"), {Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.Inf(1)}}} {
		if _, _, err := tokenNumber(v); err == nil {
			t.Fatal("overflow accepted")
		}
	}
	if _, err := accountTokens(map[string]*commonpb.AnyValue{"gen_ai.usage.input_tokens": integer(math.MaxInt64), "gen_ai.usage.output_tokens": integer(1)}); err == nil {
		t.Fatal("sum overflow accepted")
	}
}
