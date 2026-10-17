// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func routingRepriceRow(id string) map[string]any {
	r := map[string]any{}
	for k, v := range repriceRow(id) {
		r[k] = v
	}
	r["model"], r["model_id"] = "claude-sonnet-5-5", "private-deployment-sentinel"
	r["original_model_group"], r["agent_id"] = "private-route-sentinel", "private-agent-sentinel"
	r["model_group"] = "private-recovery-group-sentinel"
	return r
}

func routingRepriceFile(t *testing.T, ext string, rows []map[string]any) string {
	t.Helper()
	var b bytes.Buffer
	if ext == ".csv" {
		columns := []string{}
		for _, row := range rows {
			for k := range row {
				if !slices.Contains(columns, k) {
					columns = append(columns, k)
				}
			}
		}
		slices.Sort(columns)
		w := csv.NewWriter(&b)
		if err := w.Write(columns); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			values := make([]string, len(columns))
			for i, k := range columns {
				if row[k] != nil {
					values[i] = fmt.Sprint(row[k])
				}
			}
			if err := w.Write(values); err != nil {
				t.Fatal(err)
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			t.Fatal(err)
		}
	} else {
		enc := json.NewEncoder(&b)
		if ext == ".json" {
			if err := enc.Encode(rows); err != nil {
				t.Fatal(err)
			}
		} else {
			for _, row := range rows {
				if err := enc.Encode(row); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return readerWrite(t, ext, b.String())
}

func TestReadRoutingRepriceFormatsCoverageAndLegacy(t *testing.T) {
	for _, ext := range []string{".csv", ".json", ".jsonl"} {
		t.Run(ext, func(t *testing.T) {
			rows := []map[string]any{}
			for i := range 6 {
				rows = append(rows, routingRepriceRow(fmt.Sprint(i)))
			}
			rows[1]["call_type"], rows[1]["agent_id"] = "aresponses", nil
			delete(rows[2], "original_model_group")
			rows[2]["model"], rows[2]["model_id"], rows[2]["agent_id"] = nil, nil, " \t "
			rows[3]["call_type"] = "private-call-type-sentinel"
			rows[4]["call_type"], rows[4]["internal_call_origin"] = "pass_through_endpoint", "private-origin-sentinel"
			rows[5]["startTime"] = "2020-01-03T00:00:00Z"
			path := routingRepriceFile(t, ext, rows)
			var records []reprice.Record
			source, err := ReadRoutingReprice(path, repriceDay, func(r reprice.Record) error { records = append(records, r); return nil })
			if err != nil || source.Rows != 5 || source.Outside != 1 || len(records) != 5 || source.Attribution == nil {
				t.Fatal("routing format changed period or record coverage")
			}
			a := source.Attribution
			if a.Rows != 5 || a.Route != (reprice.IdentityCoverage{Known: 4, Unknown: 1}) ||
				a.Agent != (reprice.IdentityCoverage{Known: 3, Unknown: 2}) ||
				a.Model != a.Route || a.Deployment != a.Route || a.ServedIdentity != a.Route || len(a.ByCallType) != 4 {
				t.Fatal("source attribution did not include all pre-exclusion rows")
			}
			if a.ByCallType[0].CallType != "aresponses" || a.ByCallType[0].Agent.Unknown != 1 ||
				a.ByCallType[1].CallType != "completion" || a.ByCallType[1].Rows != 2 ||
				a.ByCallType[2].CallType != "pass_through_endpoint" || a.ByCallType[3].CallType != "unknown" {
				t.Fatal("call type coverage is not bounded, ordered or complete")
			}
			if records[0].Route != rows[0]["original_model_group"] || records[0].Agent != rows[0]["agent_id"] || !records[0].ServedIdentityKnown ||
				records[2].Route != "" || records[2].ServedIdentityKnown || records[3].Issue != "unsupported_call_type" {
				t.Fatal("original route or selected deployment evidence was inferred")
			}
			body, _ := json.Marshal(source)
			if strings.Contains(string(body), "sentinel") || strings.Contains(string(body), path) {
				t.Fatal("coverage leaked private source values")
			}
			var legacy []reprice.Record
			old, err := ReadReprice(path, repriceDay, func(r reprice.Record) error { legacy = append(legacy, r); return nil })
			if err != nil || old.Attribution != nil {
				t.Fatal("legacy reader opted into routing")
			}
			source.Attribution = nil
			for i := range records {
				records[i].Route, records[i].Agent, records[i].ServedIdentityKnown = "", "", false
			}
			if !reflect.DeepEqual(source, old) || !reflect.DeepEqual(records, legacy) {
				t.Fatal("routing changed legacy accounting or source fields")
			}
		})
	}
}

func TestReadRoutingRepriceMissingIdentityAndNoAliasPricing(t *testing.T) {
	for _, field := range []string{"original_model_group", "agent_id", "model", "model_id"} {
		for _, state := range []string{"absent", "null", "empty", "blank"} {
			t.Run(field+"/"+state, func(t *testing.T) {
				row := routingRepriceRow("one")
				switch state {
				case "absent":
					delete(row, field)
				case "null":
					row[field] = nil
				case "empty":
					row[field] = ""
				case "blank":
					row[field] = " \t "
				}
				p, f, err := reprice.Bundled()
				if err != nil {
					t.Fatal(err)
				}
				kind := "route"
				if field == "agent_id" {
					kind = "agent"
				}
				c, err := reprice.New(p, f, reprice.Scenario{Version: 1, Moves: []reprice.Move{{From: "claude-sonnet-5-5", To: "gpt-6.1-sol", Fraction: "0.5", Scope: &reprice.Selector{Kind: kind, Value: "private-" + kind + "-sentinel"}}}}, reprice.WorkloadMap{})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				source, err := ReadRoutingReprice(routingRepriceFile(t, ".json", []map[string]any{row}), repriceDay, c.Add)
				if err != nil {
					t.Fatal(err)
				}
				want := "missing_scope_identity"
				if field == "model" || field == "model_id" {
					want = "unknown_served_identity"
				}
				got := c.Result(source)
				if got.Covered != 0 || got.Excluded[want] != 1 || got.Before != nil {
					t.Fatal("missing attribution was inferred from model_group or priced as an alias")
				}
			})
		}
	}
	for _, value := range []string{"unknown", "Unknown", "__missing__", "__unmapped__", "__overflow__", "\xff", strings.Repeat("x", MaxFieldBytes+1)} {
		for _, field := range []string{"model", "model_id"} {
			row := Row{Values: map[string]string{"model": "claude-sonnet-5-5", "model_id": "private-deployment-sentinel"}}
			row.Values[field] = value
			if RoutingSelectedModelKnown(row) {
				t.Fatal("reserved or malformed selected identity accepted")
			}
		}
	}
}

func TestReadRoutingRepriceWholeFileReplayAndLateErrors(t *testing.T) {
	first, outside := routingRepriceRow("private-replay-sentinel"), routingRepriceRow("private-replay-sentinel")
	outside["startTime"] = "2020-01-03T00:00:00Z"
	source, err := ReadRoutingReprice(routingRepriceFile(t, ".jsonl", []map[string]any{first, outside}), repriceDay, func(reprice.Record) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "duplicate request_id") || strings.Contains(err.Error(), "sentinel") || !reflect.DeepEqual(source, reprice.Source{}) {
		t.Fatal("routing lost whole-file replay protection")
	}
	data, _ := json.Marshal(first)
	visits := 0
	source, err = ReadRoutingReprice(readerWrite(t, ".jsonl", string(data)+"\n{\"private-sentinel\":[]}"), repriceDay, func(reprice.Record) error { visits++; return nil })
	if err == nil || visits != 1 || strings.Contains(err.Error(), "sentinel") || !reflect.DeepEqual(source, reprice.Source{}) {
		t.Fatal("late input error failed to invalidate source")
	}
	want := errors.New("safe visitor error")
	_, err = ReadRoutingReprice(routingRepriceFile(t, ".json", []map[string]any{first}), repriceDay, func(reprice.Record) error { return want })
	if !errors.Is(err, want) {
		t.Fatal("visitor error was not preserved")
	}
}
