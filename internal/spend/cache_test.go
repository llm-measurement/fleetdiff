// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func cacheRow(id, day, key, read, write string) map[string]string {
	r := sample(id, day, key, 1000, 100)
	r["cache_read_input_tokens"], r["cache_write_input_tokens"] = read, write
	return r
}

func TestCacheDropUsesCoveredInputAndExistingMappings(t *testing.T) {
	for _, op := range []string{"completion", "acompletion", "text_completion", "atext_completion", "anthropic_messages", "aanthropic_messages", "responses", "aresponses"} {
		t.Run(op, func(t *testing.T) {
			rows := []map[string]string{cacheRow("a", "2026-09-01", "sensitive-key", "920", "50"), cacheRow("b", "2026-09-02", "sensitive-key", "610", "100")}
			for _, row := range rows {
				row["call_type"] = op
			}
			r, err := Investigate(writeRows(t, rows), options(t))
			if err != nil {
				t.Fatal(err)
			}
			a, b := r.Before.Cache, r.After.Cache
			if !a.CompleteRead() || !b.CompleteRead() || *a.Read.Share != .92 || *b.Read.Share != .61 || *a.Write.Share != .05 || *b.Write.Share != .1 || *a.Uncached != 80 || *b.Uncached != 390 {
				t.Fatalf("cache: %+v -> %+v", a, b)
			}
			if r.Before.Tokens != 1100 || r.After.Tokens != 1100 {
				t.Fatal("cache counted twice")
			}
			m := r.Cache.Rankings.Movers[0]
			if m.Before.Lower != 80 || m.After.Upper != 390 || m.Delta.Lower != 310 || m.Item != r.Rankings.Movers[0].Item {
				t.Fatal(m)
			}
			data, _ := json.Marshal(r)
			if strings.Contains(string(data), "sensitive-key") {
				t.Fatal("identity leak")
			}
		})
	}
}

func TestCacheMissingInvalidAndZero(t *testing.T) {
	for _, tc := range []struct {
		name, read, write, input  string
		covered, missing, invalid uint64
	}{
		{"absent", "", "", "1000", 0, 1, 0},
		{"zero", "0", "0", "1000", 1, 0, 0},
		{"subset", "1001", "20", "1000", 0, 0, 1},
		{"invalid", "bad", "20", "1000", 0, 0, 1},
		{"negative", "-1", "20", "1000", 0, 0, 1},
		{"too large", "9223372036854775808", "20", "1000", 0, 0, 1},
		{"missing input", "20", "20", "", 0, 0, 1},
		{"empty input", "0", "0", "0", 1, 0, 0},
	} {
		for _, side := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/%d", tc.name, side), func(t *testing.T) {
				rows := []map[string]string{cacheRow("a", "2026-09-01", "key", "920", "50"), cacheRow("b", "2026-09-02", "key", "610", "100")}
				rows[side]["cache_read_input_tokens"], rows[side]["cache_write_input_tokens"], rows[side]["prompt_tokens"] = tc.read, tc.write, tc.input
				r, err := Investigate(writeRows(t, rows), options(t))
				if err != nil {
					t.Fatal(err)
				}
				w := r.Before.Cache
				if side == 1 {
					w = r.After.Cache
				}
				if *w.Read.Covered != tc.covered || *w.Read.Missing != tc.missing || *w.Read.Invalid != tc.invalid {
					t.Fatal(w.Read)
				}
				if tc.covered == 0 && (w.Read.Share != nil || w.Uncached != nil || r.Cache.Rankings != nil) {
					t.Fatal("unknown became zero")
				}
				if tc.input == "0" && w.Read.Share != nil {
					t.Fatal("zero denominator")
				}
				if tc.read == "0" && *w.Read.ZeroOriginUnknown != 1 {
					t.Fatal("recorded cache zero lost its unknown origin")
				}
				if tc.covered == 0 {
					b, _ := json.Marshal(w)
					if !strings.Contains(string(b), `"uncached_input_tokens":null`) {
						t.Fatal(string(b))
					}
				}
			})
		}
	}
}

func TestCachePartialCoverageDoesNotUseWholeDenominator(t *testing.T) {
	rows := []map[string]string{cacheRow("a", "2026-09-01", "key", "920", "50"), cacheRow("b", "2026-09-02", "key", "610", ""), cacheRow("c", "2026-09-02", "key", "", "100")}
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil {
		t.Fatal(err)
	}
	w := r.After.Cache
	if *w.Read.Share != .61 || *w.Write.Share != .1 || *w.Read.Missing != 1 || *w.Write.Missing != 1 || w.CompleteRead() || *w.Uncached != 390 {
		t.Fatal(w)
	}
	if r.Cache.Rankings.Movers[0].Delta.Lower != 310 {
		t.Fatal("missing rows entered ranking")
	}
}

