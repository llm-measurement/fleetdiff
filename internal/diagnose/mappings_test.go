// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"strings"
	"testing"
)

func TestMappings(t *testing.T) {
	safe := fixture(t, "safe")
	for _, tc := range []struct {
		name, old, replacement, finding string
		code                            int
	}{
		{"missing-filter", "    operation_filter:\n      llm_operations: [chat, generate_content, text_completion, embeddings]\n", "", "missing_operation_filter", 3},
		{"empty-filter", "[chat, generate_content, text_completion, embeddings]", "[]", "unsafe_operation_filter", 3},
		{"unknown-operation", "text_completion", "custom_operation", "unknown_operation", 4},
		{"high-cardinality", "keys: [gen_ai.request.model]", "keys: [service.instance.id]", "high_cardinality_slice", 3},
		{"unknown-slice", "keys: [gen_ai.request.model]", "keys: [custom.dimension]", "unknown_slice_source", 4},
		{"resource-overlap", "keys: [gen_ai.request.model]", "keys: [enduser.id]\n        from_resource_attributes: [enduser.id]", "hashed_source_overlap", 3},
		{"configured-overlap", "    slices:", "    fields:\n      user_key:\n        from_attributes: [gen_ai.request.model]\n        canonicalization: text_v1\n        domain: user:v1\n    slices:", "hashed_source_overlap", 4},
		{"custom-resource-overlap", "    slices:", "    fields:\n      user_key:\n        from_resource_attributes: [gen_ai.request.model]\n    slices:", "hashed_source_overlap", 4},
		{"token-subset", "    slices:", "    weights: {input_tokens_from: [gen_ai.usage.cache_read.input_tokens]}\n    slices:", "unsafe_token_mapping", 3},
		{"token-direction", "    slices:", "    weights: {input_tokens_from: [gen_ai.usage.output_tokens]}\n    slices:", "unsafe_token_mapping", 3},
		{"empty-tokens", "    slices:", "    weights: {input_tokens_from: []}\n    slices:", "unsafe_token_mapping", 3},
		{"unknown-tokens", "    slices:", "    weights: {input_tokens_from: [custom.tokens]}\n    slices:", "unknown_token_mapping", 4},
		{"hashing", "    slices:", "    hashing: {algo: sha256}\n    slices:", "unsafe_hashing", 3},
		{"secret-interpolation", "    slices:", "    hashing: {secret_env: '${env:PRIVATE}'}\n    slices:", "unresolved_interpolation", 4},
		{"unknown-nested", "timeout: 1s", "timeout: 1s, custom: secret", "unsupported_field", 4},
		{"unknown-processor", "  batch: {timeout: 1s}", "  batch: {timeout: 1s}\n  custom: {}", "unsupported_component", 4},
		{"wrong-reference", "exporters: [genaisketch]", "exporters: [absent]", "pipeline_wiring", 3},
		{"wrong-direction", "receivers: [genaisketch]", "receivers: [otlp]", "pipeline_wiring", 3},
		{"duplicate-reference", "receivers: [otlp]", "receivers: [otlp, otlp]", "pipeline_wiring", 3},
		{"wrong-output", "exporters: [genaisketch]", "exporters: [prometheus]", "pipeline_wiring", 3},
		{"unknown-signal", "    traces:", "    logs:", "unsupported_component", 4},
		{"include", "    slices:", "    fields: !include private.yaml\n    slices:", "unsupported_mapping", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := check(t, strings.Replace(safe, tc.old, tc.replacement, 1))
			if r.ExitCode() != tc.code || !hasFinding(r, tc.finding) {
				t.Fatalf("want %d/%s, got %+v", tc.code, tc.finding, r)
			}
		})
	}
}

func TestInvalidSupportedSettingsRemainIndeterminate(t *testing.T) {
	safe := fixture(t, "safe")
	for _, change := range []struct{ old, replacement string }{
		{"timeout: 1s", "timeout: 1s, send_batch_max_size: 1"},
		{"genaisketch:", "genaisketch/:"},
		{"    slices:", "    fields: {session_key: {from_attributes: []}}\n    slices:"},
		{"    slices:", "    topk_keys: [{field: user_key, weight: tokens}, {field: user_key, weight: requests}]\n    slices:"},
		{"    slices:", "    summary_export: {directory: ./private, producer_id: invalid/name, scope_id: scope, key_id: key}\n    slices:"},
	} {
		if r := check(t, strings.Replace(safe, change.old, change.replacement, 1)); r.ExitCode() != 4 {
			t.Fatalf("invalid config accepted: %+v", r)
		}
	}
}

func TestMultipleConnectorInputs(t *testing.T) {
	input := fixture(t, "safe") + "    \n"
	input = strings.Replace(input, "    metrics:", "    traces/duplicate:\n      receivers: [otlp]\n      exporters: [genaisketch]\n    metrics:", 1)
	r := check(t, input)
	if r.ExitCode() != 4 || !hasFinding(r, "multiple_producers") {
		t.Fatal("duplicate observations must not be certified")
	}
}

func TestBackendPreservedAndSecretsNotCopied(t *testing.T) {
	input := fixture(t, "safe")
	input = strings.Replace(input, "exporters:\n  prometheus:", "exporters:\n  otlp/backend:\n    endpoint: private.example:4317\n    headers: {authorization: PRIVATE_BACKEND_SECRET}\n  prometheus:", 1)
	input = strings.Replace(input, "exporters: [genaisketch]", "exporters: [genaisketch, otlp/backend]", 1)
	if r := check(t, input); r.ExitCode() != 0 {
		t.Fatalf("supported backend must coexist: %+v", r)
	}
	if strings.Contains(ShadowConfig(), "PRIVATE_BACKEND_SECRET") || strings.Contains(ShadowConfig(), "private.example") {
		t.Fatal("copied backend configuration")
	}
}
