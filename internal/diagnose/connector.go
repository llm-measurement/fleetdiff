// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
	"go.yaml.in/yaml/v3"
)

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var defaultSources = map[string][]string{
	"user_key":         {"enduser.id", "user.id"},
	"session_key":      {"gen_ai.conversation.id", "session.id"},
	"prompt_key":       {"gen_ai.request.prompt"},
	"doc_key":          {"retrieval.doc_id"},
	"mcp_session_key":  {"mcp.session.id"},
	"mcp_method_key":   {"mcp.method.name"},
	"mcp_resource_key": {"mcp.resource.uri"},
}

var fieldDomains = map[string]string{
	"user_key": "user:v1", "session_key": "session:v1", "prompt_key": "prompt:v1", "doc_key": "retrieval-doc:v1",
	"mcp_session_key": "mcp-session:v1", "mcp_method_key": "mcp-method:v1", "mcp_resource_key": "retrieval-doc:v1",
}

var tokenSources = map[string][]string{
	"input_tokens_from":             {"gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens"},
	"output_tokens_from":            {"gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens"},
	"cache_read_input_tokens_from":  {"gen_ai.usage.cache_read.input_tokens"},
	"cache_write_input_tokens_from": {"gen_ai.usage.cache_write.input_tokens", "gen_ai.usage.cache_creation.input_tokens"},
	"reasoning_output_tokens_from":  {"gen_ai.usage.reasoning.output_tokens"},
}

func (c *checker) connector(n *yaml.Node) {
	m := c.object(n, "window_duration", "retention_windows", "max_slices", "topk", "topk_keys", "profiles", "hashing", "operation_filter", "mcp", "slices", "fields", "weights", "dedup", "summary_export")
	c.duration(m["window_duration"], time.Nanosecond, 24*time.Hour)
	c.integer(m["retention_windows"], 1, 120)
	c.integer(m["max_slices"], 1, 5000)
	c.integer(m["topk"], 0, 100)
	profiles := c.object(m["profiles"], "hllpp", "frequent_items", "bloom")
	for _, name := range []string{"hllpp", "frequent_items", "bloom"} {
		c.enum(profiles[name], "micro", "small", "default")
	}
	hash := c.object(m["hashing"], "algo", "secret_env")
	if a := hash["algo"]; a != nil && c.string(a) != "hmac_sha256_64" {
		c.add("unsafe_hashing", "blocking", a)
	}
	if s := hash["secret_env"]; s != nil && !environmentName.MatchString(c.string(s)) {
		c.add("unsafe_hashing", "blocking", s)
	}
	c.operations(m["operation_filter"], n)
	hashed := c.fields(m["fields"])
	c.slices(m["slices"], hashed, n)
	c.weights(m["weights"])
	c.topKeys(m["topk_keys"])
	dedup := c.object(m["dedup"], "enabled", "request_id_from")
	c.boolean(dedup["enabled"])
	if d := dedup["enabled"]; d != nil && d.Value == "true" {
		c.add("unsupported_mapping", "unsupported", d)
	}
	for _, key := range c.list(dedup["request_id_from"], false) {
		if !slices.Contains([]string{"gen_ai.response.id", "request.id"}, c.string(key)) {
			c.add("unsupported_mapping", "unsupported", key)
		}
	}
	mcp := c.object(m["mcp"], "enabled", "tool_errors")
	c.boolean(mcp["enabled"])
	tool := c.object(mcp["tool_errors"], "enabled")
	c.boolean(tool["enabled"])
	if tool["enabled"] != nil && tool["enabled"].Value == "true" && (mcp["enabled"] == nil || mcp["enabled"].Value != "true") {
		c.add("unsupported_mapping", "unsupported", tool["enabled"])
	}
	c.summary(m)
}

func (c *checker) operations(n, parent *yaml.Node) {
	if n == nil {
		c.add("missing_operation_filter", "blocking", parent)
		return
	}
	m := c.object(n, "llm_operations")
	if m["llm_operations"] == nil {
		c.add("missing_operation_filter", "blocking", n)
		return
	}
	items := c.list(m["llm_operations"], false)
	if len(items) == 0 {
		c.add("unsafe_operation_filter", "blocking", m["llm_operations"])
	}
	if len(items) > 64 {
		c.add("unsupported_mapping", "unsupported", n)
	}
	seen := make(map[string]bool)
	for _, item := range items {
		op := c.string(item)
		if seen[op] {
			c.add("unsafe_operation_filter", "blocking", item)
		}
		seen[op] = true
		switch op {
		case "chat", "generate_content", "text_completion", "embeddings":
		case "invoke_agent", "execute_tool", "retrieval", "tool", "agent", "*", "":
			c.add("unsafe_operation_filter", "blocking", item)
		default:
			c.add("unknown_operation", "unsupported", item)
		}
	}
}

