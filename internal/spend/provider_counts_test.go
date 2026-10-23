// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

const providerHeader = "date_utc,provider,provider_model,requests\n"
const providerMapHeader = "source_provider,source_model,provider,provider_model\n"

func providerOptions(t *testing.T, csv string) Options {
	t.Helper()
	opts := options(t)
	opts.ProviderCountsPath = readerWrite(t, ".csv", csv)
	return opts
}

func providerContract(t *testing.T, change func(*ProviderCountContract)) string {
	t.Helper()
	c := ProviderCountContract{
		Schema: "fleetdiff-provider-counts/v1", CountBasis: "primary_requests",
		Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
		Retrieved: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC), PaginationComplete: true,
		Population:         ProviderPopulation{"matched", "matched", "matched", "matched", "matched"},
		CountingUnitsMatch: true, TokenUnitsMatch: true,
	}
	if change != nil {
		change(&c)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return readerWrite(t, ".json", string(data))
}

func qualifiedSample(id, day, provider, model string) map[string]string {
	r := sample(id, day, "PRIVATE_KEY", 10, 2)
	r["custom_llm_provider"], r["model"] = provider, model
	return r
}

func TestProviderQualifiedIdentityAndExplicitMappings(t *testing.T) {
	for _, tc := range []struct {
		name, provider, model, mapping string
		mapped                         bool
	}{
		{"exact", "openai", "openai/gpt-6-luna", "", true},
		{"bare_id", "openai", "gpt-6-luna", "", false},
		{"wrong_provider", "bedrock", "openai/gpt-6-luna", "", false},
		{"missing_provider", "", "openai/gpt-6-luna", "", false},
		{"alias", "azure", "PRIVATE_DEPLOYMENT", "azure,PRIVATE_DEPLOYMENT,openai,gpt-6-luna\n", true},
		{"bare_explicit", "openai", "gpt-6-luna", "openai,gpt-6-luna,openai,gpt-6-luna\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := providerOptions(t, providerHeader+"2026-09-01,openai,gpt-6-luna,1\n")
			if tc.mapping != "" {
				opts.ProviderModelMapPath = readerWrite(t, ".csv", providerMapHeader+tc.mapping)
			}
			r, err := Investigate(writeRows(t, []map[string]string{qualifiedSample("1", "2026-09-01", tc.provider, tc.model)}), opts)
			if err != nil {
				t.Fatal(err)
			}
			models := r.ProviderCoverage.Days[0].Models
			if tc.mapped {
				if len(models) != 1 || models[0].LoggedRows != 1 || models[0].Status != "matched" || models[0].Provider != "openai" || models[0].Model != "gpt-6-luna" {
					t.Fatal(models)
				}
			} else if len(models) != 2 || models[0].LoggedRows != 0 || models[1].Status != "unmapped_spend_model" || models[1].Model == tc.model || r.ProviderCoverage.Days[0].ProviderRequests != nil {
				t.Fatal(models)
			}
		})
	}
	opts := providerOptions(t, providerHeader+"2026-09-01,openai,shared,1\n2026-09-01,bedrock,shared,0\n")
	r, err := Investigate(writeRows(t, []map[string]string{qualifiedSample("1", "2026-09-01", "openai", "openai/shared")}), opts)
	if err != nil {
		t.Fatal(err)
	}
	models := r.ProviderCoverage.Days[0].Models
	if len(models) != 2 || models[0].LoggedRows != 1 || models[1].LoggedRows != 0 || models[0].Model == models[1].Model {
		t.Fatal(models)
	}
}

func TestProviderManyAliasesAndMappingConflicts(t *testing.T) {
	opts := providerOptions(t, providerHeader+"2026-09-01,openai,gpt-6-luna,2\n")
	opts.ProviderModelMapPath = readerWrite(t, ".csv", providerMapHeader+"azure,a,openai,gpt-6-luna\nazure,b,openai,gpt-6-luna\n")
	r, err := Investigate(writeRows(t, []map[string]string{qualifiedSample("1", "2026-09-01", "azure", "a"), qualifiedSample("2", "2026-09-01", "azure", "b")}), opts)
	if err != nil || len(r.ProviderCoverage.Days[0].Models) != 1 || r.ProviderCoverage.Days[0].Models[0].LoggedRows != 2 {
		t.Fatal(r, err)
	}
	for _, mapping := range []string{
		"azure,a,openai,gpt-6-luna\nazure,a,bedrock,other\n",
		"azure,a,openai,gpt-6-luna\nazure,a,openai,gpt-6-luna\n",
		"openai,openai/gpt-6-luna,bedrock,other\n",
		"azure,,openai,gpt-6-luna\n",
	} {
		opts.ProviderModelMapPath = readerWrite(t, ".csv", providerMapHeader+mapping)
		if _, err := newProviderCoverage(opts); err == nil {
			t.Fatal("accepted conflicting/duplicate mapping")
		}
	}
}

