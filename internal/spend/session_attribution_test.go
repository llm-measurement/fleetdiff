// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func sessionAttributionRows(agents []any) []map[string]any {
	rows := make([]map[string]any, len(agents))
	for i, agent := range agents {
		row := map[string]any{}
		for k, v := range sessionRow(fmt.Sprint(i), "private-session", "2026-09-02", "0.25") {
			row[k] = v
		}
		row["call_type"], row["agent_id"] = "aresponses", agent
		row["prompt_tokens"], row["completion_tokens"], row["total_tokens"] = 10, 2, 12
		rows[i] = row
	}
	return rows
}

func writeSessionAttributionRows(t *testing.T, format string, rows []map[string]any) string {
	t.Helper()
	var b bytes.Buffer
	switch format {
	case "csv":
		columns := []string{}
		for _, row := range rows {
			for key := range row {
				if !slices.Contains(columns, key) {
					columns = append(columns, key)
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
			for i, key := range columns {
				if row[key] != nil {
					values[i] = fmt.Sprint(row[key])
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
	case "jsonl":
		for _, row := range rows {
			if err := json.NewEncoder(&b).Encode(row); err != nil {
				t.Fatal(err)
			}
		}
	default:
		if err := json.NewEncoder(&b).Encode(rows); err != nil {
			t.Fatal(err)
		}
	}
	return readerWrite(t, "."+format, b.String())
}

func TestSessionAttributionMissingAndPartial(t *testing.T) {
	for _, format := range []string{"csv", "json", "jsonl"} {
		for _, tc := range []struct {
			name                    string
			agents                  []any
			known, distinct, shared uint64
		}{
			{"all-known", []any{"private-a", "private-a", "private-a", "private-a"}, 4, 1, 0},
			{"all-known-sharing", []any{"private-a", "private-b", "private-a", "private-b"}, 4, 2, 1},
			{"partial-sharing", []any{"private-a", "private-b", nil, nil}, 2, 2, 1},
			{"partial-one", []any{"private-a", "private-a", nil, ""}, 2, 1, 0},
			{"null", []any{nil, nil, nil, nil}, 0, 0, 0},
			{"empty", []any{"", "", "", ""}, 0, 0, 0},
			{"missing", []any{nil, nil, nil, nil}, 0, 0, 0},
			{"keys", []any{nil, nil, nil, nil}, 0, 0, 1},
			{"teams", []any{nil, nil, nil, nil}, 0, 0, 1},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				rows := sessionAttributionRows(tc.agents)
				if tc.name == "missing" {
					for _, row := range rows {
						delete(row, "agent_id")
					}
				}
				if tc.name == "keys" {
					rows[1]["api_key"] = "second-private-key"
				}
				if tc.name == "teams" {
					rows[1]["team_id"] = "second-private-team"
				}
				opts := sessionOptions()
				opts.BudgetUSD = "0.5"
				r, err := ReviewSessions(writeSessionAttributionRows(t, format, rows), opts)
				if err != nil {
					t.Fatal(err)
				}
				if r.SharedCounters != tc.shared || len(r.SharedCounterReview) != int(tc.shared) {
					t.Fatalf("sharing changed: %+v", r)
				}
				var want *SessionAgentCoverage
				if tc.known != 4 {
					want = &SessionAgentCoverage{KnownRows: tc.known, MissingRows: 4 - tc.known}
				}
				if !reflect.DeepEqual(r.AgentAttribution, want) {
					t.Fatalf("coverage: got %+v, want %+v", r.AgentAttribution, want)
				}
				if want == nil {
					if r.AgentAttributionByCallType != nil {
						t.Fatal("fully attributed report gained call-type fields")
					}
				} else if !reflect.DeepEqual(r.AgentAttributionByCallType, map[string]SessionAgentCoverage{"aresponses": *want}) {
					t.Fatalf("call-type coverage: %+v", r.AgentAttributionByCallType)
				}
				for _, list := range [][]Session{r.Largest, r.CapReview, r.SharedCounterReview} {
					for _, s := range list {
						if s.Agents != tc.distinct || !reflect.DeepEqual(s.AgentAttribution, want) {
							t.Fatalf("session coverage: %+v", s)
						}
					}
				}
				data, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(data), fmt.Sprintf(`"agents":%d`, tc.distinct)) || strings.Contains(string(data), `"agent_attribution"`) != (want != nil) {
					t.Fatalf("JSON numeric agents or conditional coverage changed: %s", data)
				}
				// Hold every legacy accounting field fixed while changing only
				// agent identity availability and its observed sharing evidence.
				for _, row := range rows {
					row["agent_id"] = "one-private-agent"
				}
				baseline, err := ReviewSessions(writeSessionAttributionRows(t, format, rows), opts)
				if err != nil {
					t.Fatal(err)
				}
				for _, report := range []*SessionReport{&r, &baseline} {
					report.AgentAttribution, report.AgentAttributionByCallType = nil, nil
					report.SharedCounters, report.SharedCounterReview = 0, nil
					for _, list := range [][]Session{report.Largest, report.CapReview, report.LongSpanReview} {
						for i := range list {
							list[i].Agents, list[i].AgentAttribution = 0, nil
						}
					}
				}
				if !reflect.DeepEqual(r, baseline) || r.ModelCalls != 4 || r.RecordedTokens != 48 || r.Largest[0].RecordedSpend != "1" || r.Exceeding != 1 || r.UnattributedRows != 0 || r.Quality.MissingIdentity != 0 {
					t.Fatalf("agent availability changed accounting: %+v versus %+v", r, baseline)
				}
			})
		}
	}
}

func TestSessionAttributionScopeCallTypesAndPrivacy(t *testing.T) {
	callTypes := []string{"completion", "acompletion", "anthropic_messages", "aanthropic_messages", "responses", "aresponses", "text_completion", "atext_completion"}
	var rows []map[string]any
	want := map[string]SessionAgentCoverage{}
	for _, callType := range callTypes {
		pair := sessionAttributionRows([]any{"private-agent", nil})
		for _, row := range pair {
			row["request_id"] = fmt.Sprint(len(rows))
			row["call_type"] = callType
			rows = append(rows, row)
		}
		want[callType] = SessionAgentCoverage{KnownRows: 1, MissingRows: 1}
	}
	for _, callType := range []any{"private-call-type-sentinel", " RESPONSES ", "embedding", nil} {
		row := sessionAttributionRows([]any{nil})[0]
		row["request_id"], row["call_type"] = fmt.Sprint(len(rows)), callType
		rows = append(rows, row)
	}
	want["unknown"] = SessionAgentCoverage{MissingRows: 4}
	rows[0]["session_id"] = "" // Report coverage still includes unattributed rows.
	rows[2]["status"], rows[2]["rejection_kind"] = "failure", "session_budget"
	outside := sessionAttributionRows([]any{nil})[0]
	outside["request_id"], outside["startTime"], outside["endTime"] = "outside-private", "2026-09-05T12:00:00Z", "2026-09-05T12:00:01Z"
	rows = append(rows, outside)
	opts := sessionOptions()
	opts.ShowHashes = true
	r, err := ReviewSessions(writeSessionAttributionRows(t, "json", rows), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.AgentAttributionByCallType, want) || *r.AgentAttribution != (SessionAgentCoverage{KnownRows: 8, MissingRows: 12}) || r.SelectedRows != 20 {
		t.Fatalf("report scope: %+v", r)
	}
	if *r.Largest[0].AgentAttribution != (SessionAgentCoverage{KnownRows: 7, MissingRows: 12}) || r.UnattributedRows != 1 || r.ModelCalls != 15 || r.UnsupportedRows != 4 {
		t.Fatalf("session scope or accounting: %+v", r)
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, sentinel := range []string{"private-", "sentinel-", "sensitive-key", " RESPONSES ", "embedding"} {
		if bytes.Contains(data, []byte(sentinel)) {
			t.Fatalf("private value leaked: %s", data)
		}
	}
}

func TestSessionAttributionTopAndCompleteScopes(t *testing.T) {
	rows := sessionAttributionRows([]any{"private-agent", nil, nil})
	rows[0]["session_id"], rows[0]["spend"], rows[0]["call_type"] = "complete", "2", "completion"
	rows[1]["session_id"], rows[1]["spend"] = "missing-one", "1"
	rows[2]["session_id"], rows[2]["spend"] = "missing-two", "0.5"
	opts := sessionOptions()
	opts.Top = 1
	r, err := ReviewSessions(writeSessionAttributionRows(t, "json", rows), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Largest) != 1 || r.Largest[0].AgentAttribution != nil || *r.AgentAttribution != (SessionAgentCoverage{KnownRows: 1, MissingRows: 2}) || r.AgentAttributionByCallType["completion"] != (SessionAgentCoverage{KnownRows: 1}) {
		t.Fatalf("top changed coverage or complete session gained fields: %+v", r)
	}
	// Missing attribution on rejected/unsupported rows is still visible even
	// when no analyzed session reaches the largest-session list.
	for _, callType := range []string{"aresponses", "private-unsupported"} {
		row := sessionAttributionRows([]any{nil})[0]
		row["call_type"] = callType
		if callType == "aresponses" {
			row["status"], row["rejection_kind"] = "failure", "session_budget"
		}
		r, err := ReviewSessions(writeSessionAttributionRows(t, "json", []map[string]any{row}), opts)
		if err != nil || r.ModelCalls != 0 || len(r.Largest) != 0 || r.AgentAttribution == nil || r.AgentAttribution.MissingRows != 1 {
			t.Fatalf("non-model-call coverage: %+v %v", r, err)
		}
	}
}