func (c *checker) fields(n *yaml.Node) map[string]bool {
	hashed := make(map[string]bool)
	// Defaults are kept conservatively even when an override appears to replace
	// them: collector map merging depends on its configuration implementation.
	for _, sources := range defaultSources {
		for _, source := range sources {
			hashed[source] = true
		}
	}
	fields := c.object(n)
	for i, pair := range ordered(n) {
		if i%2 != 0 {
			continue
		}
		name := pair.Value
		field := fields[name]
		if _, ok := fieldDomains[name]; !ok {
			c.add("unsupported_mapping", "unsupported", pair)
		}
		m := c.object(field, "from_attributes", "from_resource_attributes", "canonicalization", "domain")
		if v := m["canonicalization"]; v != nil {
			c.enum(v, "text_v1")
		}
		if v := m["domain"]; v != nil {
			c.enum(v, fieldDomains[name])
		}
		for _, sourceList := range []string{"from_attributes", "from_resource_attributes"} {
			sources := c.list(m[sourceList], false)
			if len(sources) > 16 {
				c.add("unsupported_mapping", "unsupported", m[sourceList])
			}
			seen := make(map[string]bool)
			for _, source := range sources {
				value := c.string(source)
				hashed[value] = true
				if len(value) > 128 || seen[value] {
					c.add("unsupported_mapping", "unsupported", source)
				}
				seen[value] = true
				if !slices.Contains(defaultSources[name], value) {
					c.add("unknown_field_source", "unsupported", source)
				}
			}
		}
		spanSources, resourceSources := len(defaultSources[name]), len(defaultSources[name])
		if name == "session_key" {
			resourceSources = 0
		}
		if v := m["from_attributes"]; v != nil {
			spanSources = len(v.Content)
		}
		if v := m["from_resource_attributes"]; v != nil {
			resourceSources = len(v.Content)
		}
		if spanSources+resourceSources == 0 {
			c.add("unsupported_mapping", "unsupported", field)
		}
	}
	return hashed
}

func (c *checker) slices(n *yaml.Node, hashed map[string]bool, parent *yaml.Node) {
	if n == nil {
		for _, key := range []string{"gen_ai.request.model", "team.id"} {
			if hashed[key] {
				c.add("hashed_source_overlap", "blocking", parent)
			}
		}
		return
	}
	items := c.list(n, true)
	if len(items) > 32 {
		c.add("unsupported_mapping", "unsupported", n)
	}
	seen := make(map[string]bool)
	for _, item := range items {
		m := c.object(item, "name", "keys", "from_resource_attributes")
		name := c.string(m["name"])
		if seen[name] {
			c.add("unsupported_mapping", "unsupported", item)
		}
		seen[name] = true
		keys := c.list(m["keys"], true)
		if len(keys) > 8 {
			c.add("unsupported_mapping", "unsupported", m["keys"])
		}
		keySet := make(map[string]bool)
		for _, key := range keys {
			value := c.string(key)
			if len(value) > 128 || keySet[value] {
				c.add("unsupported_mapping", "unsupported", key)
			}
			keySet[value] = true
			c.sliceSource(key, value, hashed)
		}
		resources := c.list(m["from_resource_attributes"], false)
		if len(resources) > 16 {
			c.add("unsupported_mapping", "unsupported", item)
		}
		resourceSet := make(map[string]bool)
		for _, resource := range resources {
			value := c.string(resource)
			if !keySet[value] || resourceSet[value] {
				c.add("unsupported_mapping", "unsupported", resource)
			}
			resourceSet[value] = true
			c.sliceSource(resource, value, hashed)
		}
	}
}

