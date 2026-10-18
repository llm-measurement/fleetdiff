// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
)

func routingSample(id, day string, input, output int) map[string]string {
	r := sample(id, day, "PRIVATE_KEY", input, output)
	r["original_model_group"], r["model_group"] = "PRIVATE_ROUTE", "PRIVATE_RECOVERY_GROUP"
	r["agent_id"], r["model"], r["model_id"] = "PRIVATE_AGENT", "PRIVATE_MODEL", "PRIVATE_DEPLOYMENT"
	r["attempted_fallbacks"], r["internal_call_origin"] = "0", ""
	return r
}

func routingOptions(t *testing.T) Options {
	o := options(t)
	o.RoutingAudit = true
	return o
}

func routingReport(t *testing.T, rows []map[string]string, o Options) Report {
	t.Helper()
	r, err := Investigate(writeRows(t, rows), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func assertRoutingPrivate(t *testing.T, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PRIVATE_") {
		t.Fatal("raw private value escaped in report")
	}
}

func TestRoutingAccountingPartitionsAndShares(t *testing.T) {
	rows := []map[string]string{
		routingSample("1", "2026-09-01", 90, 10), routingSample("2", "2026-09-01", 180, 20),
		routingSample("3", "2026-09-01", 270, 30), routingSample("4", "2026-09-02", 19, 3),
		routingSample("5", "2026-09-02", 0, 0), routingSample("6", "2026-09-01", 50, 5),
		routingSample("7", "2026-09-01", 9, 1), routingSample("8", "2026-09-02", 7, 2),
	}
	rows[0]["spend"], rows[1]["spend"], rows[2]["spend"] = "1", "2", "3"
	rows[1]["model"], rows[1]["attempted_fallbacks"] = "PRIVATE_MODEL_SECOND", "1"
	delete(rows[2], "model_id")
	delete(rows[2], "attempted_fallbacks")
	rows[3]["status"], rows[3]["spend"] = "failure", "0.2"
	rows[4]["spend"] = "0"
	rows[5]["call_type"], rows[5]["internal_call_origin"], rows[5]["spend"] = "pass_through_endpoint", "autorouter_classifier", "0.4"
	rows[6]["call_type"], rows[6]["internal_call_origin"], rows[6]["spend"] = "pass_through_endpoint", "PRIVATE_ORIGIN", "0.1"
	rows[7]["call_type"], rows[7]["internal_call_origin"] = "PRIVATE_CALL", "autorouter_classifier"
	for _, hashes := range []bool{false, true} {
		o := routingOptions(t)
		o.ShowHashes = hashes
		r := routingReport(t, rows, o)
		a := r.RoutingAudit
		if a == nil || a.Schema != "fleetdiff-litellm-routing-audit/v1" || a.SourceContract != "litellm-1.104.0-spend-routing/v1" || a.IdentityBasis != "gateway-selected-deployment" || a.ProviderSnapshotConfirmed || a.GroupBy != "route" {
			t.Fatal("routing contract changed")
		}
		if a.ModelRows.Before.Requests != 3 || a.ModelRows.Before.Tokens != 600 || a.ModelRows.Before.RecordedSpend != "6" || a.ModelRows.Before.UnknownModelRows != 1 {
			t.Fatal("model totals did not conserve unknown-model contributions")
		}
		if a.ModelRows.After.Tokens != r.After.Tokens || a.ModelRows.After.RecordedSpend != r.After.RecordedSpend || !reflect.DeepEqual(a.ModelRows.After.FailureTokens, r.After.FailureTokens) {
			t.Fatal("audit changed established accounting or failure subset")
		}
		if a.ModelRows.After.FailureTokens == nil || a.ModelRows.After.FailureTokens.Total != 22 || a.ModelRows.After.FailureTokens.Finality != "unknown" {
			t.Fatal("failure tokens lost their recorded-subset finality")
		}
		if a.ClassifierOverhead.Before.Usage.Requests != 1 || a.ClassifierOverhead.Before.Usage.TotalTokens != 55 || a.ClassifierOverhead.Before.RecordedSpend != "0.4" || a.ClassifierOverhead.After.Usage.Requests != 0 || a.OtherRows.Before.Usage.Requests != 1 || a.OtherRows.After.Usage.Requests != 1 {
			t.Fatal("exact classifier partition failed")
		}
		if len(a.Cohorts) != 1 || a.Cohorts[0].Before.Fallbacks != (RoutingFallbacks{KnownZeroRows: 1, KnownPositiveRows: 1, UnknownRows: 1}) {
			t.Fatal("recorded fallback counts were inferred or lost")
		}
		cohort := a.Cohorts[0]
		if cohort.Agent != nil || len(cohort.Models) != 3 {
			t.Fatal("unexpected cohort dimensions")
		}
		for _, m := range cohort.Models {
			want := "0.166666666667"
			if m.Item == "selected-model-2" {
				want = "0.333333333333"
			}
			if m.State == "unknown" {
				want = "0.500000000000"
				if m.Hash != "" || m.Item != "selected-model-unknown" {
					t.Fatal("unknown model became an entity")
				}
			}
			if m.SharesBefore.Requests == nil || *m.SharesBefore.Requests != "0.333333333333" || m.SharesBefore.Tokens == nil || *m.SharesBefore.Tokens != want || m.SharesBefore.Spend == nil || *m.SharesBefore.Spend != want {
				t.Fatal("shares excluded unknown model tokens or cost")
			}
		}
		if (cohort.Route.Hash != "") != hashes {
			t.Fatal("hash display option ignored")
		}
		for _, c := range a.Coverage.ByCallType {
			if c.CallType != "completion" && c.CallType != "pass_through_endpoint" && c.CallType != "unknown" {
				t.Fatal("private call type was retained")
			}
		}
		assertRoutingPrivate(t, r)
		o.RoutingAudit = false
		legacy := routingReport(t, rows, o)
		r.RoutingAudit = nil
		got, _ := json.Marshal(r)
		want, _ := json.Marshal(legacy)
		if string(got) != string(want) {
			t.Fatal("stripping the additive audit did not recover legacy report bytes")
		}
	}
}

func TestRoutingSelectedEvidenceAndUnknownBuckets(t *testing.T) {
	rows := []map[string]string{}
	for i, edit := range []func(map[string]string){
		func(r map[string]string) { delete(r, "model_id") },
		func(r map[string]string) { delete(r, "model") },
		func(r map[string]string) { r["model"] = "__unknown__" },
		func(r map[string]string) { r["model_id"] = "unknown" },
		func(r map[string]string) { r["model"] = " \t " },
		func(r map[string]string) { r["model_id"] = "" },
	} {
		r := routingSample(fmt.Sprint(i), "2026-09-01", 10, 1)
		delete(r, "original_model_group")
		delete(r, "agent_id")
		edit(r)
		rows = append(rows, r)
	}
	o := routingOptions(t)
	o.RoutingBy, o.ShowHashes = "route-agent", true
	r := routingReport(t, rows, o)
	a := r.RoutingAudit
	if len(a.Cohorts) != 1 || a.ModelRows.Before.UnknownModelRows != 6 {
		t.Fatal("unavailable selected evidence was promoted")
	}
	c := a.Cohorts[0]
	if c.Route.Item != "route-unknown" || c.Route.Hash != "" || c.Agent == nil || c.Agent.Item != "agent-unknown" || c.Agent.Hash != "" || len(c.Models) != 1 || c.Models[0].Item != "selected-model-unknown" {
		t.Fatal("missing identity inferred from another column")
	}
	if a.Coverage.Before.SelectedModel.InvalidRows != 1 || a.Coverage.Before.DeploymentID.InvalidRows != 1 {
		t.Fatal("placeholder coverage missing")
	}
	assertRoutingPrivate(t, r)
	for _, value := range []string{"unknown", "Unknown", "unknown_model", "__unknown__", "__other__", "__missing__", "__unmapped__", "__overflow__", " \t", string([]byte{0xff}), strings.Repeat("x", MaxFieldBytes+1)} {
		if RoutingSelectedTextKnown(value) {
			t.Fatal("unavailable selected text admitted")
		}
	}
}

func TestRoutingFieldCoverageAndFallbackStates(t *testing.T) {
	for _, tc := range []struct {
		value string
		state routingFieldState
		n     uint64
	}{
		{"", routingEmpty, 0}, {" \t", routingEmpty, 0}, {"0", routingKnown, 0}, {"0001", routingKnown, 1},
		{"9223372036854775807", routingKnown, 9223372036854775807}, {"9223372036854775808", routingInvalid, 0},
		{"-1", routingInvalid, 0}, {"+1", routingInvalid, 0}, {"1.0", routingInvalid, 0}, {"1e1", routingInvalid, 0}, {" 1", routingInvalid, 0}, {"PRIVATE_NUMBER", routingInvalid, 0},
	} {
		n, state := routingFallback(Row{Values: map[string]string{"attempted_fallbacks": tc.value}})
		if state != tc.state || n != tc.n {
			t.Fatal("fallback scalar classification changed")
		}
	}
	if _, state := routingFallback(Row{Values: map[string]string{}}); state != routingAbsent {
		t.Fatal("absent fallback became zero")
	}
	for _, field := range []string{"agent_id", "original_model_group", "attempted_fallbacks", "model", "model_id", "internal_call_origin"} {
		t.Run(field, func(t *testing.T) {
			for _, format := range []string{"csv", "json", "jsonl"} {
				rows := make([]map[string]any, 3)
				for i := range rows {
					rows[i] = map[string]any{}
					for k, v := range routingSample(fmt.Sprint(i), "2026-09-01", 1, 1) {
						rows[i][k] = v
					}
					rows[i][field] = []any{nil, "", " \t"}[i]
				}
				r, err := Investigate(writeSessionAttributionRows(t, format, rows), routingOptions(t))
				if err != nil {
					t.Fatal(err)
				}
				c := routingCoverageField(r.RoutingAudit.Coverage.Before, field)
				if c.EmptyOrNullRows != 3 || c.MissingRows != 3 || c.KnownRows != 0 || c.ColumnAbsentRows != 0 {
					t.Fatal("present empty/null state changed across formats")
				}
				for _, row := range rows {
					delete(row, field)
				}
				r, err = Investigate(writeSessionAttributionRows(t, format, rows), routingOptions(t))
				if err != nil {
					t.Fatal(err)
				}
				c = routingCoverageField(r.RoutingAudit.Coverage.Before, field)
				if c.ColumnAbsentRows != 3 || c.MissingRows != 3 || c.EmptyOrNullRows != 0 {
					t.Fatal("absent column state changed across formats")
				}
			}
		})
	}
}

func routingCoverageField(f RoutingFields, field string) RoutingFieldCoverage {
	switch field {
	case "agent_id":
		return f.Agent
	case "original_model_group":
		return f.OriginalRoute
	case "attempted_fallbacks":
		return f.AttemptedFallbacks
	case "model":
		return f.SelectedModel
	case "model_id":
		return f.DeploymentID
	default:
		return f.OverheadMarker
	}
}

func TestRoutingZeroAndPartialMeasures(t *testing.T) {
	zero := routingSample("1", "2026-09-01", 0, 0)
	zero["spend"] = "0"
	r := routingReport(t, []map[string]string{zero}, routingOptions(t))
	m := r.RoutingAudit.Cohorts[0].Models[0]
	if m.SharesBefore.Requests == nil || *m.SharesBefore.Requests != "1.000000000000" || m.SharesBefore.Tokens != nil || m.SharesBefore.Spend != nil || m.SharesAfter.Requests != nil || m.Before.Quality.ZeroUnknown != 1 || m.Before.Quality.SpendZeroUnknown != 1 {
		t.Fatal("zero denominator or zero origin became known")
	}
	rows := []map[string]string{routingSample("1", "2026-09-01", 10, 2), routingSample("2", "2026-09-01", 10, 2), routingSample("3", "2026-09-01", 10, 2)}
	rows[0]["prompt_tokens"] = ""
	rows[0]["spend"] = ""
	rows[1]["prompt_tokens"], rows[1]["spend"] = "PRIVATE_BAD_TOKEN", "PRIVATE_BAD_SPEND"
	rows[2]["spend"] = "0.00000000000000000001"
	r = routingReport(t, rows, routingOptions(t))
	w := r.RoutingAudit.ModelRows.Before
	if w.Quality.Missing != 2 || w.Quality.Invalid != 1 || w.Quality.SpendMissing != 1 || w.Quality.SpendInvalid != 1 || w.RecordedSpend != "0.00000000000000000001" || w.Tokens != r.Before.Tokens {
		t.Fatalf("partial accounting or exact subtotal changed: quality=%+v tokens=%d spend=%s", w.Quality, w.Tokens, w.RecordedSpend)
	}
	assertRoutingPrivate(t, r)
}

func TestRoutingRawCoverageAndOnlyOverhead(t *testing.T) {
	rows := []map[string]string{routingSample("1", "2026-09-01", 0, 0), routingSample("2", "2026-09-01", 0, 0)}
	for _, r := range rows {
		r["call_type"], r["internal_call_origin"] = "pass_through_endpoint", "autorouter_classifier"
	}
	rows[0]["prompt_tokens"], rows[0]["completion_tokens"], rows[0]["total_tokens"] = "9223372036854775807", "PRIVATE_INVALID", ""
	rows[1]["prompt_tokens"], rows[1]["completion_tokens"], rows[1]["total_tokens"] = "1", "0", "0"
	rows[0]["spend"], rows[1]["spend"] = "PRIVATE_COST", ""
	r := routingReport(t, rows, routingOptions(t))
	w := r.RoutingAudit.ClassifierOverhead.Before
	if r.AnalysisGap == "" || r.Before.Requests != 0 || w.Usage.Requests != 2 || !w.Usage.PromptTokensOverflow || w.Usage.InvalidUsage != 1 || w.Usage.MissingUsage != 1 || w.SpendInvalid != 1 || w.SpendMissing != 1 {
		t.Fatal("unsupported raw evidence was lost or adapted as model usage")
	}
	b, _ := json.Marshal(w)
	if strings.Contains(string(b), `"prompt_tokens":`) {
		t.Fatal("overflow raw subtotal was reported as zero")
	}
	assertRoutingPrivate(t, r)
}

func TestRoutingTopRemainderAndStableAliases(t *testing.T) {
	rows := []map[string]string{}
	for i, values := range [][2]string{{"r1", "m1"}, {"r1", "m2"}, {"r2", "m1"}, {"r3", "m3"}} {
		r := routingSample(fmt.Sprint(i), "2026-09-01", i+1, 1)
		r["original_model_group"], r["model"] = values[0], values[1]
		r["spend"] = fmt.Sprint(i + 1)
		rows = append(rows, r)
	}
	o := routingOptions(t)
	all := routingReport(t, rows, o)
	o.Top, o.SecretEnv = 1, ""
	var previous *RoutingAudit
	for range 5 {
		r := routingReport(t, rows, o)
		a := r.RoutingAudit
		if !reflect.DeepEqual(a.ModelRows, all.RoutingAudit.ModelRows) || !reflect.DeepEqual(a.Coverage, all.RoutingAudit.Coverage) || len(a.Cohorts) != 1 || len(a.Cohorts[0].Models) != 1 || a.Cohorts[0].Route.Item != "route-1" || a.Cohorts[0].Models[0].Item != "selected-model-1" {
			t.Fatal("top changed totals or first-observed aliases")
		}
		if a.DisplayRemainder.Cohorts != 2 || a.DisplayRemainder.ModelCells != 3 || a.DisplayRemainder.Before.Requests != 3 || a.DisplayRemainder.Before.Tokens != 12 || a.DisplayRemainder.Before.RecordedSpend != "9" {
			t.Fatal("display remainder lost or double counted cells")
		}
		if *a.Cohorts[0].Models[0].SharesBefore.Requests != "0.500000000000" {
			t.Fatal("displayed shares were renormalized")
		}
		if previous != nil && !reflect.DeepEqual(previous, a) {
			t.Fatal("key randomness changed aliases or order")
		}
		previous = a
	}
}

func TestRoutingDefaultBytesAndAutomaticPeriods(t *testing.T) {
	rows := []map[string]string{sample("1", "2026-09-01", "k", 1, 2), sample("2", "2026-09-02", "k", 2, 3)}
	o := options(t)
	before := routingReport(t, rows, o)
	for _, row := range rows {
		row["original_model_group"], row["attempted_fallbacks"], row["internal_call_origin"] = "PRIVATE_ROUTE", "PRIVATE_INVALID_COUNT", "PRIVATE_ORIGIN"
	}
	after := routingReport(t, rows, o)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) || after.RoutingAudit != nil || after.ScopeCoverage != nil {
		t.Fatal("default bytes changed with additive routing columns")
	}
	rows = []map[string]string{}
	for day := 1; day <= 17; day++ {
		rows = append(rows, routingSample(fmt.Sprint(day), fmt.Sprintf("2026-09-%02d", day), 1, 2))
	}
	o.BeforePeriod, o.AfterPeriod = "", ""
	before = routingReport(t, rows, o)
	o.RoutingAudit = true
	after = routingReport(t, rows, o)
	if after.RoutingAudit.ModelRows.Before.Requests != 7 || after.RoutingAudit.ModelRows.After.Requests != 7 {
		t.Fatal("automatic routing selection changed")
	}
	after.RoutingAudit = nil
	if !reflect.DeepEqual(before, after) {
		t.Fatal("automatic audit altered legacy fields")
	}
}

