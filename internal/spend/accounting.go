// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"math/big"
	"regexp"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

// The initial profile supports Chat Completions and text completions only.
// Other call types remain counted as out of scope until their mappings are pinned.
func operation(v string) string {
	switch v {
	case "completion", "acompletion":
		return "chat"
	case "text_completion", "atext_completion":
		return "text_completion"
	default:
		return ""
	}
}

func attributes(row Row) map[string]*commonpb.AnyValue {
	a := map[string]*commonpb.AnyValue{}
	for _, f := range []struct{ column, attribute string }{
		{"prompt_tokens", "gen_ai.usage.input_tokens"},
		{"completion_tokens", "gen_ai.usage.output_tokens"},
		{"cache_read_input_tokens", "gen_ai.usage.cache_read.input_tokens"},
		{"cache_write_input_tokens", "gen_ai.usage.cache_write.input_tokens"},
		{"reasoning_output_tokens", "gen_ai.usage.reasoning.output_tokens"},
	} {
		if v, ok := row.Values[f.column]; ok && v != "" {
			a[f.attribute] = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}
		}
	}
	a["gen_ai.operation.name"] = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: operation(row.Values["call_type"])}}
	return a
}

type Quality struct {
	Missing          uint64 `json:"missing_token_fields"`
	Invalid          uint64 `json:"invalid_usage"`
	TotalMismatch    uint64 `json:"component_total_mismatch"`
	TotalMissing     uint64 `json:"missing_total_field"`
	ZeroUnknown      uint64 `json:"zero_only_origin_unknown"`
	Failed           uint64 `json:"failed_records"`
	UnknownStatus    uint64 `json:"unknown_status"`
	Unclear          uint64 `json:"unclear_usage_records"`
	MissingIdentity  uint64 `json:"missing_group_identity"`
	SpendMissing     uint64 `json:"missing_recorded_spend"`
	SpendInvalid     uint64 `json:"invalid_recorded_spend"`
	SpendZeroUnknown uint64 `json:"zero_spend_origin_unknown"`
}

func classify(row Row, a accounting.Observation) Quality {
	q := Quality{}
	if a.Metrics[accounting.Prefix+"missing_token_usage_total"] > 0 {
		q.Missing = 1
	}
	for k, v := range a.Quality {
		if v > 0 && (strings.HasSuffix(k, "/invalid") || strings.HasSuffix(k, "/conflict") || strings.HasSuffix(k, "/subset_violation")) {
			q.Invalid = 1
		}
	}
	total, ok, err := accounting.Number(&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: row.Values["total_tokens"]}})
	if row.Values["total_tokens"] == "" {
		q.TotalMissing = 1
	} else if !ok || err != nil {
		q.Invalid = 1
	} else if total != a.Metrics[accounting.Prefix+"total_tokens_total"] {
		q.TotalMismatch = 1
	}
	if q.Missing == 0 && q.Invalid == 0 && a.Metrics[accounting.Prefix+"total_tokens_total"] == 0 {
		q.ZeroUnknown = 1
	}
	switch row.Values["status"] {
	case "failure":
		q.Failed = 1
	case "success":
	default:
		q.UnknownStatus = 1
	}
	if q.Missing+q.Invalid+q.TotalMismatch+q.TotalMissing+q.ZeroUnknown+q.Failed+q.UnknownStatus > 0 {
		q.Unclear = 1
	}
	return q
}

func (q *Quality) add(v Quality) {
	q.Missing += v.Missing
	q.Invalid += v.Invalid
	q.TotalMismatch += v.TotalMismatch
	q.TotalMissing += v.TotalMissing
	q.ZeroUnknown += v.ZeroUnknown
	q.Failed += v.Failed
	q.UnknownStatus += v.UnknownStatus
	q.Unclear += v.Unclear
	q.MissingIdentity += v.MissingIdentity
	q.SpendMissing += v.SpendMissing
	q.SpendInvalid += v.SpendInvalid
	q.SpendZeroUnknown += v.SpendZeroUnknown
}

var decimalPattern = regexp.MustCompile(`^[0-9]{1,19}(\.[0-9]{1,20})?([eE][+-]?[0-9]{1,3})?$`)
var maxCost = new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// SQL float values arrive as decimal text. Preserve that text's exact value,
// including subnormal doubles, with bounded precision and no binary rounding.
func costValue(s string) (*big.Rat, bool) {
	if len(s) > 48 || !decimalPattern.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 || r.Cmp(maxCost) > 0 {
		return nil, false
	}
	if digits, exact := r.FloatPrec(); !exact || digits > 344 {
		return nil, false
	}
	return r, true
}

func costText(n *big.Rat) string {
	digits, _ := n.FloatPrec() // Every accepted input has a terminating decimal.
	return n.FloatString(digits)
}
