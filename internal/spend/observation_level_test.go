// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
)

func TestObservationLevelPreservesAccountingFlags(t *testing.T) {
	for _, level := range []string{"DEFAULT", "DEBUG", "WARNING", "ERROR", "unknown", "PRIVATE_LEVEL"} {
		for _, mode := range []string{"clean", "missing", "invalid", "mismatch", "zero", "failure"} {
			t.Run(level+"/"+mode, func(t *testing.T) {
				row := Row{Values: sample("one", "2026-09-01", "key", 19, 3), ObservationLevel: level}
				delete(row.Values, "status")
				switch mode {
				case "missing":
					delete(row.Values, "completion_tokens")
				case "invalid":
					row.Values["prompt_tokens"] = "-1"
				case "mismatch":
					row.Values["total_tokens"] = "99"
				case "zero":
					row.Values["prompt_tokens"], row.Values["completion_tokens"], row.Values["total_tokens"] = "0", "0", "0"
				case "failure":
					row.Values["status"] = "failure"
				}
				obs, err := accounting.Tokens(attributes(row))
				if err != nil {
					t.Fatal(err)
				}
				q := classify(row, obs)
				wantReview := uint64(1)
				if mode == "clean" && (level == "DEFAULT" || level == "DEBUG" || level == "WARNING") {
					wantReview = 0
				}
				if q.Unclear != wantReview || mode != "failure" && q.UnknownStatus != 1 || mode == "failure" && q.Failed != 1 {
					t.Fatal("severity cleared accounting/status flag or implied success")
				}
				b, _ := json.Marshal(q)
				if strings.Contains(string(b), "PRIVATE_") {
					t.Fatal("unrecognized severity leaked")
				}
			})
		}
	}
}

func TestObservationLevelIsBoundToEveryReplay(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, changeOnPass := range []int{2, 3} {
			opts := options(t)
			if automatic {
				opts.BeforePeriod, opts.AfterPeriod = "", ""
			}
			pass := 0
			_, err := InvestigateRows(func(visit func(Row) error) error {
				pass++
				level := "DEFAULT"
				if pass >= changeOnPass {
					level = "ERROR"
				}
				for _, day := range []string{"2026-09-01", "2026-09-02", "2026-09-15", "2026-09-22"} {
					row := Row{Values: sample(day, day, "key", 19, 3), ObservationLevel: level}
					delete(row.Values, "status")
					if e := visit(row); e != nil {
						return e
					}
				}
				return nil
			}, opts)
			// Explicit periods need two passes; automatic periods add a date pass.
			if changeOnPass == 3 && !automatic {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatal("changed severity escaped the completed-snapshot binding", err)
			}
		}
	}
}

func TestSQLCannotInjectObservationSeverity(t *testing.T) {
	row := sample("one", "2026-09-01", "key", 19, 3)
	delete(row, "status")
	row["level"], row["ObservationLevel"], row["observation_level"] = "DEFAULT", "DEFAULT", "DEFAULT"
	r, err := Investigate(writeRows(t, []map[string]string{row}), options(t))
	if err != nil || r.Before.Quality.UnknownStatus != 1 || r.Before.Quality.Unclear != 1 || r.Before.Quality.ObservationLevels != nil {
		t.Fatal("SQL projection acquired adapter severity semantics", err)
	}
}
