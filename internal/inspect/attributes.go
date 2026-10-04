// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
	"google.golang.org/protobuf/proto"
)

func attributes(kvs []*commonpb.KeyValue) (map[string]*commonpb.AnyValue, error) {
	if len(kvs) > 256 {
		return nil, errors.New("attribute map exceeds 256 entries")
	}
	result := make(map[string]*commonpb.AnyValue, len(kvs))
	for _, kv := range kvs {
		if kv == nil || len(kv.Key) > 256 || !utf8.ValidString(kv.Key) {
			return nil, errors.New("invalid attribute key")
		}
		if _, exists := result[kv.Key]; exists {
			return nil, errors.New("duplicate attribute key")
		}
		if proto.Size(kv.Value) > 65536 {
			return nil, errors.New("attribute value exceeds 64 KiB")
		}
		if err := validateValue(kv.Value, 0); err != nil {
			return nil, err
		}
		result[kv.Key] = kv.Value
	}
	return result, nil
}

func validateValue(v *commonpb.AnyValue, depth int) error {
	if depth > 16 {
		return errors.New("attribute nesting exceeds 16 levels")
	}
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		if !utf8.ValidString(x.StringValue) {
			return errors.New("invalid UTF-8 attribute")
		}
	case *commonpb.AnyValue_ArrayValue:
		if len(x.ArrayValue.GetValues()) > 256 {
			return errors.New("attribute array exceeds 256 entries")
		}
		for _, v := range x.ArrayValue.GetValues() {
			if err := validateValue(v, depth+1); err != nil {
				return err
			}
		}
	case *commonpb.AnyValue_KvlistValue:
		values := x.KvlistValue.GetValues()
		if len(values) > 256 {
			return errors.New("attribute map exceeds 256 entries")
		}
		keys := map[string]bool{}
		for _, kv := range values {
			if kv == nil || len(kv.Key) > 256 || !utf8.ValidString(kv.Key) || keys[kv.Key] {
				return errors.New("invalid or duplicate nested attribute key")
			}
			keys[kv.Key] = true
			if err := validateValue(kv.Value, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// Match the default collector's scalar AsString conversion before text_v1.
// Complex identities are JSON-encoded; map keys are sorted by the stdlib encoder.
func attributeString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(x.IntValue, 10)
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(x.BoolValue)
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(x.BytesValue)
	case *commonpb.AnyValue_DoubleValue:
		if math.IsNaN(x.DoubleValue) {
			return "NaN"
		}
		if math.IsInf(x.DoubleValue, 1) {
			return "Infinity"
		}
		if math.IsInf(x.DoubleValue, -1) {
			return "-Infinity"
		}
		return jsonString(x.DoubleValue)
	case *commonpb.AnyValue_ArrayValue, *commonpb.AnyValue_KvlistValue:
		return jsonString(rawValue(v))
	default:
		return ""
	}
}

func rawValue(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch x := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return x.StringValue
	case *commonpb.AnyValue_IntValue:
		return x.IntValue
	case *commonpb.AnyValue_DoubleValue:
		return x.DoubleValue
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue
	case *commonpb.AnyValue_BytesValue:
		return x.BytesValue
	case *commonpb.AnyValue_ArrayValue:
		a := []any{}
		for _, v := range x.ArrayValue.GetValues() {
			a = append(a, rawValue(v))
		}
		return a
	case *commonpb.AnyValue_KvlistValue:
		m := map[string]any{}
		for _, kv := range x.KvlistValue.GetValues() {
			m[kv.Key] = rawValue(kv.Value)
		}
		return m
	default:
		return nil
	}
}

func jsonString(v any) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if e.Encode(v) != nil {
		return ""
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func publicAttribute(key string) bool {
	switch key {
	case "gen_ai.operation.name", "gen_ai.request.model", "gen_ai.response.model", "gen_ai.provider.name", "gen_ai.system", "service.name", "service.namespace", "service.instance.id", "deployment.environment.name", "team.id", "tenant.id", "agent.id", "agent.name", "workflow.name", "enduser.id", "user.id", "gen_ai.conversation.id", "session.id", "gen_ai.request.prompt", "gen_ai.tool.name", "gen_ai.tool.call.id", "mcp.session.id", "mcp.resource.uri", "jsonrpc.request.id", "mcp.method.name", "error.type":
		return true
	case "gen_ai_sketch.usage.input.provenance", "gen_ai_sketch.usage.output.provenance":
		return true
	}
	for _, f := range tokenFields {
		for _, source := range f.sources {
			if key == source {
				return true
			}
		}
	}
	return false
}

func sensitiveAttribute(key string) bool {
	switch key {
	case "enduser.id", "user.id", "gen_ai.conversation.id", "session.id", "gen_ai.request.prompt", "gen_ai.tool.call.id", "mcp.session.id", "mcp.resource.uri", "jsonrpc.request.id":
		return true
	}
	return false
}