func TestProviderThreeWayCountsAndUnchangedTotals(t *testing.T) {
	rows := []map[string]string{qualifiedSample("1", "2026-09-01", "openai", "openai/gpt-6-luna"), qualifiedSample("2", "2026-09-02", "openai", "openai/gpt-6-luna")}
	path := writeRows(t, rows)
	base := options(t)
	base.DailySpendPath = readerWrite(t, ".csv", dailyHeader+"2026-09-01,1,1,0\n2026-09-02,9,9,0\n")
	baseline, err := Investigate(path, base)
	if err != nil {
		t.Fatal(err)
	}
	opts := base
	opts.ProviderCountsPath = readerWrite(t, ".csv", "date_utc,provider,provider_model,requests,input_tokens,output_tokens\n2026-09-01,openai,gpt-6-luna,2,20,4\n2026-09-02,openai,gpt-6-luna,1,10,2\n")
	opts.ProviderContractPath = providerContract(t, nil)
	r, err := Investigate(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := r.ProviderCoverage
	if !c.Days[0].LiteLLMTablesAgreeProviderDiffers || c.Days[1].LiteLLMTablesAgreeProviderDiffers {
		t.Fatal(c.Days)
	}
	m := c.Days[0].Models[0]
	if !m.Comparable || m.RequestDifference == nil || *m.RequestDifference != 1 || *m.InputDifference != 10 || *m.OutputDifference != 2 {
		t.Fatal(m)
	}
	if c.Days[1].Models[0].Status != "matched" || *c.Days[1].LiteLLMDailyRequests != 9 {
		t.Fatal(c.Days[1])
	}
	r.ProviderCoverage = nil
	if !reflect.DeepEqual(r, baseline) {
		t.Fatal("provider opt-in changed existing report totals or fields")
	}
	opts.DailySpendPath = readerWrite(t, ".csv", dailyHeader+"2026-09-01,0,0,0\n2026-09-02,0,0,0\n")
	r, err = Investigate(readerWrite(t, ".json", "[]"), opts)
	if err != nil || !r.ProviderCoverage.Days[0].LiteLLMTablesAgreeProviderDiffers || r.ProviderCoverage.Days[0].LoggedRows != 0 {
		t.Fatal(r, err)
	}
}

func TestProviderUnavailableAndZeroMeasurements(t *testing.T) {
	for _, tc := range []struct{ requests, input, output, loggedOutput string }{
		{"", "", "0", ""}, {"0", "", "", "0"}, {"", "0", "5", "-1"},
	} {
		opts := providerOptions(t, "date_utc,provider,provider_model,requests,input_tokens,output_tokens\n2026-09-01,openai,gpt-6-luna,"+tc.requests+","+tc.input+","+tc.output+"\n")
		opts.ProviderContractPath = providerContract(t, nil)
		row := qualifiedSample("1", "2026-09-01", "openai", "openai/gpt-6-luna")
		row["completion_tokens"] = tc.loggedOutput
		r, err := Investigate(writeRows(t, []map[string]string{row}), opts)
		if err != nil {
			t.Fatal(err)
		}
		m := r.ProviderCoverage.Days[0].Models[0]
		if (m.ProviderRequests == nil) != (tc.requests == "") || (m.ProviderInputTokens == nil) != (tc.input == "") {
			t.Fatal(m)
		}
		if tc.output == "0" && (m.ProviderOutputTokens == nil || *m.ProviderOutputTokens != 0) {
			t.Fatal(m)
		}
		if tc.loggedOutput != "0" && (m.LoggedOutputTokens != nil || m.OutputDifference != nil) {
			t.Fatal("missing/invalid logged usage became zero", m)
		}
		if tc.requests == "" && (m.RequestDifference != nil || m.Comparable || m.Status != "request_count_unavailable") {
			t.Fatal(m)
		}
	}
	opts := providerOptions(t, providerHeader+"2026-09-01,openai,gpt-6-luna,0\n")
	r, err := Investigate(writeRows(t, []map[string]string{qualifiedSample("1", "2026-09-02", "openai", "openai/gpt-6-luna")}), opts)
	if err != nil || r.ProviderCoverage.Days[1].Models[0].Status != "provider_counts_missing" || r.ProviderCoverage.Days[1].Models[0].ProviderRequests != nil {
		t.Fatal(r, err)
	}
}

func TestProviderUTCAndDayReversal(t *testing.T) {
	opts := providerOptions(t, providerHeader+"2026-09-01,openai,gpt-6-luna,2\n2026-09-02,openai,gpt-6-luna,0\n")
	a, b := qualifiedSample("1", "2026-09-01", "openai", "openai/gpt-6-luna"), qualifiedSample("2", "2026-09-02", "openai", "openai/gpt-6-luna")
	a["startTime"], a["endTime"] = "2026-09-02T01:00:00+02:00", "2026-09-02T01:00:01+02:00"
	r, err := Investigate(writeRows(t, []map[string]string{a, b}), opts)
	if err != nil {
		t.Fatal(err)
	}
	if *r.ProviderCoverage.Days[0].Models[0].RequestDifference != 1 || *r.ProviderCoverage.Days[1].Models[0].RequestDifference != -1 {
		t.Fatal(r.ProviderCoverage.Days)
	}
	opts.BeforePeriod = "2026-09-01T12:00:00Z/2026-09-02T00:00:00Z"
	opts.AfterPeriod = "2026-09-02T00:00:00Z/2026-09-02T12:00:00Z"
	r, err = Investigate(writeRows(t, []map[string]string{a}), opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range r.ProviderCoverage.Days {
		if d.Status != "partial_day" || d.Models[0].RequestDifference != nil || d.Models[0].Comparable {
			t.Fatal(d)
		}
	}
	opts.Now = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	before, _ := ParsePeriod("2026-09-01")
	after, _ := ParsePeriod("2026-09-02")
	builder, err := newProviderCoverage(opts, before, after)
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := builder.result(nil)
	if err != nil || coverage.Days[1].Status != "unsettled_day" || coverage.Days[1].Models[0].RequestDifference != nil {
		t.Fatal(coverage, err)
	}
}

func TestProviderPopulationAndCountBasis(t *testing.T) {
	for _, tc := range []struct {
		name       string
		change     func(*ProviderCountContract)
		status     string
		comparable bool
	}{
		{"matched", nil, "matched", true},
		{"mismatch", func(c *ProviderCountContract) { c.Population.Gateway = "mismatch" }, "population_mismatch", false},
		{"unverified", func(c *ProviderCountContract) { c.Population.Account = "unverified" }, "population_unverified", false},
		{"incomplete", func(c *ProviderCountContract) { c.PaginationComplete = false }, "provider_export_incomplete", false},
		{"units", func(c *ProviderCountContract) { c.CountingUnitsMatch = false }, "matched", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := providerOptions(t, providerHeader+"2026-09-01,openai,gpt-6-luna,2\n")
			opts.ProviderContractPath = providerContract(t, tc.change)
			r, err := Investigate(writeRows(t, []map[string]string{qualifiedSample("1", "2026-09-01", "openai", "openai/gpt-6-luna")}), opts)
			if err != nil {
				t.Fatal(err)
			}
			d := r.ProviderCoverage.Days[0]
			if d.PopulationStatus != tc.status || d.Models[0].Comparable != tc.comparable {
				t.Fatal(d)
			}
			if (tc.status == "population_mismatch" || tc.status == "provider_export_incomplete") && d.Models[0].RequestDifference != nil {
				t.Fatal(d)
			}
		})
	}
	opts := providerOptions(t, "date_utc,provider,provider_model,requests,output_tokens\n2026-09-01,anthropic,claude-model,,12\n")
	opts.ProviderContractPath = providerContract(t, func(c *ProviderCountContract) { c.CountBasis = "tokens_only"; c.CountingUnitsMatch = false })
	if _, err := newProviderCoverage(opts); err != nil {
		t.Fatal(err)
	}
	// Scope agreement cannot resolve an ambiguous model alias.
	opts = providerOptions(t, providerHeader+"2026-09-01,openai,gpt-6-luna,2\n")
	opts.ProviderContractPath = providerContract(t, nil)
	r, err := Investigate(writeRows(t, []map[string]string{qualifiedSample("1", "2026-09-01", "openai", "PRIVATE_ALIAS")}), opts)
	if err != nil {
		t.Fatal(err)
	}
	d := r.ProviderCoverage.Days[0]
	if d.UnmappedLoggedRows != 1 || d.Models[0].Comparable || d.Models[0].OutputDifference != nil {
		t.Fatal(d)
	}
}

func TestProviderCSVRejectsUnsafeOrInvalidInputs(t *testing.T) {
	for _, body := range []string{
		"", "date_utc,provider,provider_model,requests,PRIVATE_SENTINEL\n", "date_utc,provider,provider_model,requests,requests\n",
		providerHeader + "2026-09-01,openai,gpt-6-luna,1\n2026-09-01,openai,gpt-6-luna,2\n",
		providerHeader + "2026-09-01,openai,gpt-6-luna,\n", providerHeader + "2026-09-01T00:00:00Z,openai,gpt-6-luna,1\n",
		providerHeader + "2026-02-30,openai,gpt-6-luna,1\n", providerHeader + "2026-09-01,,gpt-6-luna,1\n",
		providerHeader + "2026-09-01,openai,\"PRIVATE\nSENTINEL\",1\n", providerHeader + strings.Repeat("x", maxProviderLine+1),
		strings.Repeat("x", maxProviderBytes+1), providerHeader + "2026-09-01,openai,\xff,1\n",
	} {
		if _, err := newProviderCoverage(providerOptions(t, body)); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("invalid input accepted or leaked", err)
		}
	}
	for _, n := range []string{"-1", "+1", "1.0", "1e2", " 1", "9223372036854775808", "PRIVATE_SENTINEL"} {
		if _, err := newProviderCoverage(providerOptions(t, providerHeader+"2026-09-01,openai,gpt-6-luna,"+n+"\n")); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal(err)
		}
	}
	var many strings.Builder
	many.WriteString(providerHeader)
	for i := 0; i <= maxProviderRows; i++ {
		fmt.Fprintf(&many, "2026-09-01,openai,model-%d,0\n", i)
	}
	if _, err := newProviderCoverage(providerOptions(t, many.String())); err == nil {
		t.Fatal("row bound not enforced")
	}
	regular := readerWrite(t, ".csv", providerHeader)
	dir := t.TempDir()
	link, fifo := filepath.Join(dir, "PRIVATE_LINK"), filepath.Join(dir, "PRIVATE_FIFO")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, link, fifo, filepath.Join(dir, "PRIVATE_ABSENT")} {
		if _, err := readProviderFile(path); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal(err)
		}
	}
	opts := providerOptions(t, fmt.Sprintf(providerHeader+"2026-09-01,openai,a,%d\n2026-09-01,openai,b,1\n", uint64(math.MaxInt64)))
	p, _ := ParsePeriod("2026-09-01")
	b, err := newProviderCoverage(opts, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.result(nil); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
}

