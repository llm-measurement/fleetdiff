// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package clauderecords

import (
	"math/big"
	"strconv"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

type observation struct {
	input, output, total, read, write *uint64
	cost                              *big.Rat
	costDelta                         *big.Rat
	costKind                          string
	quality                           map[string]uint64
}

// Numeric OTLP values are parsed exactly before the shared token contract sees
// them. An invalid row is excluded; only aggregate overflow aborts a report.
func number(v value) (uint64, bool) {
	if v.invalid || len(v.text) > 32 {
		return 0, false
	}
	if v.kind == "doubleValue" {
		if strings.ContainsAny(v.text, "xXpP/") {
			return 0, false
		}
		if i := strings.IndexAny(v.text, "eE"); i >= 0 {
			e, err := strconv.Atoi(v.text[i+1:])
			if err != nil || e < -400 || e > 400 {
				return 0, false
			}
		}
		if _, err := strconv.ParseFloat(v.text, 64); err != nil {
			return 0, false
		}
		n, ok := new(big.Rat).SetString(v.text)
		if !ok || n.Sign() < 0 || !n.IsInt() || !n.Num().IsInt64() {
			return 0, false
		}
		return n.Num().Uint64(), true
	}
	if v.kind != "stringValue" && v.kind != "intValue" {
		return 0, false
	}
	n, err := strconv.ParseUint(v.text, 10, 63)
	return n, err == nil
}

func normalize(r record) observation {
	o := observation{quality: map[string]uint64{}}
	var parts [4]*uint64
	for i, name := range []string{"input_tokens", "cache_read_tokens", "cache_creation_tokens", "output_tokens"} {
		v, present := r.resolve(name)
		if !present {
			o.quality[name+"_missing"]++
			continue
		}
		n, ok := number(v)
		if !ok {
			o.quality[name+"_invalid"]++
			continue
		}
		parts[i] = &n
		if n == 0 {
			o.quality["zero_token_origin_unknown"] = 1
		}
	}
	if parts[0] != nil && parts[1] != nil && parts[2] != nil {
		n, err := accounting.AddBounded(*parts[0], *parts[1])
		if err == nil {
			n, err = accounting.AddBounded(n, *parts[2])
		}
		if err == nil {
			o.input = &n
		} else {
			o.quality["input_sum_invalid"] = 1
		}
	}
	o.read, o.write, o.output = parts[1], parts[2], parts[3]
	attrs := map[string]*commonpb.AnyValue{}
	for name, n := range map[string]*uint64{"input_tokens": o.input, "output_tokens": o.output, "cache_read.input_tokens": o.read, "cache_write.input_tokens": o.write} {
		if n != nil {
			attrs["gen_ai.usage."+name] = &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(*n)}}
		}
	}
	a, err := accounting.Tokens(attrs)
	if err != nil {
		o.quality["total_sum_invalid"] = 1
	} else if o.input != nil && o.output != nil {
		n := a.Metrics[accounting.Prefix+"total_tokens_total"]
		o.total = &n
	}
	v, hasDecimal := r.resolve("cost_usd")
	if hasDecimal {
		if !v.invalid {
			o.cost, _ = accounting.Cost(v.text)
		}
		if o.cost == nil {
			o.quality["cost_invalid"] = 1
		} else {
			o.costKind = v.kind
			o.quality["cost_decimal"] = 1
			if micros, present := r.resolve("cost_usd_micros"); present {
				if n, ok := number(micros); ok {
					m := new(big.Rat).SetFrac(new(big.Int).SetUint64(n), big.NewInt(1_000_000))
					o.costDelta = new(big.Rat).Sub(o.cost, m)
				} else {
					o.quality["cost_micros_invalid"] = 1
				}
			}
		}
	} else if v, ok := r.resolve("cost_usd_micros"); ok {
		if n, valid := number(v); valid {
			o.cost = new(big.Rat).SetFrac(new(big.Int).SetUint64(n), big.NewInt(1_000_000))
			o.quality["cost_micros_fallback"] = 1
			o.costKind = v.kind
		} else {
			o.quality["cost_invalid"] = 1
		}
	} else {
		o.quality["cost_missing"] = 1
	}
	if o.cost != nil && o.cost.Sign() == 0 {
		o.quality["zero_cost_origin_unknown"] = 1
	}
	return o
}
