// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package inspect

import (
	"errors"

	tracepb "go.opentelemetry.io/proto/slim/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const maxMessageObjects = 250000

var (
	spanDescriptor          = (&tracepb.Span{}).ProtoReflect().Descriptor()
	resourceSpansDescriptor = (&tracepb.ResourceSpans{}).ProtoReflect().Descriptor()
	scopeSpansDescriptor    = (&tracepb.ScopeSpans{}).ProtoReflect().Descriptor()
)

// Count structural objects before protobuf allocates them. Length limits alone
// do not bound the number of empty repeated messages an attacker can encode.
// Use generated descriptors and protobuf's wire parser, not a second decoder.
func preflightProto(data []byte) error {
	left := maxMessageObjects
	spans := 0
	return checkMessage(data, (&traceRequest{}).ProtoReflect().Descriptor(), 0, &left, &spans)
}

func checkMessage(data []byte, desc protoreflect.MessageDescriptor, depth int, left, spans *int) error {
	*left--
	if *left < 0 || depth > maxJSONDepth {
		return errors.New("protobuf capture exceeds structural limits")
	}
	if desc == spanDescriptor {
		*spans++
		if *spans > MaxSpans {
			return errors.New("capture exceeds 100000 spans")
		}
	}
	counts := map[protowire.Number]int{}
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return errEncoding
		}
		data = data[n:]
		if kind == protowire.StartGroupType || kind == protowire.EndGroupType {
			return errEncoding
		}
		length := protowire.ConsumeFieldValue(number, kind, data)
		if length < 0 {
			return errEncoding
		}
		field := desc.Fields().ByNumber(protoreflect.FieldNumber(number))
		if field != nil && field.Kind() == protoreflect.MessageKind {
			if kind != protowire.BytesType {
				return errEncoding
			}
			body, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return errEncoding
			}
			counts[number]++
			limit := 256
			switch field.Message() {
			case spanDescriptor:
				limit = MaxSpans
			case resourceSpansDescriptor, scopeSpansDescriptor:
				limit = 1024
			}
			if counts[number] > limit {
				return errors.New("protobuf repeated field exceeds structural limit")
			}
			if err := checkMessage(body, field.Message(), depth+1, left, spans); err != nil {
				return err
			}
		}
		data = data[length:]
	}
	return nil
}
