// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func topKInputs(t *testing.T, name string, includeBefore bool, afterWeights ...int64) (string, string) {
	t.Helper()
	a, b := inputs(t)
	for i, path := range []string{a, b} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		e, err := summary.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		e.Counters["requests"] = 10
		e.Counters["missing_token_usage"] = 4
		e.Counters["input_tokens"] = 600
		e.Counters["output_tokens"] = 0
		if i != 0 || includeBefore {
			domain := sketchhash.SessionV1
			if strings.HasPrefix(name, "top_users") {
				domain = sketchhash.UserV1
			}
			f, err := frequentitems.New("micro", domain, sketchhash.HMACSHA25664)
			if err != nil {
				t.Fatal(err)
			}
			weights := []int64{1, 9}
			if i == 1 {
				weights = []int64{8, 2}
				if len(afterWeights) != 0 {
					weights = afterWeights
				}
			}
			for key, weight := range weights {
				if err := f.AddHash(uint64(key+1), weight); err != nil {
					t.Fatal(err)
				}
			}
			payload, err := f.MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
			e.Sketches[name] = summary.Payload{Kind: "frequent_items", Data: payload}
			e.Counters["topk_contract.v1."+name+".0123456789abcdef0123456789abcdef"] = 0
		}
		data, err = e.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return a, b
}

func TestReleaseBinaryTopK(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test an unpacked release archive")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sketch, question, threshold, status string
		includeBefore, flagged                    bool
	}{
		{"request sessions", "top_sessions_requests", "sessions", "0.25", "observed", true, true},
		{"strict threshold", "top_sessions_requests", "sessions", "0.8", "observed", true, false},
		{"older window", "top_sessions_requests", "sessions", "0.25", "cannot_determine", false, false},
		{"missing token usage", "top_sessions", "sessions", "0.25", "limited", true, false},
		{"request users", "top_users_requests", "users", "0.25", "observed", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := topKInputs(t, tc.sketch, tc.includeBefore)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "investigate", "--before", a, "--after", b,
				"--expected", "operator", "--format", "json", "--flag-share", tc.threshold)
			var diagnostic bytes.Buffer
			cmd.Stderr = &diagnostic
			output, err := cmd.Output()
			if err != nil {
				t.Fatal(err, diagnostic.String())
			}
			for _, private := range []string{"SENTINEL", "topk_contract", `"hash"`} {
				if strings.Contains(string(output)+diagnostic.String(), private) {
					t.Fatal("private metadata leaked by release binary")
				}
			}
			var report compare.Investigation
			if err := json.Unmarshal(output, &report); err != nil {
				t.Fatal(err)
			}
			if report.Evidence.After.Usage.Missing != 4 {
				t.Fatal("release binary changed missing-usage accounting")
			}
			for _, question := range report.Questions {
				if question.ID != tc.question {
					continue
				}
				flagged := false
				for _, contributor := range question.Contributors {
					flagged = flagged || contributor.Flag == "runaway_candidate"
				}
				if question.Status != tc.status || flagged != tc.flagged {
					t.Fatalf("unexpected release answer: %+v", question)
				}
				return
			}
			t.Fatal("release binary omitted question", tc.question)
		})
	}
}

func TestTopKRequestInvestigationCLI(t *testing.T) {
	a, b := topKInputs(t, "top_sessions_requests", true)
	for _, tc := range []struct {
		threshold, format string
		flagged           bool
	}{
		{"", "json", true}, {"0", "json", true}, {"0.8", "json", false}, {"1", "json", false},
		{"0.79", "json", true}, {"2.5e-1", "json", true}, {"", "text", true},
	} {
		t.Run(tc.threshold+"/"+tc.format, func(t *testing.T) {
			args := []string{"investigate", "--before", a, "--after", b, "--expected", "operator", "--format", tc.format}
			if tc.threshold != "" {
				args = append(args, "--flag-share", tc.threshold)
			}
			var out, diagnostic bytes.Buffer
			if code := Run(args, &out, &diagnostic); code != 0 {
				t.Fatal(code, diagnostic.String())
			}
			if strings.Contains(out.String(), "SENTINEL") || strings.Contains(out.String(), "topk_contract") || strings.Contains(out.String(), `"hash"`) {
				t.Fatal("private metadata leaked")
			}
			if tc.format == "text" {
				for _, want := range []string{"Which sessions need investigation?", "flagged for review", "1 -> 8", "top_sessions_requests", "Coverage (before -> after):"} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing %q: %s", want, out.String())
					}
				}
				if strings.Contains(out.String(), "evidence.before") || strings.Contains(out.String(), "evidence.after") {
					t.Fatal("JSON field path in text coverage hint")
				}
				return
			}
			var r compare.Investigation
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			found, flagged := false, false
			for _, q := range r.Questions {
				if q.ID == "sessions" {
					found = true
					if q.Status != "observed" || !strings.Contains(q.Answer, "model attempts") {
						t.Fatal(q)
					}
					for _, want := range []string{"attributed tokens or model attempts", "excluding activity without a key", "Per-key missing-ID coverage is unknown", "lower-bound shares", "complete relevant observations", "investigate the flagged sessions"} {
						if !strings.Contains(q.Answer, want) {
							t.Fatalf("missing qualification %q: %s", want, q.Answer)
						}
					}
					for _, c := range q.Contributors {
						flagged = flagged || c.Flag == "runaway_candidate"
					}
				}
			}
			if !found || flagged != tc.flagged || r.Evidence.After.Usage.Missing != 4 {
				t.Fatal(r)
			}
		})
	}
}

