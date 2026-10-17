// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/attribution"
)

func routingRecord() Record {
	r := record()
	r.Route, r.Agent, r.ServedIdentityKnown = "private-route-sentinel", "private-agent-sentinel", true
	return r
}

func setRoutingIdentity(r *Record, kind, value string) {
	if kind == "route" {
		r.Route = value
	} else {
		r.Agent = value
	}
}

func TestRoutingScopeBaselineAndFailureComposition(t *testing.T) {
	for _, kind := range []string{"route", "agent"} {
		t.Run(kind, func(t *testing.T) {
			p, f, s := fixture()
			s.Moves[0].Scope = &Selector{kind, "private-" + kind + "-sentinel"}
			c := calculator(t, p, f, s, WorkloadMap{})
			if !c.UsesRouting() || c.moves[0].move.Scope != nil {
				t.Fatal("routing mode missing or raw selector retained")
			}
			for _, state := range []string{"match", "nonmatch", "missing", "unselected-deployment", "missing-split"} {
				r := routingRecord()
				r.Failed = true
				switch state {
				case "nonmatch":
					setRoutingIdentity(&r, kind, "other-private-sentinel")
				case "missing":
					setRoutingIdentity(&r, kind, " \t ")
				case "unselected-deployment":
					r.ServedIdentityKnown = false
				case "missing-split":
					r.CacheWrite = nil
					setRoutingIdentity(&r, kind, "")
				}
				if err := c.Add(r); err != nil {
					t.Fatal(err)
				}
			}
			got := c.Result(Source{})
			if got.Covered != 2 || got.Moves[0].Covered != 1 || got.Moves[0].Excluded != 3 ||
				*got.Before != "0.000369000" || *got.Level1 != "0.000545750" ||
				!reflect.DeepEqual(got.Excluded, map[string]uint64{"missing_scope_identity": 1, "unknown_served_identity": 1, "missing_split": 1}) {
				t.Fatal("routing eligibility changed original-volume costs or exclusions")
			}
			assertFailureContribution(t, got.PricedFailures, got)
			if got.PricedFailures.Input.String() != "200" || got.PricedFailures.Output.String() != "100" ||
				got.PricedFailures.CacheRead.String() != "40" || got.PricedFailures.CacheWrite.String() != "20" || got.PricedFailures.Total.String() != "300" {
				t.Fatal("excluded failures contributed or cache was double-counted")
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "sentinel") {
				t.Fatal("raw identity leaked")
			}
			c.Close()
			for _, b := range c.failures.secret {
				if b != 0 {
					t.Fatal("failure child retained the hashing secret")
				}
			}
		})
	}
}

func TestRoutingCanonicalSelectorsAndDomainSeparation(t *testing.T) {
	for _, kind := range []string{"route", "agent"} {
		for _, tc := range []struct {
			name, selector, observed string
			matched                  uint64
		}{
			{"canonical", " caf\u00e9 ", "cafe\u0301", 1},
			{"case-sensitive", "Route", "route", 0},
			{"no-prefix", "route", "route-extra", 0},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				p, f, s := fixture()
				s.Moves[0].Scope = &Selector{kind, tc.selector}
				c := calculator(t, p, f, s, WorkloadMap{})
				r := routingRecord()
				setRoutingIdentity(&r, kind, tc.observed)
				if err := c.Add(r); err != nil || c.Result(Source{}).Moves[0].Covered != tc.matched {
					t.Fatal("selector canonicalization, case or exact-match behavior changed")
				}
				want, err := attribution.Identity(c.secret, "litellm-spend/v1", kind, tc.selector)
				if err != nil || c.moves[0].scope.digest != want {
					t.Fatal("selector does not use the existing source/kind HMAC framing")
				}
				for _, other := range []string{"key", "team", "route", "agent"} {
					if other == kind {
						continue
					}
					digest, err := c.hashSelector(Selector{other, tc.selector})
					if err != nil || digest.digest == want {
						t.Fatal("identity kinds were not domain-separated")
					}
				}
			})
		}
	}
}