func TestRoutingLimits(t *testing.T) {
	key := []byte("routing-limit-test-key")
	row := Row{Values: routingSample("1", "2026-09-01", 1, 1)}
	obs, err := accounting.Tokens(attributes(row))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		count int
		edit  func(map[string]string, int)
	}{
		{"routes", MaxRoutingRoutes, func(r map[string]string, i int) { r["original_model_group"] = fmt.Sprint(i) }},
		{"agents", MaxRoutingAgents, func(r map[string]string, i int) { r["agent_id"] = fmt.Sprint(i) }},
		{"models", MaxRoutingModels, func(r map[string]string, i int) { r["model"] = fmt.Sprint(i) }},
		{"cohorts", MaxRoutingCohorts, func(r map[string]string, i int) {
			r["original_model_group"], r["agent_id"] = fmt.Sprint(i/MaxRoutingAgents), fmt.Sprint(i%MaxRoutingAgents)
		}},
		{"cells", MaxRoutingCells, func(r map[string]string, i int) {
			r["original_model_group"], r["model"] = fmt.Sprint(i/MaxRoutingModels), fmt.Sprint(i%MaxRoutingModels)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newRoutingAccumulator(Options{RoutingAudit: true, RoutingBy: "route-agent"}, nil)
			for i := 0; i < tc.count; i++ {
				r := Row{Values: maps.Clone(row.Values), Number: i + 1}
				tc.edit(r.Values, i)
				if err := a.add(r, i%2, key, obs, Quality{}); err != nil {
					t.Fatal("routing cap rejected its boundary", err)
				}
			}
			if tc.name == "routes" || tc.name == "agents" || tc.name == "models" {
				r := Row{Values: maps.Clone(row.Values), Number: tc.count + 1}
				field := map[string]string{"routes": "original_model_group", "agents": "agent_id", "models": "model"}[tc.name]
				delete(r.Values, field)
				if err := a.add(r, 0, key, obs, Quality{}); err != nil {
					t.Fatal("unknown bucket consumed a known-identity slot", err)
				}
			}
			if _, _, err := a.result(1, false); err != nil {
				t.Fatal("bounded remainder failed", err)
			}
			r := Row{Values: maps.Clone(row.Values), Number: tc.count + 2}
			tc.edit(r.Values, tc.count)
			if tc.name == "cells" {
				r.Values["original_model_group"] = "0"
				delete(r.Values, "model_id")
			}
			if err := a.add(r, 0, key, obs, Quality{}); err == nil || !strings.Contains(err.Error(), "routing audit exceeds") || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("routing cap did not fail safely one over")
			}
		})
	}
}

