// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scenario

import (
	"fmt"
	"strconv"
)

// Only the OTLP/JSON fields used by this fixed synthetic workload are needed.
type Value struct {
	String string `json:"stringValue,omitempty"`
	Int    string `json:"intValue,omitempty"`
}
type Attribute struct {
	Key   string `json:"key"`
	Value Value  `json:"value"`
}
type Span struct {
	TraceID      string      `json:"traceId"`
	SpanID       string      `json:"spanId"`
	ParentSpanID string      `json:"parentSpanId,omitempty"`
	Name         string      `json:"name"`
	Kind         int         `json:"kind"`
	Start        string      `json:"startTimeUnixNano"`
	End          string      `json:"endTimeUnixNano"`
	Attributes   []Attribute `json:"attributes"`
}
type Batch struct {
	ResourceSpans []ResourceSpans `json:"resourceSpans"`
}
type ResourceSpans struct {
	Resource   OTLPResource `json:"resource"`
	ScopeSpans []ScopeSpans `json:"scopeSpans"`
}
type OTLPResource struct {
	Attributes []Attribute `json:"attributes"`
}
type ScopeSpans struct {
	Scope Scope  `json:"scope"`
	Spans []Span `json:"spans"`
}
type Scope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

func text(key, value string) Attribute { return Attribute{key, Value{String: value}} }
func number(key string, value int64) Attribute {
	return Attribute{key, Value{Int: strconv.FormatInt(value, 10)}}
}

// OTLP creates two disjoint span streams in shared traces. The owned client span
// is the partner invocation's parent; only the supervisor has an empty parent.
func (s Scenario) OTLP(window int) map[string]Batch {
	w := s.Windows[window]
	spans := map[string][]Span{}
	roots := map[int]string{}
	next := 0
	add := func(producer string, run int, parent, name string, kind int, attrs ...Attribute) string {
		next++
		id := fmt.Sprintf("%016x", next)
		spans[producer] = append(spans[producer], Span{
			TraceID: fmt.Sprintf("%032x", (window+1)*100+run), SpanID: id, ParentSpanID: parent,
			Name: name, Kind: kind, Start: "1700000000000000000", End: "1700000000001000000", Attributes: attrs,
		})
		return id
	}
	for _, c := range w.Calls {
		if c.Agent == "supervisor" {
			roots[c.Run] = add("owned", c.Run, "", "research supervisor", 1, text("gen_ai.operation.name", "invoke_agent"), text("gen_ai.agent.name", "research-supervisor"))
		}
	}
	for i, c := range w.Calls {
		parent := roots[c.Run]
		if c.Agent != "supervisor" {
			kind := 1
			if c.Producer == "partner" {
				parent = add("owned", c.Run, parent, "delegate research", 3, text("gen_ai.operation.name", "invoke_agent"), text("gen_ai.agent.name", c.Agent))
				kind = 2
			}
			parent = add(c.Producer, c.Run, parent, "specialist invocation", kind, text("gen_ai.operation.name", "invoke_agent"), text("gen_ai.agent.name", c.Agent))
		}
		attrs := []Attribute{text("gen_ai.operation.name", "chat"), text("gen_ai.request.model", fmt.Sprintf("demo-model-%d", c.Run)), text("enduser.id", User(c)), text("gen_ai.request.prompt", Prompt(c.Agent)), text("retrieval.doc_id", "FC_PRIVATE_context"), text("gen_ai.response.id", fmt.Sprintf("FC_PRIVATE_request_%s_%d", w.Name, i))}
		if c.Usage != nil {
			attrs = append(attrs, number("gen_ai.usage.input_tokens", c.Usage[0]), number("gen_ai.usage.output_tokens", c.Usage[1]))
			if c.CacheRead > 0 {
				attrs = append(attrs, number("gen_ai.usage.cache_read.input_tokens", c.CacheRead))
			}
			if c.Reasoning > 0 {
				attrs = append(attrs, number("gen_ai.usage.reasoning.output_tokens", c.Reasoning))
			}
		}
		add(c.Producer, c.Run, parent, "model request", 3, attrs...)
		if c.Agent == "supervisor" {
			continue
		}
		session := text("mcp.session.id", Session(w, i))
		add(c.Producer, c.Run, parent, "MCP initialize", 3, session, text("mcp.method.name", "initialize"))
		for _, resource := range c.Resources {
			add(c.Producer, c.Run, parent, "MCP resource read", 3, session, text("mcp.method.name", "resources/read"), text("mcp.resource.uri", Resource(resource)))
		}
		for _, name := range c.Errors {
			e := s.ToolErrors[name]
			add(c.Producer, c.Run, parent, "MCP tool call", 3, session, text("mcp.method.name", "tools/call"), text("gen_ai.operation.name", "execute_tool"), text("gen_ai.tool.name", e.Tool), text("error.type", e.Error))
		}
	}
	result := map[string]Batch{}
	for _, producer := range []string{"owned", "partner"} {
		result[producer] = Batch{[]ResourceSpans{{OTLPResource{[]Attribute{text("service.name", producer+"-research")}}, []ScopeSpans{{Scope{"fleetdiff.synthetic-research", "1"}, spans[producer]}}}}}
	}
	return result
}