func TestRoutingMappingConflictsAndMissingMapping(t *testing.T) {
	p, f, s := fixture()
	for _, kind := range []string{"route", "agent"} {
		for _, other := range []string{"key", "team", "route", "agent"} {
			if kind == other {
				continue
			}
			for _, agree := range []bool{false, true} {
				m := WorkloadMap{1, []Mapping{{Selector{kind, "private-" + kind + "-sentinel"}, "qa"}, {Selector{other, "private-" + other + "-sentinel"}, "qa"}}}
				if !agree {
					m.Mappings[1].Workload = "coding"
				}
				c := calculator(t, p, f, s, m)
				err := c.Add(routingRecord())
				if agree {
					got := c.Result(Source{})
					if err != nil || !c.UsesRouting() || got.Unmapped != 0 || len(got.Level2) != 1 {
						t.Fatal("agreeing mappings did not compose")
					}
				} else if err == nil || strings.Contains(err.Error(), "sentinel") {
					t.Fatal("mapping conflict accepted or leaked identities")
				}
			}
		}
		c := calculator(t, p, f, s, WorkloadMap{1, []Mapping{{Selector{kind, "private-" + kind + "-sentinel"}, "coding"}}})
		r := routingRecord()
		setRoutingIdentity(&r, kind, "")
		if err := c.Add(r); err != nil {
			t.Fatal(err)
		}
		got := c.Result(Source{})
		if got.Covered != 1 || got.Unmapped != 1 || len(got.Level2) != 3 || len(got.Excluded) != 0 {
			t.Fatal("missing mapping must remain unresolved, not exclude or guess a workload")
		}
	}
}

func TestRoutingOverlapAndMissingScopePrecedence(t *testing.T) {
	p, f, s := fixture()
	s.Moves[0].Scope = &Selector{"route", "private-route-sentinel"}
	s.Moves = append(s.Moves, Move{From: "a", To: "c", Fraction: "0.6", Scope: &Selector{"agent", "private-agent-sentinel"}})
	c := calculator(t, p, f, s, WorkloadMap{})
	if err := c.Add(routingRecord()); err == nil || strings.Contains(err.Error(), "sentinel") {
		t.Fatal("overlapping scopes accepted or leaked")
	}
	for _, issue := range []string{"", "invalid_usage", "unsupported_call_type", "missing_split"} {
		c = calculator(t, p, f, s, WorkloadMap{})
		r := routingRecord()
		r.Agent, r.Issue = "", issue
		if err := c.Add(r); err != nil {
			t.Fatal(err)
		}
		want := issue
		if want == "" {
			want = "missing_scope_identity"
		}
		got := c.Result(Source{})
		if got.Excluded[want] != 1 || got.Covered != 0 || got.Moves[0].Excluded != 1 || got.Moves[1].Excluded != 1 {
			t.Fatal("unknown scope hid usage evidence or produced partial move costs")
		}
	}
}

func TestRoutingSelectedIdentityGateOnlyForNewKinds(t *testing.T) {
	for _, kind := range []string{"", "key", "team", "route", "agent"} {
		for _, mapped := range []bool{false, true} {
			p, f, s := fixture()
			m := WorkloadMap{}
			if kind != "" {
				selector := Selector{kind, "private-" + kind + "-sentinel"}
				if mapped {
					m = WorkloadMap{1, []Mapping{{selector, "qa"}}}
				} else {
					s.Moves[0].Scope = &selector
				}
			}
			c := calculator(t, p, f, s, m)
			r := routingRecord()
			r.ServedIdentityKnown = false
			if err := c.Add(r); err != nil {
				t.Fatal(err)
			}
			got := c.Result(Source{})
			newKind := kind == "route" || kind == "agent"
			if c.UsesRouting() != newKind || (got.Excluded["unknown_served_identity"] == 1) != newKind || (got.Covered == 1) == newKind {
				t.Fatal("selected-deployment gate applied outside routing or was omitted")
			}
		}
	}
}