func TestRoutingModelAliasesAreDisjointFromLegacy(t *testing.T) {
	rows := []map[string]string{routingSample("1", "2026-09-01", 100, 0), routingSample("2", "2026-09-01", 10, 0), routingSample("3", "2026-09-02", 100, 0)}
	rows[1]["model"], rows[2]["model"] = "PRIVATE_SECOND", "PRIVATE_SECOND"
	o := routingOptions(t)
	o.ShowHashes = true
	r := routingReport(t, rows, o)
	if r.Models[0].Item != "model-1" || r.Models[0].Before.Tokens != 10 {
		t.Fatal("legacy delta ordering changed")
	}
	legacy := map[string]string{}
	for _, m := range r.Models {
		legacy[m.Item] = m.Hash
	}
	var first string
	for _, c := range r.RoutingAudit.Cohorts {
		for _, m := range c.Models {
			if _, collision := legacy[m.Item]; collision || !strings.HasPrefix(m.Item, "selected-model-") {
				t.Fatal("audit alias overlaps a legacy model alias")
			}
			if m.Item == "selected-model-1" {
				first = m.Hash
			}
		}
	}
	if first == "" || first == r.Models[0].Hash || first != r.Models[1].Hash {
		t.Fatal("divergent alias order regression was not exercised")
	}
}

