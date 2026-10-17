// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/attribution"
)

func TestSelectionCanonicalIdentityAndUnknown(t *testing.T) {
	key := []byte("synthetic-routing-key-for-tests")
	for _, kind := range []string{"route", "agent"} {
		for _, value := range []string{"PRIVATE_ID", " PRIVATE_ID\t", "PRIVATE_\u00e9", "PRIVATE_e\u0301"} {
			opts := Options{}
			if kind == "route" {
				opts.ScopeRoute = value
			} else {
				opts.ScopeAgent = value
			}
			s, err := newScopeSelection(opts, key)
			if err != nil {
				t.Fatal(err)
			}
			want, err := attribution.Identity(key, "litellm-spend/v1", kind, value)
			if err != nil || s.hash != want {
				t.Fatal("selector changed identity construction")
			}
			for _, candidate := range []string{value, "PRIVATE_ID", "PRIVATE_\u00e9", "PRIVATE_e\u0301", "PRIVATE_ID_suffix", "private_id"} {
				r := Row{Values: map[string]string{scopeField(kind): candidate}}
				got, known := s.match(r, key)
				h, _ := attribution.Identity(key, "litellm-spend/v1", kind, candidate)
				if !known || got != (h == want) {
					t.Fatal("selector inferred a noncanonical identity match")
				}
			}
			for _, candidate := range []string{"", " \t", string([]byte{0xff}), strings.Repeat("x", MaxFieldBytes+1)} {
				if matched, known := s.match(Row{Values: map[string]string{scopeField(kind): candidate}}, key); matched || known {
					t.Fatal("unknown identity became a match or proven nonmatch")
				}
			}
			if matched, known := s.match(Row{Values: map[string]string{}}, key); matched || known {
				t.Fatal("absent identity became a nonmatch")
			}
		}
	}
	route, _ := newScopeSelection(Options{ScopeRoute: "same"}, key)
	agent, _ := newScopeSelection(Options{ScopeAgent: "same"}, key)
	otherSource, _ := attribution.Identity(key, "other-source", "route", "same")
	if route.hash == agent.hash || route.hash == otherSource {
		t.Fatal("scope identities lost kind or source separation")
	}
}

func TestSelectionBackendValidation(t *testing.T) {
	for _, edit := range []func(*Options){
		func(o *Options) { o.ScopeRoute, o.ScopeAgent = "PRIVATE_ROUTE", "PRIVATE_AGENT" },
		func(o *Options) { o.ScopeRoute, o.BeforePeriod = "PRIVATE_ROUTE", "" },
		func(o *Options) { o.ScopeAgent, o.AfterPeriod = "PRIVATE_AGENT", "" },
		func(o *Options) { o.ScopeRoute, o.DailySpendPath = "PRIVATE_ROUTE", "PRIVATE_PATH" },
		func(o *Options) { o.ScopeAgent, o.GroupBy = "PRIVATE_AGENT", "session" },
		func(o *Options) { o.ScopeRoute = " \t" },
		func(o *Options) { o.ScopeAgent = strings.Repeat("x", MaxFieldBytes+1) },
		func(o *Options) { o.ScopeRoute = string([]byte{0xff}) },
		func(o *Options) { o.RoutingBy = "route" },
		func(o *Options) { o.RoutingAudit, o.RoutingBy = true, "PRIVATE_MODE" },
	} {
		o := options(t)
		edit(&o)
		_, err := Investigate("PRIVATE_NONEXISTENT_PATH", o)
		if err == nil || strings.Contains(err.Error(), "PRIVATE_") || strings.Contains(err.Error(), "readable regular file") {
			t.Fatal("backend option validation failed or exposed a value")
		}
	}
	for _, mode := range []string{"", "route", "route-agent"} {
		o := routingOptions(t)
		o.GroupBy, o.RoutingBy = "session", mode
		r := routingReport(t, []map[string]string{routingSample("1", "2026-09-01", 1, 2)}, o)
		if r.RoutingAudit == nil {
			t.Fatal("unscoped two-period session grouping rejected audit")
		}
	}
}

func TestSelectionCoveragePrecedesFilter(t *testing.T) {
	rows := []map[string]string{}
	for i := range 6 {
		day := "2026-09-01"
		if i >= 3 {
			day = "2026-09-02"
		}
		r := routingSample(fmt.Sprint(i), day, 10, 2)
		r["call_type"] = "aresponses"
		if i%3 == 1 {
			r["agent_id"] = "PRIVATE_NONMATCH"
		}
		if i%3 == 2 {
			delete(r, "agent_id")
		}
		rows = append(rows, r)
	}
	classifier := routingSample("6", "2026-09-01", 2, 1)
	classifier["call_type"], classifier["internal_call_origin"] = "pass_through_endpoint", "autorouter_classifier"
	delete(classifier, "agent_id")
	rows = append(rows, classifier)
	for _, audit := range []bool{false, true} {
		o := options(t)
		o.ScopeAgent, o.RoutingAudit = "PRIVATE_AGENT", audit
		r := routingReport(t, rows, o)
		if r.Rows != 7 || r.Before.Requests != 1 || r.After.Requests != 1 || r.Before.Tokens != 12 || r.After.Tokens != 12 {
			t.Fatal("scope did not filter report totals")
		}
		s := r.ScopeCoverage
		if s == nil || s.Kind != "agent" || s.Before != (ScopeCounts{MatchedRows: 1, NonmatchingKnownRows: 1, UnknownIdentityRows: 2}) || s.After != (ScopeCounts{MatchedRows: 1, NonmatchingKnownRows: 1, UnknownIdentityRows: 1}) {
			t.Fatal("scope coverage was filtered or unknowns counted as nonmatches")
		}
		for _, c := range s.FieldCoverageByCallType {
			if c.CallType == "aresponses" && (c.Before.Agent.KnownRows != 2 || c.Before.Agent.ColumnAbsentRows != 1 || c.After.Agent.KnownRows != 2 || c.After.Agent.ColumnAbsentRows != 1) {
				t.Fatal("missing Responses agents hidden by selection")
			}
		}
		if audit && (r.RoutingAudit.ModelRows.Before.Requests != 1 || r.RoutingAudit.Coverage.Before.Agent.MissingRows != 2 || r.RoutingAudit.ClassifierOverhead.Before.Usage.Requests != 0) {
			t.Fatal("audit filter and coverage populations were conflated")
		}
		assertRoutingPrivate(t, r)
		o.ScopeAgent = "PRIVATE_ABSENT"
		r = routingReport(t, rows, o)
		if r.AnalysisGap == "" || !r.Incomplete || r.Before.Requests != 0 || r.ScopeCoverage.Before.UnknownIdentityRows != 2 || r.ScopeCoverage.Before.NonmatchingKnownRows != 2 {
			t.Fatal("empty match fabricated a zero-cost comparison")
		}
	}
}