func (c *checker) sliceSource(n *yaml.Node, key string, hashed map[string]bool) {
	if hashed[key] {
		c.add("hashed_source_overlap", "blocking", n)
	}
	switch key {
	case "gen_ai.request.model", "gen_ai.response.model", "gen_ai.system", "gen_ai.provider.name", "gen_ai.operation.name", "service.name", "service.namespace", "deployment.environment.name", "team.id":
		return
	case "enduser.id", "user.id", "gen_ai.conversation.id", "session.id", "mcp.session.id", "mcp.resource.uri", "retrieval.doc_id", "gen_ai.request.prompt", "gen_ai.response.completion", "gen_ai.input.messages", "gen_ai.output.messages":
		c.add("sensitive_slice", "blocking", n)
		c.add("high_cardinality_slice", "blocking", n)
		return
	case "trace_id", "span_id", "trace.id", "span.id", "gen_ai.response.id", "request.id", "jsonrpc.request.id", "service.instance.id", "url.full":
		c.add("high_cardinality_slice", "blocking", n)
		return
	}
	lower := strings.ToLower(key)
	for _, term := range []string{"prompt", "message", "password", "secret", "authorization", "email"} {
		if strings.Contains(lower, term) {
			c.add("sensitive_slice", "blocking", n)
			break
		}
	}
	if strings.HasSuffix(lower, ".id") || strings.HasSuffix(lower, ".uuid") || strings.HasSuffix(lower, ".uri") || strings.HasSuffix(lower, ".url") {
		c.add("high_cardinality_slice", "blocking", n)
	}
	c.add("unknown_slice_source", "unsupported", n)
}

func (c *checker) weights(n *yaml.Node) {
	m := c.object(n, "input_tokens_from", "output_tokens_from", "cache_read_input_tokens_from", "cache_write_input_tokens_from", "reasoning_output_tokens_from", "fallback_when_missing")
	c.enum(m["fallback_when_missing"], "request_count_only")
	for _, name := range []string{"input_tokens_from", "output_tokens_from", "cache_read_input_tokens_from", "cache_write_input_tokens_from", "reasoning_output_tokens_from"} {
		if m[name] == nil {
			continue
		}
		items := c.list(m[name], false)
		if len(items) == 0 && (name == "input_tokens_from" || name == "output_tokens_from") {
			c.add("unsafe_token_mapping", "blocking", m[name])
		}
		if len(items) > 16 {
			c.add("unsupported_mapping", "unsupported", m[name])
		}
		seen := make(map[string]bool)
		for _, item := range items {
			key := c.string(item)
			if seen[key] || len(key) > 128 {
				c.add("unsupported_mapping", "unsupported", item)
			}
			seen[key] = true
			if slices.Contains(tokenSources[name], key) {
				continue
			}
			known := key == "gen_ai.usage.total_tokens"
			for _, sources := range tokenSources {
				known = known || slices.Contains(sources, key)
			}
			if known {
				c.add("unsafe_token_mapping", "blocking", item)
			} else {
				c.add("unknown_token_mapping", "unsupported", item)
			}
		}
	}
}

func (c *checker) topKeys(n *yaml.Node) {
	seen := make(map[string]bool)
	items := c.list(n, false)
	if len(items) > 4 {
		c.add("unsupported_mapping", "unsupported", n)
	}
	for _, item := range items {
		m := c.object(item, "field", "weight")
		field := c.string(m["field"])
		c.enum(m["field"], "prompt_key", "user_key", "session_key")
		c.enum(m["weight"], "tokens", "requests")
		if seen[field] {
			c.add("unsupported_mapping", "unsupported", item)
		}
		seen[field] = true
	}
}

func (c *checker) summary(parent map[string]*yaml.Node) {
	m := c.object(parent["summary_export"], "directory", "producer_id", "scope_id", "key_id", "interval")
	if len(m) == 0 {
		return
	}
	c.string(m["directory"])
	identities := summary.Envelope{Version: 1, Sequence: 1, Epoch: "validation", AccountingID: "validation", WindowDuration: 1,
		ProducerID: c.string(m["producer_id"]), ScopeID: c.string(m["scope_id"]), KeyID: c.string(m["key_id"]),
		Counters: map[string]uint64{}, Sketches: map[string]summary.Payload{}}
	if identities.Validate() != nil {
		c.add("unsupported_mapping", "unsupported", parent["summary_export"])
	}
	c.duration(m["interval"], time.Second, time.Minute)
	window := time.Minute
	if n := parent["window_duration"]; n != nil {
		window, _ = time.ParseDuration(n.Value)
	}
	interval := 5 * time.Second
	if n := m["interval"]; n != nil {
		interval, _ = time.ParseDuration(n.Value)
	}
	if interval > window {
		c.add("unsupported_mapping", "unsupported", parent["summary_export"])
	}
	c.integer(parent["retention_windows"], 2, 120)
}
