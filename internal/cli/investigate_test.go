// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

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