func TestProviderContractValidation(t *testing.T) {
	for _, change := range []func(*ProviderCountContract){
		func(c *ProviderCountContract) { c.Schema = "PRIVATE_SENTINEL" }, func(c *ProviderCountContract) { c.CountBasis = "errors_plus_invocations" },
		func(c *ProviderCountContract) { c.Population.Account = "PRIVATE_SENTINEL" }, func(c *ProviderCountContract) { c.Start = c.Start.Add(time.Hour) },
		func(c *ProviderCountContract) { c.Retrieved = c.End.Add(-time.Hour) }, func(c *ProviderCountContract) { c.Retrieved = c.End.AddDate(1, 0, 0) },
		func(c *ProviderCountContract) { c.CountBasis = "tokens_only" },
	} {
		_, err := readProviderContract(providerContract(t, change), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
		if err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal("invalid contract accepted or leaked", err)
		}
	}
	data, err := os.ReadFile(providerContract(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{strings.Replace(string(data), "{", "{\"schema\":\"duplicate\",", 1), strings.Replace(string(data), "{", "{\"PRIVATE_SENTINEL\":\"secret\",", 1)} {
		if _, err := readProviderContract(readerWrite(t, ".json", body), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)); err == nil || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatal(err)
		}
	}
	for _, body := range []string{
		strings.Replace(string(data), "\"schema\"", "\"SCHEMA\"", 1),
		strings.Replace(string(data), "\"account\"", "\"Account\"", 1),
		strings.Replace(string(data), "\"pagination_complete\":true", "\"pagination_complete\":null", 1),
		strings.Replace(string(data), "\"counting_units_match\":true,", "", 1),
		string(data) + "{}",
	} {
		if _, err := readProviderContract(readerWrite(t, ".json", body), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("ambiguous contract accepted")
		}
	}
}
