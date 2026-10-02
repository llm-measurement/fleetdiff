// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestSessionsDemo(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		var out, diagnostic bytes.Buffer
		args := []string{"investigate", "--before", "../../examples/sessions/data/before",
			"--after", "../../examples/sessions/data/after", "--expected", "app", "--format", format}
		if code := Run(args, &out, &diagnostic); code != 0 {
			t.Fatal(code, diagnostic.String())
		}
		for _, hidden := range []string{"SESSIONS_PRIVATE_", `"hash"`, "topk_contract"} {
			if strings.Contains(out.String()+diagnostic.String(), hidden) {
				t.Fatal("private fixture content in report")
			}
		}
		if format == "text" {
			if !strings.HasPrefix(out.String(), "1 of 8 tracked sessions flagged: 90.91% of attributed tokens.\n") {
				t.Fatal("session result must lead the report", out.String())
			}
			for _, want := range []string{"Reported tokens: 400 -> 3300", "Model attempts: 4 -> 13",
				"+1592.31 tokens", "+1307.69 tokens", "[90.91%, 90.91%]; runaway candidate",
				"Were these counts reported by the provider? [cannot_determine]"} {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("missing %q in report", want)
				}
			}
			continue
		}
		var report compare.Investigation
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if !report.Evidence.Complete {
			t.Fatal("fixture coverage is incomplete")
		}
		seen := map[string]bool{}
		for _, q := range report.Questions {
			seen[q.ID] = true
			switch q.ID {
			case "volume":
				v := q.Volume
				if q.Status != "observed" || v == nil || v.BeforeTokens != 400 || v.AfterTokens != 3300 || v.BeforeRequests != 4 || v.AfterRequests != 13 ||
					math.Abs(v.RequestContribution-20700.0/13) > 1e-9 || math.Abs(v.TokensPerRequestContribution-17000.0/13) > 1e-9 {
					t.Fatal("incorrect volume split", v)
				}
			case "sessions", "users":
				leaders, flags := 0, 0
				for _, c := range q.Contributors {
					if c.Flag != "" {
						flags++
						if c.Flag != "runaway_candidate" || c.After == nil || c.After.Lower != 3000 {
							t.Fatal("unexpected candidate flag", c)
						}
					}
					if c.After != nil && c.After.Lower == 3000 {
						leaders++
						if c.Before == nil || c.Before.Lower != 0 || c.Before.Upper != 0 || c.After.Upper != 3000 ||
							c.AfterShare == nil || math.Abs(c.AfterShare.Lower-10.0/11) > 1e-12 || c.AfterShare.Upper != c.AfterShare.Lower {
							t.Fatal("incorrect leading contributor bounds", c)
						}
					}
				}
				wantFlags := 0
				if q.ID == "sessions" {
					wantFlags = 1
				}
				if q.Status != "observed" || leaders != 1 || flags != wantFlags {
					t.Fatal("incorrect attribution", q)
				}
			case "coverage":
				if q.Status != "observed" {
					t.Fatal(q)
				}
			case "usage_source":
				if q.Status != "cannot_determine" {
					t.Fatal("invented provider origin", q)
				}
			}
		}
		for _, id := range []string{"volume", "sessions", "users", "coverage", "usage_source"} {
			if !seen[id] {
				t.Fatal("missing question", id)
			}
		}
	}
}

func TestSingleAppAndTwoStackInvestigation(t *testing.T) {
	base := "../../examples/single-app/data/"
	var single compare.Investigation
	for _, mode := range []string{"single", "two-stacks", "missing-usage"} {
		before, after, expected := base+mode+"/before", base+mode+"/after", "app"
		if mode == "two-stacks" {
			expected = "gateway, direct"
		}
		if mode == "missing-usage" {
			before = base + "single/before"
		}
		for _, format := range []string{"json", "text"} {
			var out, diagnostic bytes.Buffer
			args := []string{"investigate", "--before", before, "--after", after, "--expected", expected, "--format", format}
			if code := Run(args, &out, &diagnostic); code != 0 {
				t.Fatal(mode, diagnostic.String())
			}
			if strings.Contains(out.String(), "PRIVATE_LITELLM") || strings.Contains(out.String(), `"hash"`) {
				t.Fatal("private fixture content in report")
			}
			if format == "text" {
				if !strings.Contains(out.String(), "What changed in my agent app?") || !strings.Contains(out.String(), "cannot_determine") {
					t.Fatal(out.String())
				}
				if mode == "missing-usage" {
					if !strings.HasPrefix(out.String(), "Token-change breakdown unavailable.") {
						t.Fatal("missing usage must be visible in the headline")
					}
				} else if !strings.HasPrefix(out.String(), "Reported tokens: 200 -> 600 (+400); model attempts: 2 -> 3.") {
					t.Fatal("volume result must lead the report")
				}
				continue
			}
			var report compare.Investigation
			if err := json.Unmarshal(out.Bytes(), &report); err != nil {
				t.Fatal(err)
			}
			v := report.Questions[0].Volume
			if mode == "missing-usage" {
				if v != nil || report.Questions[0].Status != "cannot_determine" || report.Evidence.After.Usage.Missing != 1 {
					t.Fatal(report)
				}
			} else if v == nil || v.BeforeTokens != 200 || v.AfterTokens != 600 || v.RequestContribution != 150 || v.TokensPerRequestContribution != 250 {
				t.Fatal(v)
			}
			if mode == "single" {
				single = report
			}
			if mode == "two-stacks" && (!reflect.DeepEqual(report.Questions, single.Questions) || !reflect.DeepEqual(report.Evidence.Counters, single.Evidence.Counters) || !reflect.DeepEqual(report.Evidence.Concentration, single.Evidence.Concentration)) {
				t.Fatal("partition changed answers")
			}
		}
	}
}

func TestInvestigationErrorsRemainPrivate(t *testing.T) {
	a, b := inputs(t)
	args := []string{"investigate", "--before", a, "--after", b, "--expected", "operator"}
	var out, diagnostic bytes.Buffer
	if code := Run(args, failedWriter{}, &diagnostic); code != 1 || strings.Contains(diagnostic.String(), "SENTINEL") {
		t.Fatal(diagnostic.String())
	}
	args[4] = a
	diagnostic.Reset()
	if code := Run(args, &out, &diagnostic); code != 1 || out.Len() != 0 {
		t.Fatal("overlap accepted")
	}
}
