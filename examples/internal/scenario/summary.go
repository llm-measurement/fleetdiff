// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scenario

import (
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

// Summary builds synthetic exports directly from the recipe, independently of
// the OTLP encoding. Live tests check the same expectations through the collector.
func (s Scenario) Summary(w Window, producer string, start int64, secret sketchhash.Secret) (summary.Envelope, error) {
	domains := map[string]sketchhash.Domain{
		"distinct_users": sketchhash.UserV1, "distinct_prompts": sketchhash.PromptV1,
		"distinct_docs": sketchhash.RetrievalDocV1, "distinct_mcp_resources": sketchhash.RetrievalDocV1,
		"distinct_mcp_sessions": sketchhash.MCPSessionV1, "distinct_mcp_methods": sketchhash.MCPMethodV1,
	}
	hlls := map[string]*hllpp.Sketch{}
	for name, domain := range domains {
		h, err := hllpp.New("small", domain, sketchhash.HMACSHA25664)
		if err != nil {
			return summary.Envelope{}, err
		}
		hlls[name] = h
	}
	prompts, err := frequentitems.New("small", sketchhash.PromptV1, sketchhash.HMACSHA25664)
	if err != nil {
		return summary.Envelope{}, err
	}
	errors, err := frequentitems.New("small", sketchhash.ToolErrorV1, sketchhash.HMACSHA25664)
	if err != nil {
		return summary.Envelope{}, err
	}
	counts := map[string]uint64{"agent_runs": 0, "requests": 0, "input_tokens": 0, "output_tokens": 0, "missing_token_usage": 0, "cache_read_input_tokens": 0, "cache_write_input_tokens": 0, "reasoning_output_tokens": 0, "dedup_suppressed": 0, "dedup_key_missing": 0}
	add := func(name, value string) error {
		h, err := Hash(secret, domains[name], value)
		if err != nil {
			return err
		}
		hlls[name].AddHash(h)
		return nil
	}
	for i, c := range w.Calls {
		if c.Producer != producer {
			continue
		}
		counts["requests"]++
		if c.Agent == "supervisor" {
			counts["agent_runs"]++
		}
		for name, value := range map[string]string{"distinct_users": User(c), "distinct_prompts": Prompt(c.Agent), "distinct_docs": "FC_PRIVATE_context"} {
			if err := add(name, value); err != nil {
				return summary.Envelope{}, err
			}
		}
		if c.Usage == nil {
			counts["missing_token_usage"]++
		} else {
			counts["input_tokens"] += uint64(c.Usage[0])
			counts["output_tokens"] += uint64(c.Usage[1])
			counts["cache_read_input_tokens"] += uint64(c.CacheRead)
			counts["reasoning_output_tokens"] += uint64(c.Reasoning)
			h, err := Hash(secret, sketchhash.PromptV1, Prompt(c.Agent))
			if err != nil {
				return summary.Envelope{}, err
			}
			if err := prompts.AddHash(h, c.Usage[0]+c.Usage[1]); err != nil {
				return summary.Envelope{}, err
			}
		}
		if c.Agent == "supervisor" {
			continue
		}
		if err := add("distinct_mcp_sessions", Session(w, i)); err != nil {
			return summary.Envelope{}, err
		}
		if err := add("distinct_mcp_methods", "initialize"); err != nil {
			return summary.Envelope{}, err
		}
		for _, id := range c.Resources {
			if err := add("distinct_mcp_resources", Resource(id)); err != nil {
				return summary.Envelope{}, err
			}
			if err := add("distinct_mcp_methods", "resources/read"); err != nil {
				return summary.Envelope{}, err
			}
		}
		for _, name := range c.Errors {
			if err := add("distinct_mcp_methods", "tools/call"); err != nil {
				return summary.Envelope{}, err
			}
			h, err := s.ToolErrors[name].Hash(secret)
			if err != nil {
				return summary.Envelope{}, err
			}
			if err := errors.AddHash(h, 1); err != nil {
				return summary.Envelope{}, err
			}
		}
	}
	payloads := map[string]summary.Payload{}
	for name, h := range hlls {
		data, err := h.MarshalBinary()
		if err != nil {
			return summary.Envelope{}, err
		}
		payloads[name] = summary.Payload{Kind: "hllpp", Data: data}
	}
	for name, f := range map[string]*frequentitems.Sketch{"top_prompts": prompts, "top_tool_errors": errors} {
		data, err := f.MarshalBinary()
		if err != nil {
			return summary.Envelope{}, err
		}
		payloads[name] = summary.Payload{Kind: "frequent_items", Data: data}
	}
	const duration = int64(15_000_000_000)
	return summary.Envelope{Version: 1, Sequence: 1, ProducerID: producer, Epoch: "synthetic-epoch", ScopeID: "research-demo", KeyID: "public-synthetic-key", AccountingID: "synthetic-research-v1", WindowStart: start, WindowDuration: duration, ObservedStart: start, ObservedEnd: start + duration, EmittedAt: start + duration, Counters: counts, Sketches: payloads}, nil
}
