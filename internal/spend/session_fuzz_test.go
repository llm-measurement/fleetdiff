// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzSessionBudget(f *testing.F) {
	f.Add("0.1", "1.00", "")
	f.Add("NaN", "0.01", "session_budget")
	f.Add("1e-324", "1e-323", "session_iterations")
	f.Add("1e999", "0", "unrecognized")
	f.Fuzz(func(t *testing.T, cost, cap, kind string) {
		if len(cost) > 128 || len(cap) > 128 || len(kind) > 128 {
			t.Skip()
		}
		row := sessionRow("fuzz-request", "sentinel-session-fuzz", "2026-09-02", cost)
		row["rejection_kind"] = kind
		row["status"] = "failure"
		opts := sessionOptions()
		opts.BudgetUSD = cap
		opts.ShowHashes = true
		r, err := ReviewSessions(writeRows(t, []map[string]string{row}), opts)
		if err != nil {
			if strings.Contains(err.Error(), "sentinel-session-fuzz") || strings.Contains(err.Error(), "sentinel-agent-private") {
				t.Fatal("error leaked identity")
			}
			return
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "sentinel-session-fuzz") || strings.Contains(string(b), "sentinel-agent-private") {
			t.Fatal("report leaked identity")
		}
		if r.Spend.N > r.Sessions || r.Exceeding > r.Spend.N || r.Singletons > r.Sessions || r.Calls.N != r.Sessions {
			t.Fatal("population invariant")
		}
	})
}
