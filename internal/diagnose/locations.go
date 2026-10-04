// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Use a closed schema vocabulary: custom names can themselves contain secrets.
var schemaKeys = strings.Fields(`receivers processors connectors exporters extensions service
otlp prometheus debug batch memory_limiter genaisketch pipelines traces metrics logs
protocols grpc http endpoint headers tls insecure insecure_skip_verify certificates
window_duration retention_windows max_slices topk topk_keys profiles hashing operation_filter
mcp slices fields weights dedup summary_export hllpp frequent_items bloom algo secret_env
llm_operations enabled tool_errors request_id_from name keys from_resource_attributes
from_attributes canonicalization domain field weight directory producer_id scope_id key_id
interval fallback_when_missing timeout send_batch_size send_batch_max_size check_interval
limit_mib spike_limit_mib verbosity telemetry logs level`)

func schemaPaths(root *yaml.Node) map[*yaml.Node]string {
	paths := make(map[*yaml.Node]string)
	var visit func(*yaml.Node, string)
	visit = func(n *yaml.Node, path string) {
		paths[n] = path
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i < len(n.Content); i += 2 {
				key, value := n.Content[i], n.Content[i+1]
				name := fmt.Sprintf("[key-%d]", i/2+1)
				_, field := fieldDomains[key.Value]
				_, weight := tokenSources[key.Value]
				if slices.Contains(schemaKeys, key.Value) || field || weight {
					name = key.Value
				}
				next := name
				if path != "" {
					next = path + "." + name
				}
				paths[key] = next
				visit(value, next)
			}
		case yaml.SequenceNode:
			for i, child := range n.Content {
				next := path
				// Scalar list items share a setting; line/column identifies each.
				if child.Kind != yaml.ScalarNode {
					next = fmt.Sprintf("%s[%d]", path, i)
				}
				visit(child, next)
			}
		}
	}
	visit(root, "")
	return paths
}

func knownAttribute(name string) bool {
	for _, sources := range defaultSources {
		if slices.Contains(sources, name) {
			return true
		}
	}
	for _, sources := range tokenSources {
		if slices.Contains(sources, name) {
			return true
		}
	}
	switch name {
	case "gen_ai.request.model", "gen_ai.response.model", "gen_ai.system", "gen_ai.provider.name", "gen_ai.operation.name", "service.name", "service.namespace", "deployment.environment.name", "team.id",
		"gen_ai.response.completion", "gen_ai.input.messages", "gen_ai.output.messages", "trace_id", "span_id", "trace.id", "span.id", "gen_ai.response.id", "request.id", "jsonrpc.request.id", "service.instance.id", "url.full", "gen_ai.usage.total_tokens":
		return true
	}
	return false
}
