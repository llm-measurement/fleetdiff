// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package scenario holds the synthetic research workload shared by both demos.
// It is example support, not a general telemetry normalizer or collector.
package scenario

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

//go:embed research.json
var recipe []byte

type Scenario struct {
	ToolErrors map[string]ToolError `json:"tool_errors"`
	Windows    []Window             `json:"windows"`
}

type ToolError struct {
	Tool  string `json:"tool"`
	Error string `json:"error"`
}

type Window struct {
	Name     string   `json:"name"`
	Calls    []Call   `json:"calls"`
	Expected Expected `json:"expected"`
}

type Call struct {
	Producer  string    `json:"producer"`
	Agent     string    `json:"agent"`
	Run       int       `json:"run"`
	Usage     *[2]int64 `json:"usage,omitempty"`
	CacheRead int64     `json:"cache_read,omitempty"`
	Reasoning int64     `json:"reasoning,omitempty"`
	Resources []string  `json:"resources,omitempty"`
	Errors    []string  `json:"errors,omitempty"`
}

type Expected struct {
	OwnedTokens   uint64            `json:"owned_tokens"`
	Counters      map[string]uint64 `json:"counters"`
	Distinct      map[string]int    `json:"distinct"`
	PromptWeights map[string]int64  `json:"prompt_weights"`
	ToolErrors    map[string]int64  `json:"tool_errors"`
}

func Load() (Scenario, error) {
	var s Scenario
	err := json.Unmarshal(recipe, &s)
	return s, err
}

func User(c Call) string         { return fmt.Sprintf("FC_PRIVATE_user_%d", c.Run) }
func Prompt(agent string) string { return "FC_PRIVATE_prompt_" + agent }
func Resource(id string) string  { return "https://reference.invalid/FC_PRIVATE_" + id }
func Session(w Window, index int) string {
	return fmt.Sprintf("FC_PRIVATE_session_%s_%d", w.Name, index)
}

func Hash(secret sketchhash.Secret, domain sketchhash.Domain, text string) (uint64, error) {
	value, err := canon.CanonicalizeString(canon.TextV1, text)
	if err != nil {
		return 0, err
	}
	return sketchhash.Hash64(secret, domain, value)
}

func (e ToolError) Hash(secret sketchhash.Secret) (uint64, error) {
	tool, err := canon.CanonicalizeString(canon.TextV1, e.Tool)
	if err != nil {
		return 0, err
	}
	cause, err := canon.CanonicalizeString(canon.TextV1, e.Error)
	if err != nil {
		return 0, err
	}
	return sketchhash.Hash64(secret, sketchhash.ToolErrorV1, append(append(tool, 0), cause...))
}
