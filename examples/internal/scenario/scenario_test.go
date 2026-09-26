// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scenario

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"testing"
)

func TestResearchRecipeAndTraceOwnership(t *testing.T) {
	s, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Windows) != 2 || s.Windows[0].Name != "before" || s.Windows[1].Name != "after" {
		t.Fatal("expected before and after")
	}
	for wi, w := range s.Windows {
		t.Run(w.Name, func(t *testing.T) {
			counters := map[string]uint64{}
			for name := range w.Expected.Counters {
				counters[name] = 0
			}
			sets := map[string]map[string]bool{}
			for name := range w.Expected.Distinct {
				sets[name] = map[string]bool{}
			}
			prompts, toolErrors := map[string]int64{}, map[string]int64{}
			for name := range w.Expected.ToolErrors {
				toolErrors[name] = 0
			}
			owners, spans := map[string]string{}, map[string]Span{}
			roots, delegates := 0, 0
			var ownedTokens uint64
			for producer, batch := range s.OTLP(wi) {
				// Exercise exactly the JSON shape submitted to the OTLP receiver.
				data, err := json.Marshal(batch)
				if err != nil {
					t.Fatal(err)
				}
				var decoded Batch
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatal(err)
				}
				for _, span := range decoded.ResourceSpans[0].ScopeSpans[0].Spans {
					id := span.TraceID + "/" + span.SpanID
					if _, exists := owners[id]; exists {
						t.Fatal("duplicate event across operators")
					}
					owners[id], spans[id] = producer, span
					attrs := map[string]Value{}
					for _, a := range span.Attributes {
						attrs[a.Key] = a.Value
					}
					operation := attrs["gen_ai.operation.name"].String
					if span.ParentSpanID == "" {
						if producer != "owned" || operation != "invoke_agent" || attrs["gen_ai.agent.name"].String != "research-supervisor" {
							t.Fatal("unexpected trace root")
						}
						roots++
					}
					if operation == "invoke_agent" && span.ParentSpanID == "" {
						counters["agent_runs"]++
					}
					if operation == "chat" {
						counters["requests"]++
						sets["distinct_users"][attrs["enduser.id"].String] = true
						sets["distinct_prompts"][attrs["gen_ai.request.prompt"].String] = true
						sets["distinct_docs"][attrs["retrieval.doc_id"].String] = true
						var total uint64
						for attr, counter := range map[string]string{"gen_ai.usage.input_tokens": "input_tokens", "gen_ai.usage.output_tokens": "output_tokens", "gen_ai.usage.cache_read.input_tokens": "cache_read_input_tokens", "gen_ai.usage.reasoning.output_tokens": "reasoning_output_tokens"} {
							if value, exists := attrs[attr]; exists {
								n, err := strconv.ParseUint(value.Int, 10, 64)
								if err != nil {
									t.Fatal(err)
								}
								counters[counter] += n
								if counter == "input_tokens" || counter == "output_tokens" {
									total += n
								}
							}
						}
						if attrs["gen_ai.usage.input_tokens"].Int == "" || attrs["gen_ai.usage.output_tokens"].Int == "" {
							counters["missing_token_usage"]++
						}
						if producer == "owned" {
							ownedTokens += total
						}
						prompts[attrs["gen_ai.request.prompt"].String] += int64(total)
					}
					for attr, name := range map[string]string{"mcp.session.id": "distinct_mcp_sessions", "mcp.method.name": "distinct_mcp_methods", "mcp.resource.uri": "distinct_mcp_resources"} {
						if v := attrs[attr].String; v != "" {
							sets[name][v] = true
						}
					}
					if tool, cause := attrs["gen_ai.tool.name"].String, attrs["error.type"].String; tool != "" && cause != "" {
						matched := false
						for name, e := range s.ToolErrors {
							if e.Tool == tool && e.Error == cause {
								toolErrors[name]++
								matched = true
							}
						}
						if !matched {
							t.Fatal("unknown planted error signature")
						}
					}
				}
			}
			for id, span := range spans {
				if span.ParentSpanID == "" {
					continue
				}
				parentID := span.TraceID + "/" + span.ParentSpanID
				parent, exists := spans[parentID]
				if !exists {
					t.Fatal("orphan span or lost trace context")
				}
				if owners[id] != owners[parentID] {
					if owners[id] != "partner" || owners[parentID] != "owned" || parent.Kind != 3 || span.Kind != 2 {
						t.Fatal("invalid cross-operator delegation")
					}
					delegates++
				}
				// Every path must terminate at a root, not a cycle.
				for steps := 0; parent.ParentSpanID != ""; steps++ {
					if steps >= len(spans) {
						t.Fatal("cyclic trace")
					}
					parent = spans[parent.TraceID+"/"+parent.ParentSpanID]
				}
			}
			wantDelegates := 2
			if w.Name == "after" {
				wantDelegates = 6
			}
			if roots != 2 || delegates != wantDelegates {
				t.Fatalf("roots=%d delegated calls=%d", roots, delegates)
			}
			if !reflect.DeepEqual(counters, w.Expected.Counters) || ownedTokens != w.Expected.OwnedTokens {
				t.Fatalf("accounting differs: %+v owned=%d", counters, ownedTokens)
			}
			for name, want := range w.Expected.Distinct {
				if len(sets[name]) != want {
					t.Fatalf("%s distinct=%d want=%d", name, len(sets[name]), want)
				}
			}
			for agent, want := range w.Expected.PromptWeights {
				if prompts[Prompt(agent)] != want {
					t.Fatal("prompt weights differ")
				}
			}
			if !reflect.DeepEqual(toolErrors, w.Expected.ToolErrors) {
				t.Fatalf("tool errors differ: %+v", toolErrors)
			}
			for _, producer := range []string{"owned", "partner"} {
				seenShared := false
				for _, span := range s.OTLP(wi)[producer].ResourceSpans[0].ScopeSpans[0].Spans {
					for _, attr := range span.Attributes {
						if attr.Key == "mcp.resource.uri" && attr.Value.String == Resource("R1") {
							seenShared = true
						}
					}
				}
				if !seenShared {
					t.Fatal("shared MCP resource absent from one operator")
				}
			}
			t.Log(fmt.Sprintf("%d disjoint spans, %d roots, %d cross-operator invocations", len(spans), roots, delegates))
		})
	}
}