func TestRoutingCoverageReconcilesAllCallTypes(t *testing.T) {
	rows := []map[string]string{}
	for i, callType := range []string{"completion", "acompletion", "text_completion", "atext_completion", "anthropic_messages", "aanthropic_messages", "responses", "aresponses", "pass_through_endpoint", "PRIVATE_CALL_A", "PRIVATE_CALL_B"} {
		for side, day := range []string{"2026-09-01", "2026-09-02"} {
			r := routingSample(fmt.Sprintf("%d-%d", i, side), day, 1, 0)
			r["call_type"] = callType
			if i%2 == 0 {
				delete(r, "agent_id")
			}
			if i%3 == 0 {
				r["original_model_group"] = ""
			}
			if i%4 == 0 {
				r["attempted_fallbacks"] = "PRIVATE_INVALID_COUNT"
			}
			r["internal_call_origin"] = "autorouter_classifier"
			rows = append(rows, r)
		}
	}
	r := routingReport(t, rows, routingOptions(t))
	a := r.RoutingAudit
	if len(a.Coverage.ByCallType) != 10 || a.ModelRows.Before.Requests != 8 || a.ClassifierOverhead.Before.Usage.Requests != 1 || a.OtherRows.Before.Usage.Requests != 2 {
		t.Fatal("call-type partition did not reconcile")
	}
	for _, field := range []string{"agent_id", "original_model_group", "attempted_fallbacks", "model", "model_id", "internal_call_origin"} {
		for side := range 2 {
			total := RoutingFieldCoverage{}
			for _, c := range a.Coverage.ByCallType {
				f := c.Before
				if side == 1 {
					f = c.After
				}
				v := routingCoverageField(f, field)
				total.KnownRows += v.KnownRows
				total.ColumnAbsentRows += v.ColumnAbsentRows
				total.EmptyOrNullRows += v.EmptyOrNullRows
				total.InvalidRows += v.InvalidRows
				total.MissingRows += v.MissingRows
			}
			f := a.Coverage.Before
			if side == 1 {
				f = a.Coverage.After
			}
			if total != routingCoverageField(f, field) || total.KnownRows+total.MissingRows != 11 || total.MissingRows != total.ColumnAbsentRows+total.EmptyOrNullRows+total.InvalidRows {
				t.Fatal("field states or per-call-type coverage did not reconcile")
			}
		}
	}
	assertRoutingPrivate(t, r)
}