func TestSelectionRankingUsesOnlyMatchedOrdinals(t *testing.T) {
	rows := []map[string]string{routingSample("1", "2026-09-01", 100, 0), routingSample("2", "2026-09-01", 20, 0), routingSample("3", "2026-09-01", 10, 0), routingSample("4", "2026-09-02", 30, 0), routingSample("5", "2026-09-02", 20, 0)}
	rows[0]["original_model_group"] = "PRIVATE_UNSELECTED"
	for i, key := range []string{"PRIVATE_FIRST", "PRIVATE_SECOND", "PRIVATE_FIRST", "PRIVATE_SECOND", "PRIVATE_FIRST"} {
		rows[i]["api_key"] = key
	}
	o := options(t)
	o.ScopeRoute, o.SecretEnv = "PRIVATE_ROUTE", ""
	var previous Report
	for i := range 5 {
		r := routingReport(t, rows, o)
		if len(r.Rankings.Movers) != 2 || r.Rankings.Movers[0].Before.Lower != 20 || r.Rankings.Movers[0].Item != "key-1" {
			t.Fatal("unselected row determined scoped ranking alias")
		}
		if i > 0 && !reflect.DeepEqual(previous, r) {
			t.Fatal("scoped aliases depend on random key")
		}
		previous = r
	}
}

func TestSelectionFullFileGuards(t *testing.T) {
	for _, audit := range []bool{false, true} {
		o := options(t)
		o.ScopeRoute, o.RoutingAudit = "PRIVATE_SCOPE_NEVER_MATCHES", audit
		rows := []map[string]string{routingSample("1", "2026-09-01", 1, 2), routingSample("1", "2026-08-01", 1, 2)}
		_, err := Investigate(writeRows(t, rows), o)
		if err == nil || !strings.Contains(err.Error(), "duplicate request_id") {
			t.Fatal("scope hid a duplicate outside selected periods")
		}
		rows = rows[:1]
		rows[0]["endTime"] = "2026-12-01T00:00:00Z"
		if _, err := Investigate(writeRows(t, rows), o); err == nil {
			t.Fatal("scope hid unfinished supported input")
		}
	}
}

func TestRoutingRankingDigestIncludesValuesAndPresence(t *testing.T) {
	for _, field := range []string{"original_model_group", "agent_id", "attempted_fallbacks", "internal_call_origin", "model", "model_id"} {
		for _, absent := range []bool{false, true} {
			rows := []map[string]string{routingSample("1", "2026-09-01", 1, 0), routingSample("2", "2026-09-02", 2, 0)}
			if absent {
				delete(rows[0], field)
			}
			o := routingOptions(t)
			o.ShowHashes = true
			path := writeRows(t, rows)
			r, err := Investigate(path, o)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.New()
			enc := json.NewEncoder(digest)
			if err := ReadRouting(path, func(row Row) error { return enc.Encode(row.Values) }); err != nil {
				t.Fatal(err)
			}
			rows[0][field] = "PRIVATE_CHANGED"
			if absent {
				rows[0][field] = ""
			}
			b, _ := json.Marshal(rows)
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			key, _, err := secret(o.SecretEnv)
			if err != nil {
				t.Fatal(err)
			}
			err = orderCandidates(path, &r, key, digest.Sum(nil), nil, true)
			clear(key)
			if err == nil || !strings.Contains(err.Error(), "export changed while reading") || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("routing-only change escaped immutable-file guard")
			}
		}
	}
}

func TestSelectionDoesNotAllocateAuditCardinality(t *testing.T) {
	o := options(t)
	o.ScopeAgent = "PRIVATE_AGENT"
	rows := []map[string]string{}
	for i := 0; i <= MaxRoutingRoutes; i++ {
		r := routingSample(fmt.Sprint(i), "2026-09-01", 1, 0)
		r["original_model_group"] = fmt.Sprint(i)
		rows = append(rows, r)
	}
	r := routingReport(t, rows, o)
	if r.RoutingAudit != nil || r.ScopeCoverage.Before.MatchedRows != MaxRoutingRoutes+1 {
		t.Fatal("scope-only path accumulated audit dimensions")
	}
	key := []byte("scope-state-test-key")
	s, _ := newScopeSelection(o, key)
	a := newRoutingAccumulator(o, s)
	for i, r := range rows {
		a.observe(Row{Values: maps.Clone(r), Number: i + 1}, 0, key)
	}
	if a.routes != nil || a.agents != nil || a.models != nil || a.cohorts != nil || a.cells != 0 {
		t.Fatal("scope-only state retained entity maps")
	}
}