func TestSessionFlagDecimalThresholdCLI(t *testing.T) {
	a, b := topKInputs(t, "top_sessions_requests", true, 3, 7)
	for _, tc := range []struct {
		threshold string
		flagged   bool
	}{
		{".3", false},
		{"0.30", false},
		{"3e-1", false},
		{"0.29999999999999993", true},
		{"0.30000000000000004", false},
	} {
		t.Run(tc.threshold, func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			args := []string{"investigate", "--before", a, "--after", b, "--expected", "operator", "--format", "json", "--show-hashes", "--flag-share", tc.threshold}
			if code := Run(args, &out, &diagnostic); code != 0 {
				t.Fatal(code, diagnostic.String())
			}
			var r compare.Investigation
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			for _, q := range r.Questions {
				if q.ID != "sessions" {
					continue
				}
				if q.Status != "observed" || r.Evidence.After.Usage.Missing != 4 {
					t.Fatal("request attribution lost observed status", q)
				}
				for _, c := range q.Contributors {
					if c.Hash == "0000000000000001" {
						if c.After.Lower != 3 || (c.Flag == "runaway_candidate") != tc.flagged {
							t.Fatalf("flag crossed decimal threshold: %+v", c)
						}
						return
					}
				}
			}
			t.Fatal("missing boundary candidate")
		})
	}
}

func TestTopKCLIUpgradeAndUnknownNames(t *testing.T) {
	for _, name := range []string{"top_sessions_requests", "top_key.PRIVATE_SENTINEL.requests", "top_key.PRIVATE_SENTINEL_requests.tokens"} {
		for _, oldBefore := range []bool{false, true} {
			a, b := topKInputs(t, name, !oldBefore)
			args := []string{"investigate", "--before", a, "--after", b, "--expected", "operator", "--format", "json"}
			var out, diagnostic bytes.Buffer
			code := Run(args, &out, &diagnostic)
			if strings.Contains(out.String()+diagnostic.String(), "SENTINEL") {
				t.Fatal("raw optional attribute leaked")
			}
			if name != "top_sessions_requests" && oldBefore {
				if code != 1 || out.Len() != 0 {
					t.Fatal("unknown extension projected away")
				}
				continue
			}
			if code != 0 {
				t.Fatal(diagnostic.String())
			}
			var r compare.Investigation
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			for _, q := range r.Questions {
				if q.ID == "sessions" && (oldBefore || name != "top_sessions_requests") && q.Status != "cannot_determine" {
					t.Fatal(q)
				}
			}
		}
	}
}

func TestFlagShareCLIValidationIsPrivate(t *testing.T) {
	a, b := inputs(t)
	for _, value := range []string{"NaN", "Inf", "+Inf", "-Inf", "-0.1", "1.01", "0x1p-2", "1e9999", "PRIVATE_SENTINEL"} {
		for _, command := range []string{"investigate", "compare"} {
			var out, diagnostic bytes.Buffer
			code := Run([]string{command, "--before", a, "--after", b, "--expected", "operator", "--flag-share", value}, &out, &diagnostic)
			if code != 2 || out.Len() != 0 || !strings.Contains(diagnostic.String(), "flag-share") || strings.Contains(diagnostic.String(), "SENTINEL") {
				t.Fatal(code, out.String(), diagnostic.String())
			}
		}
	}
}

func TestMissingRequiredFlagsUseFixedNames(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"investigate"}, "missing required flag --before"},
		{[]string{"investigate", "--before", "PRIVATE_SENTINEL"}, "missing required flag --after"},
		{[]string{"compare", "--before", "PRIVATE_SENTINEL", "--after", "PRIVATE_SENTINEL"}, "missing required flag --expected"},
		{[]string{"investigate", "--before"}, "missing value for --before"},
		{[]string{"investigate", "--after"}, "missing value for --after"},
		{[]string{"investigate", "--expected"}, "missing value for --expected"},
		{[]string{"investigate", "--flag-share"}, "missing value for --flag-share"},
		{[]string{"investigate", "--PRIVATE_SENTINEL"}, "invalid compare options"},
	} {
		var out, diagnostic bytes.Buffer
		if code := Run(tc.args, &out, &diagnostic); code != 2 || out.Len() != 0 || !strings.Contains(diagnostic.String(), tc.want) || strings.Contains(diagnostic.String(), "SENTINEL") {
			t.Fatal(code, diagnostic.String())
		}
	}
	var out, diagnostic bytes.Buffer
	if Run([]string{"--help"}, &out, &diagnostic) != 0 || !strings.Contains(out.String(), "producer_id values configured by your summary collectors") || !strings.Contains(out.String(), "--flag-share DECIMAL") {
		t.Fatal(out.String())
	}
}