func TestRoutingOverheadMarkersAndNoInferredFallback(t *testing.T) {
	rows := []map[string]string{}
	for i, pair := range [][2]string{
		{"pass_through_endpoint", "autorouter_classifier"}, {"pass_through_endpoint", " autorouter_classifier"},
		{"pass_through_endpoint", "autorouter_classifier "}, {"pass_through_endpoint", "Autorouter_classifier"},
		{"pass_through_endpoint ", "autorouter_classifier"}, {"pass_through_endpoint", ""},
		{"completion", "autorouter_classifier"}, {"completion", "autorouter_classifier"},
	} {
		r := routingSample(fmt.Sprint(i), "2026-09-01", 1, 0)
		r["call_type"], r["internal_call_origin"], r["model"] = pair[0], pair[1], fmt.Sprintf("PRIVATE_MODEL_%d", i)
		rows = append(rows, r)
	}
	a := routingReport(t, rows, routingOptions(t)).RoutingAudit
	if a.ModelRows.Before.Requests != 2 || a.ClassifierOverhead.Before.Usage.Requests != 1 || a.OtherRows.Before.Usage.Requests != 5 || a.ModelRows.Before.Fallbacks.KnownZeroRows != 2 || a.ModelRows.Before.Fallbacks.KnownPositiveRows != 0 {
		t.Fatal("marker inference or model variation invented overhead/fallbacks")
	}
}

