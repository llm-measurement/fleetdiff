// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func providerCLIFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProviderCoverageCLIPrivacyAndRendering(t *testing.T) {
	const sentinel = "PRIVATE_PROVIDER_SENTINEL@example.invalid"
	t.Setenv("PROVIDER_CLI_KEY", "PRIVATE_PROVIDER_SENTINEL_SECRET")
	provider := providerCLIFile(t, "PRIVATE_COUNTS.csv", "date_utc,provider,provider_model,requests,output_tokens\n2026-09-01,openai,gpt-6-luna,2,8\n2026-09-02,PRIVATE_PROVIDER_SENTINEL@example.invalid,PRIVATE_PROVIDER_SENTINEL@example.invalid,0,0\n")
	daily := providerCLIFile(t, "PRIVATE_DAILY.csv", "date,api_requests,successful_requests,failed_requests\n2026-09-01,1,1,0\n2026-09-02,1,0,1\n")
	const rowHeader = "request_id,call_type,startTime,endTime,prompt_tokens,completion_tokens,total_tokens,model,custom_llm_provider,status,api_key\n"
	rows := providerCLIFile(t, "PRIVATE_SPEND.csv", rowHeader+sentinel+",completion,2026-09-01T12:00:00Z,2026-09-01T12:00:01Z,10,2,12,openai/gpt-6-luna,openai,success,"+sentinel+"\n"+sentinel+"2,completion,2026-09-02T12:00:00Z,2026-09-02T12:00:01Z,,,,"+sentinel+","+sentinel+",failure,"+sentinel+"\n")
	base := []string{"investigate", "--litellm-spend", rows, "--before-period", "2026-09-01", "--after-period", "2026-09-02", "--litellm-daily-spend", daily, "--hash-secret-env", "PROVIDER_CLI_KEY"}
	for _, format := range []string{"text", "json"} {
		for _, hashes := range []bool{false, true} {
			args := append(append([]string{}, base...), "--format", format, "--provider-daily-counts", provider)
			if hashes {
				args = append(args, "--show-hashes")
			}
			var out, errs bytes.Buffer
			if code := Run(args, &out, &errs); code != 0 {
				t.Fatal(code, errs.String())
			}
			for _, private := range []string{sentinel, provider, daily, rows, os.Getenv("PROVIDER_CLI_KEY")} {
				if strings.Contains(out.String()+errs.String(), private) {
					t.Fatal("privacy sentinel escaped")
				}
			}
			if format == "text" {
				for _, want := range []string{"Provider request count exceeds logged rows by 1", "Both LiteLLM tables agree with each other but differ from the provider count.", "LiteLLM daily requests (whole day): 1.", "unavailable", "unverified"} {
					if !strings.Contains(out.String(), want) {
						t.Fatal("missing text", want, out.String())
					}
				}
				if strings.Contains(out.String(), "net row deficit") {
					t.Fatal("unverified population described as a deficit")
				}
			} else {
				var report spend.Report
				if err := json.Unmarshal(out.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				c := report.ProviderCoverage
				if c == nil || len(c.Days) != 2 || !c.Days[0].LiteLLMTablesAgreeProviderDiffers || c.Days[0].Models[0].Comparable || c.Days[1].Models[1].LoggedOutputTokens != nil {
					t.Fatal(c)
				}
				// The whole-day daily total never appears on individual model lines.
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
					t.Fatal(err)
				}
				if bytes.Count(raw["provider_coverage"], []byte("\"litellm_daily_requests\"")) != 2 {
					t.Fatal("daily totals duplicated on model rows")
				}
			}
		}
		// Opt-in rendering prepends a section; baseline text and JSON stay intact.
		var baseline, errs bytes.Buffer
		if code := Run(append(append([]string{}, base...), "--format", format), &baseline, &errs); code != 0 {
			t.Fatal(code, errs.String())
		}
		if strings.Contains(baseline.String(), "provider_coverage") || strings.HasPrefix(baseline.String(), "Provider count coverage") {
			t.Fatal("default changed")
		}
	}
	for _, body := range []string{
		"date_utc,provider,provider_model,requests," + sentinel + "\n",
		"date_utc,provider,provider_model,requests\n2026-09-01,openai,gpt-6-luna," + sentinel + "\n",
	} {
		bad := providerCLIFile(t, "PRIVATE_INVALID.csv", body)
		var out, errs bytes.Buffer
		if code := Run(append(append([]string{}, base...), "--provider-daily-counts", bad), &out, &errs); code == 0 || out.Len() != 0 || strings.Contains(errs.String(), sentinel) || strings.Contains(errs.String(), bad) {
			t.Fatal("unsafe error or partial report")
		}
	}
}