func TestCacheRankingBoundsAndStableAliases(t *testing.T) {
	opts := options(t)
	opts.ShowHashes = true
	rows := []map[string]string{}
	for i := 0; i < 1500; i++ {
		rows = append(rows, cacheRow(fmt.Sprintf("b%d", i), "2026-09-01", fmt.Sprint(i), "900", "0"), cacheRow(fmt.Sprintf("a%d", i), "2026-09-02", fmt.Sprint(i), "500", "0"))
	}
	p := writeRows(t, rows)
	r, err := Investigate(p, opts)
	if err != nil {
		t.Fatal(err)
	}
	if r.Cache.Rankings.BeforeMaxError == 0 && r.Cache.Rankings.AfterMaxError == 0 {
		t.Fatal("expected approximate ranking")
	}
	for _, m := range r.Cache.Rankings.Movers {
		if m.Before.Lower > 100 || m.Before.Upper < 100 || m.After.Lower > 500 || m.After.Upper < 500 || m.Delta.Lower > 400 || m.Delta.Upper < 400 {
			t.Fatal(m)
		}
	}
	// Equal total weights but unequal cache losses: aliases still refer to the same keys.
	rows = []map[string]string{cacheRow("1", "2026-09-01", "a", "900", "0"), cacheRow("2", "2026-09-01", "b", "900", "0"), cacheRow("3", "2026-09-02", "a", "800", "0"), cacheRow("4", "2026-09-02", "b", "100", "0")}
	opts.SecretEnv = ""
	for i := 0; i < 5; i++ {
		r, err = Investigate(writeRows(t, rows), opts)
		if err != nil {
			t.Fatal(err)
		}
		if r.Cache.Rankings.Movers[0].Item != "key-2" || r.Cache.Rankings.Movers[0].Delta != (compare.Interval{Lower: 800, Upper: 800}) {
			t.Fatal(r.Cache.Rankings)
		}
	}
}

func TestCacheGroupsAndMissingIdentity(t *testing.T) {
	for _, group := range []string{"key", "team", "session", "user", "end-user"} {
		t.Run(group, func(t *testing.T) {
			rows := []map[string]string{cacheRow("a", "2026-09-01", "secret", "920", "50"), cacheRow("b", "2026-09-02", "secret", "610", "100")}
			for _, row := range rows {
				row["team_id"], row["user"], row["end_user"] = "private-team", "private-user", "private-enduser"
			}
			opts := options(t)
			opts.GroupBy = group
			r, err := Investigate(writeRows(t, rows), opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Cache.Rankings.Movers) != 1 || r.Cache.Rankings.Movers[0].Delta.Lower != 310 {
				t.Fatal(r.Cache)
			}
		})
	}
}

func TestCacheMissingIdentityExclusion(t *testing.T) {
	rows := []map[string]string{cacheRow("a", "2026-09-01", "key", "920", "50"), cacheRow("b", "2026-09-02", "", "610", "100")}
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil {
		t.Fatal(err)
	}
	if r.After.Cache.MissingIdentity != 1 || *r.After.Cache.Uncached != 390 || len(r.Cache.Rankings.Movers) != 0 {
		t.Fatal("identity-free usage must enter totals but not ranking", r.Cache)
	}
}

func TestCacheWriteInvalidAndAggregateOverflow(t *testing.T) {
	for _, value := range []string{"bad", "-1", "1001", "9223372036854775808"} {
		rows := []map[string]string{cacheRow("a", "2026-09-01", "key", "920", "50"), cacheRow("b", "2026-09-02", "key", "610", value)}
		r, err := Investigate(writeRows(t, rows), options(t))
		if err != nil {
			t.Fatal(err)
		}
		if *r.After.Cache.Write.Invalid != 1 || r.After.Cache.Write.Share != nil || !r.After.Cache.CompleteRead() {
			t.Fatal("invalid write must not become zero or hide usable read", r.After.Cache)
		}
	}
	rows := []map[string]string{sample("a", "2026-09-01", "key", math.MaxInt64, 0), sample("b", "2026-09-01", "key", 1, 0), sample("c", "2026-09-02", "key", 1, 0)}
	for _, row := range rows {
		row["cache_read_input_tokens"], row["cache_write_input_tokens"] = "0", "0"
	}
	if _, err := Investigate(writeRows(t, rows), options(t)); err == nil {
		t.Fatal("cache aggregate overflow must fail")
	}
}

func TestUncachedGrowthCanAccompanyImprovedShare(t *testing.T) {
	rows := []map[string]string{cacheRow("a", "2026-09-01", "key", "900", "0"), cacheRow("b", "2026-09-02", "key", "2850", "0")}
	rows[1]["prompt_tokens"], rows[1]["total_tokens"] = "3000", "3100"
	r, err := Investigate(writeRows(t, rows), options(t))
	if err != nil {
		t.Fatal(err)
	}
	if *r.After.Cache.Read.Share <= *r.Before.Cache.Read.Share || r.Cache.Rankings.Movers[0].Delta.Lower != 50 {
		t.Fatal(r.Cache)
	}
}