func TestRoutingRouteAgentIdentityAndPartialAgents(t *testing.T) {
	rows := []map[string]string{routingSample("1", "2026-09-01", 1, 0), routingSample("2", "2026-09-01", 1, 0), routingSample("3", "2026-09-02", 1, 0)}
	rows[0]["agent_id"] = rows[0]["original_model_group"]
	rows[1]["agent_id"] = ""
	rows[2]["agent_id"] = rows[0]["agent_id"]
	o := routingOptions(t)
	o.RoutingBy, o.ShowHashes = "route-agent", true
	a := routingReport(t, rows, o).RoutingAudit
	if len(a.Cohorts) != 2 || a.Coverage.Before.Agent.KnownRows != 1 || a.Coverage.Before.Agent.EmptyOrNullRows != 1 {
		t.Fatal("partial agent identities lost coverage or cohorts")
	}
	for _, c := range a.Cohorts {
		if c.Agent == nil {
			t.Fatal("route-agent dimension missing")
		}
		if c.Agent.State == "known" && (c.Route.Hash == c.Agent.Hash || c.Agent.Item != "agent-1") {
			t.Fatal("route/agent namespace or alias changed")
		}
		if c.Agent.State == "unknown" && (c.Agent.Hash != "" || c.Before.Requests != 1) {
			t.Fatal("unknown agent is not a typed one-row bucket")
		}
	}
}

