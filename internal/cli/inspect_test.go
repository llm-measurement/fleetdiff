// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/inspect"
)

func TestInspectTextJSONAndStdin(t *testing.T) {
	data, err := os.ReadFile("../../testdata/inspect-contract/v1/litellm-normal-no-usage-stock.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		var out, errout bytes.Buffer
		code := RunWithInput([]string{"inspect", "--format", format, "-"}, bytes.NewReader(data), &out, &errout)
		if code != 0 || errout.Len() != 0 {
			t.Fatalf("code=%d err=%s", code, &errout)
		}
		if strings.Contains(out.String(), "INSPECT_CONTRACT_PRIVATE") {
			t.Fatal("sentinel leaked")
		}
		if format == "json" {
			var r map[string]any
			if json.Unmarshal(out.Bytes(), &r) != nil || r["schema"] != "fleetdiff-inspect/v1" {
				t.Fatal("bad JSON")
			}
		} else if !strings.HasPrefix(out.String(), "Your traces can fully answer ") || !strings.Contains(out.String(), "a real zero and a filled-in zero cannot be distinguished") {
			t.Fatal("missing headline or origin limit")
		}
	}
}

func TestInspectPrivateErrorsAndNoPartialOutput(t *testing.T) {
	data, _ := os.ReadFile("../../testdata/inspect-contract/v1/operation-scope.json")
	for _, tc := range []struct {
		args []string
		data []byte
		code int
	}{
		{[]string{"inspect", "--format", "SENTINEL", "-"}, nil, 2},
		{[]string{"inspect", "--SENTINEL"}, nil, 2},
		{[]string{"inspect", "SENTINEL_path"}, nil, 1},
		{[]string{"inspect", "--top", "0", "-"}, nil, 2},
		{[]string{"inspect", "--secret-env", "SENTINEL_missing_env", "-"}, data, 1},
		{[]string{"inspect", "-"}, append(data, []byte(`{"SENTINEL":`)...), 1},
	} {
		var out, errout bytes.Buffer
		code := RunWithInput(tc.args, bytes.NewReader(tc.data), &out, &errout)
		if code != tc.code || out.Len() != 0 || strings.Contains(errout.String(), "SENTINEL") {
			t.Fatalf("code=%d output=%s err=%s", code, &out, &errout)
		}
	}
}

func TestInspectHelp(t *testing.T) {
	var out, errout bytes.Buffer
	if RunWithInput([]string{"inspect", "--help"}, nil, &out, &errout) != 0 || !strings.Contains(out.String(), "file-proto") {
		t.Fatal("inspect help missing")
	}
}

func TestInspectNameDisclosureAndTerminalEscaping(t *testing.T) {
	name := "acme.customer_email\n\x1b[2J\u202e"
	key, _ := json.Marshal(name)
	data := []byte(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"name":"SENTINEL_span","attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"chat"}},{"key":` + string(key) + `,"value":{"stringValue":"SENTINEL_value"}},{"key":"session.id","value":{"stringValue":"SENTINEL_session"}}]}]}]}]}`)
	for _, format := range []string{"text", "json"} {
		for _, showNames := range []bool{false, true} {
			for _, showHashes := range []bool{false, true} {
				args := []string{"inspect", "--format", format}
				if showNames {
					args = append(args, "--show-names")
				}
				if showHashes {
					args = append(args, "--show-hashes")
				}
				var out, errout bytes.Buffer
				if code := RunWithInput(append(args, "-"), bytes.NewReader(data), &out, &errout); code != 0 || errout.Len() != 0 {
					t.Fatal(code, errout.String())
				}
				text := out.String()
				if strings.Contains(text, "SENTINEL") || (format == "text" && strings.ContainsAny(text, "\x1b\u202e")) || strings.Contains(text, "acme.customer_email") != showNames {
					t.Fatal("name visibility or terminal escaping failed")
				}
				if format == "text" && showNames && !strings.Contains(text, strconv.QuoteToASCII(name)) {
					t.Fatal("custom name was not visibly escaped")
				}
				if format == "json" {
					var r inspect.Report
					if err := json.Unmarshal(out.Bytes(), &r); err != nil {
						t.Fatal(err)
					}
					found := false
					for _, d := range r.Dimensions {
						found = found || d.Attribute == name
					}
					if found != showNames || r.Readiness.Questions[0].ID != "model_activity" {
						t.Fatal("JSON name or stable question ID changed")
					}
				}
			}
		}
	}
}

func TestInspectReadableQuestionsAndBounds(t *testing.T) {
	r := inspect.Report{
		Readiness: inspect.Readiness{Questions: []inspect.Question{{ID: "model_activity", Status: "ready"}, {ID: "usage_origin", Status: "cannot_determine"}}},
		Identities: []inspect.Identity{{Field: "session", Present: 1, Rankings: []inspect.Ranking{{Weight: "attempts", Items: []inspect.Item{
			{Alias: "session-1", Lower: 8, Upper: 8, LowerShare: .2, UpperShare: .2},
			{Alias: "session-2", Lower: 7, Upper: 10, LowerShare: .175, UpperShare: .25},
		}}}}},
	}
	var out bytes.Buffer
	renderInspection(&out, r, 10)
	for _, want := range []string{"Model activity: ready", "Usage origin: cannot determine", "session-1: 8 (20.00%)", "session-2: [7, 10] ([17.50%, 25.00%])"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing readable output:", want)
		}
	}
}

func TestReleaseBinaryInspect(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test a packaged executable")
	}
	for _, names := range []bool{false, true} {
		args := []string{"inspect", "--format", "json", "--input-format", "json"}
		if names {
			args = append(args, "--show-names")
		}
		cmd := exec.Command(binary, append(args, "-")...)
		cmd.Stdin = strings.NewReader(`{"resourceSpans":[{"scopeSpans":[{"spans":[{"attributes":[{"key":"gen_ai.operation.name","value":{"stringValue":"chat"}},{"key":"acme.customer_email","value":{"stringValue":"SENTINEL_value"}},{"key":"gen_ai.usage.input_tokens","value":{"intValue":"8"}},{"key":"gen_ai.usage.output_tokens","value":{"intValue":"0"}}]}]}]}]}`)
		data, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(data))
		}
		var r inspect.Report
		if err := json.Unmarshal(data, &r); err != nil || r.Schema != "fleetdiff-inspect/v1" || r.ObservedCounters["gen_ai_sketch_total_tokens_total"] != 8 {
			t.Fatal("packaged inspection failed", err)
		}
		if bytes.Contains(data, []byte("SENTINEL")) || bytes.Contains(data, []byte("acme.customer_email")) != names || len(r.Dimensions) != 2 {
			t.Fatal("packaged privacy or label policy failed")
		}
	}
}