func TestProviderFlagsAreIsolatedBeforeDatabaseAccess(t *testing.T) {
	for _, args := range [][]string{
		{"compare", "--provider-daily-counts", "PRIVATE.csv"},
		{"investigate", "--provider-daily-counts", "PRIVATE.csv"},
		{"investigate", "--litellm-spend", "PRIVATE.csv", "--provider-daily-counts", ""},
		{"investigate", "--litellm-db", "--provider-model-map", "PRIVATE.csv"},
		{"investigate", "--litellm-db", "--provider-daily-counts", "PRIVATE.csv", "--period", "2026-09-01"},
		{"investigate", "--litellm-db", "--provider-daily-counts", "PRIVATE.csv", "--scope-route", "PRIVATE_ROUTE"},
	} {
		called := false
		exporter := func(context.Context, exportdata.Options) (exportdata.Result, error) {
			called = true
			return exportdata.Result{}, nil
		}
		code, out, errs := databaseRun(args, exporter)
		if code != 2 || called || out != "" || strings.Contains(errs, "PRIVATE") {
			t.Fatal(code, out, errs, called)
		}
	}
}

func TestProviderCountsWithFakeDatabaseSnapshot(t *testing.T) {
	provider := providerCLIFile(t, "counts.csv", "date_utc,provider,provider_model,requests\n2026-09-01,openai,gpt-6-luna,1\n2026-09-02,openai,gpt-6-luna,1\n")
	t.Setenv("PROVIDER_DB_KEY", "synthetic-provider-db-secret")
	args := []string{"investigate", "--litellm-db", "--daily", "--before-period", "2026-09-01", "--after-period", "2026-09-02", "--hash-secret-env", "PROVIDER_DB_KEY", "--provider-daily-counts", provider, "--format", "json"}
	calls := 0
	code, out, errs := databaseRun(args, func(ctx context.Context, opts exportdata.Options) (exportdata.Result, error) {
		calls++
		return databaseFixture(t, opts, []byte("request_id,call_type,startTime,prompt_tokens,completion_tokens,total_tokens\n"), []byte("date,api_requests,successful_requests,failed_requests\n2026-09-01,0,0,0\n2026-09-02,0,0,0\n")), nil
	})
	if code != 0 || calls != 1 || !strings.Contains(out, "\"litellm_tables_agree_provider_differs\": true") {
		t.Fatal(code, errs, out, calls)
	}
}

func TestProviderCoverageTextGolden(t *testing.T) {
	one, two := uint64(1), uint64(2)
	difference := int64(1)
	coverage := &spend.ProviderCoverage{Days: []spend.ProviderCoverageDay{{
		Date: "2026-09-01", Status: "complete_day", PopulationStatus: "population_unverified",
		LoggedRows: 1, LiteLLMDailyRequests: &one, ProviderRequests: &two, LiteLLMTablesAgreeProviderDiffers: true,
		Models: []spend.ProviderCoverageModel{{Provider: "openai", Model: "gpt-6-luna", Status: "provider_exceeds_logged_rows", ProviderRequests: &two, LoggedRows: 1, RequestDifference: &difference}},
	}}}
	var out bytes.Buffer
	renderProviderCoverage(&out, coverage)
	const want = `Provider count coverage:
  Population and counting units unverified; differences are arithmetic comparisons only.
  2026-09-01 UTC (complete_day; population_unverified): logged rows 1; provider requests 2.
    LiteLLM daily requests (whole day): 1.
    Both LiteLLM tables agree with each other but differ from the provider count.
    openai/gpt-6-luna: provider requests 2; logged rows 1; provider_exceeds_logged_rows.
      Provider request count exceeds logged rows by 1.
      Logged failures: 0; unknown status: 0 (included in logged rows).
      Input tokens: provider unavailable; logged unavailable. Output tokens: provider unavailable; logged unavailable.
Provider counts use one primary counter; provider errors and throttles are not added. Differences identify no individual requests. Recorded totals remain unchanged.

`
	if out.String() != want {
		t.Fatalf("provider text changed:\n%s", out.String())
	}
	got, err := json.Marshal(coverage.Days[0].Models[0])
	if err != nil {
		t.Fatal(err)
	}
	const wantJSON = `{"provider":"openai","model":"gpt-6-luna","status":"provider_exceeds_logged_rows","comparable":false,"provider_requests":2,"logged_rows":1,"failed_rows":0,"unknown_status_rows":0,"provider_minus_logged_rows":1,"provider_input_tokens":null,"provider_output_tokens":null,"logged_input_tokens":null,"logged_output_tokens":null,"provider_minus_logged_input_tokens":null,"provider_minus_logged_output_tokens":null}`
	if string(got) != wantJSON {
		t.Fatalf("provider JSON changed: %s", got)
	}
}