func TestRoutingUnknownPriceAndSelectorValidation(t *testing.T) {
	p, f, s := fixture()
	s.Moves[0].Scope = &Selector{"route", "private-route-sentinel"}
	c := calculator(t, p, f, s, WorkloadMap{})
	r := routingRecord()
	r.Model = "private-unpriced-sentinel"
	if err := c.Add(r); err != nil || c.Result(Source{}).Excluded["unknown_model_price"] != 1 {
		t.Fatal("selected but unpriced identity inherited a route price")
	}
	for _, kind := range []string{"route", "agent"} {
		for _, value := range []string{"", " \t ", strings.Repeat("x", 8193), "\xff"} {
			s.Moves[0].Scope = &Selector{kind, value}
			if c, err := New(p, f, s, WorkloadMap{}); err == nil {
				c.Close()
				t.Fatal("invalid routing selector accepted")
			}
		}
	}
}

func TestRoutingFractionalMovesPreserveFailureWorkloadVariants(t *testing.T) {
	p, f, s := fixture()
	s.Moves[0].Fraction = "0.25"
	s.Moves[0].Scope = &Selector{"route", "private-route-sentinel"}
	s.Moves = append(s.Moves, Move{From: "a", To: "b", Fraction: "0.5", Scope: &Selector{"agent", "private-agent-sentinel"}})
	c := calculator(t, p, f, s, WorkloadMap{1, []Mapping{{Selector{"key", "private-key-sentinel"}, "qa"}}})
	r := routingRecord()
	r.Failed = true
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	r.Route, r.Agent = "other-route", "other-agent"
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	r = routingRecord()
	r.Key = "unmapped-success"
	if err := c.Add(r); err != nil {
		t.Fatal(err)
	}
	got := c.Result(Source{})
	v := got.PricedFailures
	if got.Covered != 3 || got.Unmapped != 1 || len(got.Level2) != 3 || *got.Before != "0.000553500" || *got.Level1 != "0.001083750" ||
		v == nil || v.Records != 2 || v.Total.String() != "300" || *v.Before != "0.000369000" || *v.Level1 != "0.000634125" || len(v.Level2) != 3 {
		t.Fatal("mixed routing moves changed exact costs or failure workload variants")
	}
	for i, w := range v.Level2 {
		if w.Workload != got.Level2[i].Workload || w.Mean != "0.000784125" || w.Low != "0.000709125" || w.High != "0.000859125" {
			t.Fatal("mapped failure contribution changed under the success workload assumption")
		}
	}
}

func TestRoutingCanonicalMapConflictsAndOtherModelBaseline(t *testing.T) {
	p, f, s := fixture()
	for _, kind := range []string{"route", "agent"} {
		m := WorkloadMap{1, []Mapping{{Selector{kind, "caf\u00e9"}, "qa"}, {Selector{kind, "cafe\u0301"}, "coding"}}}
		if c, err := New(p, f, s, m); err == nil {
			c.Close()
			t.Fatal("canonical routing mapping conflict accepted")
		} else if err.Error() != "canonical workload selectors conflict" {
			t.Fatal("canonical mapping conflict changed or leaked")
		}
		s.Moves[0].Scope = &Selector{kind, "private-selector-sentinel"}
		c := calculator(t, p, f, s, WorkloadMap{})
		r := routingRecord()
		r.Model, r.Route, r.Agent = "c", "", ""
		if err := c.Add(r); err != nil {
			t.Fatal(err)
		}
		got := c.Result(Source{})
		if got.Covered != 1 || len(got.Excluded) != 0 || got.Moves[0].Covered != 0 || got.Moves[0].Excluded != 0 || *got.Before != "0.000369000" || *got.Before != *got.Level1 {
			t.Fatal("missing attribution excluded a known model outside every move")
		}
	}
}
