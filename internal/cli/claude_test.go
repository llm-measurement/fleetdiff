// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func claudeCapture(t *testing.T) string {
	t.Helper()
	attr := func(key string, v any) any {
		return map[string]any{"key": key, "value": map[string]any{"stringValue": v}}
	}
	var logs []any
	for _, day := range []int{11, 12} {
		logs = append(logs, map[string]any{"timeUnixNano": strconv.FormatInt(time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC).UnixNano(), 10), "body": map[string]any{"stringValue": "claude_code.api_request"}, "attributes": []any{
			attr("input_tokens", "7"), attr("cache_read_tokens", "11"), attr("cache_creation_tokens", "5"), attr("output_tokens", "4"), attr("cost_usd", "0.00003435"), attr("cost_usd_micros", "34"), attr("user.id", "PRIVATE_INSTALL_ID"), attr("session.id", "PRIVATE_SESSION_ID"), attr("prompt.id", "PRIVATE_PROMPT_ID"), attr("model", "PRIVATE_MODEL"), attr("error", "SECRET_ERROR /Users/private/code.py def private_function():"), attr("unknown", "SECRET_UNKNOWN"),
		}})
	}
	b, e := json.Marshal(map[string]any{"resourceLogs": []any{map[string]any{"resource": map[string]any{"attributes": []any{attr("service.version", "2.1.296"), attr("fleetdiff.capture.profile", "claude-code-logs/v1"), attr("process.command_line", "SECRET_COMMAND")}}, "scopeLogs": []any{map[string]any{"scope": map[string]any{"name": "SECRET_SCOPE", "attributes": []any{attr("code", "SECRET_CODE")}}, "logRecords": logs}}}}})
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(t.TempDir(), "SECRET_FILENAME.jsonl")
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestClaudeCLIPrivacyAndObservationUnit(t *testing.T) {
	p := claudeCapture(t)
	for _, format := range []string{"text", "json"} {
		var out, err bytes.Buffer
		code := Run([]string{"investigate", "--claude-code-logs", p, "--before-period", "2026-10-11", "--after-period", "2026-10-12", "--format", format}, &out, &err)
		if code != 0 {
			t.Fatalf("%d: %s", code, &err)
		}
		for _, s := range []string{"PRIVATE_", "SECRET_", "/Users/private", "private_function", "logical API", "success", "\"requests\""} {
			if strings.Contains(out.String()+err.String(), s) {
				t.Fatalf("unsafe label or leaked input %q: %s", s, &out)
			}
		}
		if !strings.Contains(out.String(), "observed api_request records") || !strings.Contains(out.String(), "unknown") {
			t.Fatal(out.String())
		}
		if format == "text" && !strings.HasPrefix(out.String(), "Observed api_request token totals: 27 -> 27") {
			t.Fatal(out.String())
		}
	}
}
func TestClaudeCLIRejectsMixedModesAtomically(t *testing.T) {
	p := claudeCapture(t)
	for _, extra := range [][]string{{"--litellm-spend", "SECRET_FILE"}, {"--expected", "SECRET_PRODUCER"}, {"--before", "SECRET_PATH"}, {"--period", "2026-10-11"}, {"--session-budget-usd", "1"}, {"--group-by", "key"}, {"--format", "SECRET_FORMAT"}, {"--hash-secret-env", "SECRET_ENV"}} {
		args := []string{"investigate", "--claude-code-logs", p, "--before-period", "2026-10-11", "--after-period", "2026-10-12"}
		args = append(args, extra...)
		var out, err bytes.Buffer
		if Run(args, &out, &err) == 0 || out.Len() != 0 || strings.Contains(err.String(), "SECRET_") {
			t.Fatalf("bad failure: %s / %s", &out, &err)
		}
	}
	for _, b := range []string{`{"resourceLogs":["SECRET_ERROR"]}`, `{"resourceLogs":[],"error":"SECRET_ERROR"}`} {
		if e := os.WriteFile(p, []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
		var out, err bytes.Buffer
		if Run([]string{"investigate", "--claude-code-logs", p, "--before-period", "2026-10-11", "--after-period", "2026-10-12"}, &out, &err) == 0 || out.Len() != 0 || strings.Contains(err.String(), "SECRET_") {
			t.Fatalf("bad failure: %s / %s", &out, &err)
		}
	}
}

func TestClaudeRotatedCLIOptionalPeriodsAndSafeErrors(t *testing.T) {
	p := claudeCapture(t)
	for _, format := range []string{"text", "json"} {
		args := []string{"investigate", "--claude-code-logs", p, "--claude-code-logs", p, "--deduplicate-exact", "--format", format}
		var out, errout bytes.Buffer
		if code := Run(args, &out, &errout); code != 0 {
			t.Fatal(code, errout.String())
		}
		if strings.Contains(out.String()+errout.String(), "SECRET_") || strings.Contains(out.String(), "PRIVATE_") {
			t.Fatal("input leaked")
		}
		if format == "json" {
			var r struct {
				Files         int    `json:"input_files"`
				Selection     string `json:"period_selection"`
				Before, After struct {
					Count int `json:"observed_api_request_records"`
				}
			}
			if err := json.Unmarshal(out.Bytes(), &r); err != nil {
				t.Fatal(err)
			}
			if r.Files != 2 || r.Selection != "observed_span_halves" || r.Before.Count != 1 || r.After.Count != 1 {
				t.Fatal(out.String())
			}
		} else {
			for _, s := range []string{"file set's observed span", "Sun 2026-10-11", "Mon 2026-10-12 00:00:00.000000002 UTC"} {
				if !strings.Contains(out.String(), s) {
					t.Fatal(s, out.String())
				}
			}
		}
	}
	for _, extra := range [][]string{{"--claude-code-logs", "SECRET_MISSING"}, {"--claude-code-logs", ""}, {"--before-period", "2026-10-11"}} {
		var out, errout bytes.Buffer
		if code := Run(append([]string{"investigate", "--claude-code-logs", p}, extra...), &out, &errout); code == 0 || out.Len() != 0 || strings.Contains(errout.String(), "SECRET_") {
			t.Fatal(code, out.String(), errout.String())
		}
	}
}
