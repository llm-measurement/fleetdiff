// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scenario

import (
	"errors"
	"fmt"
	"math"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
)

// CheckReport checks observed answers, not authenticity, causality, or task quality.
func (s Scenario) CheckReport(r compare.Report) error {
	a, b := s.Windows[0].Expected, s.Windows[1].Expected
	if !r.Complete || r.Before.Usage == nil || r.After.Usage == nil {
		return errors.New("incomplete research scenario coverage")
	}
	for name, before := range a.Counters {
		found := false
		for _, c := range r.Counters {
			if c.Name == name {
				found = true
				if c.Before != before || c.After != b.Counters[name] || c.Delta != int64(b.Counters[name])-int64(before) {
					return fmt.Errorf("research counter mismatch: %s", name)
				}
			}
		}
		if !found {
			return fmt.Errorf("missing research counter: %s", name)
		}
	}
	for name, before := range a.Distinct {
		found := false
		for _, d := range r.Distinct {
			if d.Name == name {
				found = true
				if math.Abs(d.Before.Estimate-float64(before)) > .02 || math.Abs(d.After.Estimate-float64(b.Distinct[name])) > .02 {
					return fmt.Errorf("research distinct estimate mismatch: %s", name)
				}
			}
		}
		if !found {
			return fmt.Errorf("missing research sketch: %s", name)
		}
	}
	if r.Before.Usage.Complete != a.Counters["requests"]-a.Counters["missing_token_usage"] || r.After.Usage.Complete != b.Counters["requests"]-b.Counters["missing_token_usage"] {
		return errors.New("incorrect research token coverage")
	}
	return nil
}

// CheckSignatures uses the fixture key only inside the harness. Hash-bearing
// comparisons are never written as the default report or narrated with raw names.
func (s Scenario) CheckSignatures(r compare.Report, secret sketchhash.Secret) error {
	a, b := s.Windows[0].Expected, s.Windows[1].Expected
	for _, name := range []string{"top_prompts", "top_tool_errors"} {
		want := map[string][2]int64{}
		if name == "top_prompts" {
			for agent, before := range a.PromptWeights {
				h, err := Hash(secret, sketchhash.PromptV1, Prompt(agent))
				if err != nil {
					return err
				}
				want[fmt.Sprintf("%016x", h)] = [2]int64{before, b.PromptWeights[agent]}
			}
		} else {
			for signature, e := range s.ToolErrors {
				h, err := e.Hash(secret)
				if err != nil {
					return err
				}
				want[fmt.Sprintf("%016x", h)] = [2]int64{a.ToolErrors[signature], b.ToolErrors[signature]}
			}
		}
		found := false
		for _, c := range r.Concentration {
			if c.Name != name {
				continue
			}
			found = true
			var beforeWeight, afterWeight int64
			for _, pair := range want {
				beforeWeight += pair[0]
				afterWeight += pair[1]
			}
			if c.BeforeWeight != beforeWeight || c.AfterWeight != afterWeight || c.CandidateCount != len(want) || len(c.Movers) != len(want) {
				return errors.New("research weights or candidate count differ")
			}
			if name == "top_tool_errors" {
				h, err := s.ToolErrors["search_timeout"].Hash(secret)
				if err != nil {
					return err
				}
				if c.Movers[0].Hash != fmt.Sprintf("%016x", h) {
					return errors.New("top tool-error mover is not the planted search timeout")
				}
			}
			for _, m := range c.Movers {
				pair, ok := want[m.Hash]
				if !ok || m.Before != (compare.Interval{Lower: pair[0], Upper: pair[0]}) || m.After != (compare.Interval{Lower: pair[1], Upper: pair[1]}) || m.Delta != (compare.Interval{Lower: pair[1] - pair[0], Upper: pair[1] - pair[0]}) {
					return errors.New("research signature bounds do not match planted counts")
				}
				direction := "unchanged"
				if pair[1] > pair[0] {
					direction = "increased"
				} else if pair[1] < pair[0] {
					direction = "decreased"
				}
				if m.Direction != direction {
					return errors.New("research signature direction differs")
				}
				delete(want, m.Hash)
			}
		}
		if !found || len(want) != 0 {
			return errors.New("missing research signature candidates")
		}
	}
	return nil
}