func BenchmarkRoutingBoundedState(b *testing.B) {
	for _, layout := range []string{"dense-models", "many-cohorts"} {
		b.Run(layout, func(b *testing.B) {
			b.ReportAllocs()
			key := []byte("routing-benchmark-key")
			row := Row{Values: routingSample("1", "2026-09-01", 1, 0)}
			obs, err := accounting.Tokens(attributes(row))
			if err != nil {
				b.Fatal(err)
			}
			for range b.N {
				a := newRoutingAccumulator(Options{RoutingAudit: true, RoutingBy: "route-agent"}, nil)
				for i := range MaxRoutingCells {
					row.Number = i + 1
					if layout == "dense-models" {
						row.Values["original_model_group"], row.Values["model"] = fmt.Sprint(i/MaxRoutingModels), fmt.Sprint(i%MaxRoutingModels)
					} else {
						cohort := i / 4
						row.Values["original_model_group"], row.Values["agent_id"], row.Values["model"] = fmt.Sprint(cohort/MaxRoutingAgents), fmt.Sprint(cohort%MaxRoutingAgents), fmt.Sprint(i%MaxRoutingModels)
					}
					if err := a.add(row, i%2, key, obs, Quality{}); err != nil {
						b.Fatal(err)
					}
				}
				r, _, err := a.result(100, false)
				if err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(r)
			}
		})
	}
}
