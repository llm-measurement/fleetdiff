// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func whatIfArgs() []string {
	return []string{"what-if", "--litellm-spend", "../../examples/what-if/spend.csv", "--period", "2026-10-07", "--scenario", "../../examples/what-if/scenario.json"}
}

func TestWhatIfCheckedExample(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var out, errs bytes.Buffer
			args := append(whatIfArgs(), "--format", format)
			if n := Run(args, &out, &errs); n != 0 {
				t.Fatalf("exit %d: %s", n, &errs)
			}
			want, err := os.ReadFile("../../examples/what-if/expected." + format)
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != string(want) {
				t.Fatalf("checked example changed:\n%s", &out)
			}
			if format == "text" {
				doc, err := os.ReadFile("../../docs/WHAT_IF.md")
				if err != nil {
					t.Fatal(err)
				}
				first, _, _ := strings.Cut(out.String(), "\n")
				if !strings.Contains(string(doc), first) {
					t.Fatal("guide excerpt differs from real output")
				}
			}
		})
	}
}

func TestWhatIfMappingsAndPrivacy(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		var out, errs bytes.Buffer
		args := append(whatIfArgs(), "--format", format, "--workload-map", "../../examples/what-if/workload-map.json")
		if n := Run(args, &out, &errs); n != 0 {
			t.Fatalf("exit %d: %s", n, &errs)
		}
		if strings.Contains(out.String()+errs.String(), "SENTINEL_") {
			t.Fatal("raw identity escaped")
		}
		if format == "json" {
			var r reprice.Report
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if len(r.Level2) != 1 || r.Level2[0].Workload != "mapped" || r.Unmapped != 0 {
				t.Fatalf("mapping not applied: %+v", r)
			}
		}
	}
}

func TestWhatIfErrorSentinels(t *testing.T) {
	for _, args := range [][]string{
		{"what-if", "--litellm-spend", "SENTINEL_PATH", "--period", "2026-10-07", "--from", "SENTINEL_MODEL", "--to", "gpt-6.1-sol", "--fraction", "0.5"},
		{"what-if", "--scenario", "SENTINEL_PATH"},
		{"what-if", "--SENTINEL_FLAG"},
		{"what-if", "--litellm-spend", "SENTINEL_PATH", "--period", "SENTINEL_DATE", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5"},
	} {
		var out, errs bytes.Buffer
		if Run(args, &out, &errs) == 0 {
			t.Fatal("invalid input accepted")
		}
		if strings.Contains(out.String()+errs.String(), "SENTINEL_") {
			t.Fatalf("error leak: %s %s", &out, &errs)
		}
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"moves":[{"from":"claude-sonnet-5-5","to":"gpt-6.1-sol","fraction":"SENTINEL_FRACTION"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	args := whatIfArgs()
	args[len(args)-1] = path
	var out, errs bytes.Buffer
	if Run(args, &out, &errs) == 0 || strings.Contains(errs.String(), "SENTINEL") {
		t.Fatalf("config error: %s", &errs)
	}
}

func TestWhatIfUnknownSummarySplit(t *testing.T) {
	args := []string{"what-if", "--summaries", "../../examples/sessions/data/before", "--expected", "app", "--period", "1970-01-01", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5", "--format", "json"}
	var out, errs bytes.Buffer
	if n := Run(args, &out, &errs); n != 0 {
		t.Fatalf("exit %d: %s", n, &errs)
	}
	var r reprice.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.Before != nil || len(r.Source.Gaps) == 0 {
		t.Fatal("summary model split fabricated")
	}
}

func TestWhatIfHelp(t *testing.T) {
	var out, errs bytes.Buffer
	if Run([]string{"what-if", "--help"}, &out, &errs) != 0 || !strings.Contains(out.String(), "--prices") {
		t.Fatal("missing what-if help")
	}
}

func TestWhatIfPriceOverrideAndUnmatchedScope(t *testing.T) {
	p, _, err := reprice.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	p.Date = "2026-10-15"
	for i := range p.Models {
		p.Models[i].Input, p.Models[i].Output, p.Models[i].CacheRead, p.Models[i].CacheWrite = "0", "0", "0", "0"
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	args := append(whatIfArgs(), "--prices", path, "--format", "json")
	if code := Run(args, &out, &errs); code != 0 {
		t.Fatalf("%d: %s", code, &errs)
	}
	var r reprice.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.PriceSource != "override" || r.PriceDate != p.Date || *r.Before != "0.000000000" || *r.Level1 != "0.000000000" {
		t.Fatalf("override: %+v", r)
	}
	out.Reset()
	errs.Reset()
	args = []string{"what-if", "--litellm-spend", "../../examples/what-if/spend.csv", "--period", "2026-10-07", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5", "--scope-key", "SENTINEL_UNMATCHED"}
	if code := Run(args, &out, &errs); code != 0 {
		t.Fatalf("%d: %s", code, &errs)
	}
	if !strings.HasPrefix(out.String(), "No covered traffic matches") || strings.Contains(out.String()+errs.String(), "SENTINEL") {
		t.Fatalf("unmatched: %s %s", &out, &errs)
	}
}
