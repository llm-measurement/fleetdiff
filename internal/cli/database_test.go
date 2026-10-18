// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func databaseRun(args []string, exporter exportdata.Exporter) (int, string, string) {
	var out, errs bytes.Buffer
	code := RunWithExporter(context.Background(), args, strings.NewReader(""), &out, &errs, exporter)
	return code, out.String(), errs.String()
}

func databaseFixture(t *testing.T, opts exportdata.Options, contents, daily []byte) exportdata.Result {
	t.Helper()
	if _, err := os.Lstat(opts.Directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("export destination must not exist: %v", err)
	}
	if strings.HasPrefix(filepath.Base(filepath.Dir(opts.Directory)), "fleetdiff-export-") {
		resolved, err := filepath.EvalSymlinks(filepath.Dir(opts.Directory))
		if err != nil || resolved != filepath.Dir(opts.Directory) {
			t.Fatalf("temporary path still has symlink components: %q %v", resolved, err)
		}
		parent, err := os.Stat(filepath.Dir(opts.Directory))
		if err != nil || parent.Mode().Perm() != 0700 {
			t.Fatalf("temporary export parent must be private: %v %v", parent, err)
		}
	}
	if err := os.Mkdir(opts.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	r := exportdata.Result{Directory: opts.Directory, SpendPath: filepath.Join(opts.Directory, "spend.csv"), Start: opts.Start, End: opts.End, Rows: 2}
	if err := os.WriteFile(r.SpendPath, contents, 0600); err != nil {
		t.Fatal(err)
	}
	if opts.Daily {
		r.DailyPath = filepath.Join(opts.Directory, "daily.csv")
		if err := os.WriteFile(r.DailyPath, daily, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func databaseSparseRows(t *testing.T, opts exportdata.Options) []byte {
	t.Helper()
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	rows := [][]string{{"request_id", "startTime", "endTime", "call_type", "status", "api_key", "session_id", "model", "prompt_tokens", "completion_tokens", "total_tokens", "spend", "cache_read_input_tokens", "cache_write_input_tokens"}}
	for i, at := range []time.Time{opts.Start.Add(time.Hour), opts.Start.Add(opts.End.Sub(opts.Start) / 2).Add(time.Hour)} {
		rows = append(rows, []string{[]string{"SENTINEL_REQUEST_A", "SENTINEL_REQUEST_B"}[i], at.Format(time.RFC3339Nano), at.Add(time.Second).Format(time.RFC3339Nano), "completion", "success", "SENTINEL_KEY", "SENTINEL_SESSION", "claude-sonnet-5-5", "100", "20", "120", "0.001", "0", "0"})
	}
	if err := w.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDatabaseExportOptions(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "explicit"}[explicit], func(t *testing.T) {
			args := []string{"export", "litellm", "--out", filepath.Join(t.TempDir(), "SENTINEL_DIRECTORY")}
			if explicit {
				args = append(args, "--from", "2026-10-01", "--to", "2026-10-15T00:00:00Z", "--format", "jsonl", "--daily", "--routing-details", "--usage-details=false", "--database-url-file", "SENTINEL_CREDENTIAL_FILE")
			}
			var got exportdata.Options
			calls := 0
			before := time.Now()
			code, out, errs := databaseRun(args, func(ctx context.Context, opts exportdata.Options) (exportdata.Result, error) {
				calls++
				got = opts
				return exportdata.Result{Rows: 23}, nil
			})
			if code != 0 || calls != 1 || errs != "" || !strings.Contains(out, "Exported 23 spend rows") || strings.Contains(out, "SENTINEL") {
				t.Fatalf("exit=%d calls=%d out=%q errors=%q", code, calls, out, errs)
			}
			if got.Directory != args[3] || got.End.Sub(got.Start) != 14*24*time.Hour {
				t.Fatalf("incorrect range/destination: %+v", got)
			}
			if explicit {
				if got.Format != "jsonl" || !got.Daily || !got.Routing || got.UsageDetails || got.CredentialFile != "SENTINEL_CREDENTIAL_FILE" || got.Start.Format(time.DateOnly) != "2026-10-01" {
					t.Fatalf("explicit options lost: %+v", got)
				}
			} else if got.Format != "csv" || got.Daily || got.Routing || !got.UsageDetails || !got.End.Equal(before.UTC().Truncate(24*time.Hour)) && !got.End.Equal(time.Now().UTC().Truncate(24*time.Hour)) {
				t.Fatalf("wrong defaults: %+v", got)
			}
		})
	}
}

func TestDatabaseNeverExportsOfflineCommands(t *testing.T) {
	cases := [][]string{
		nil, {"--help"}, {"--version"}, {"export", "--help"}, {"export", "litellm", "--help"},
		{"investigate", "--litellm-db", "--help"}, {"what-if", "--litellm-db", "--help"},
		{"inspect", "--help"}, {"diagnose", "--help"}, {"scan", "--help"},
		{"compare", "--before", "../../examples/two-systems/data/before", "--after", "../../examples/two-systems/data/after", "--expected", "owned,partner"},
		{"investigate", "--litellm-spend", "../../examples/litellm-spend/synthetic.csv", "--before-period", "2026-10-07", "--after-period", "2026-10-08"},
		{"inspect", "../../testdata/inspect-contract/v1/litellm-normal-no-usage-provenance.json"},
		{"diagnose", "../../examples/diagnose/safe.yaml"},
		{"scan", "../../examples/what-if-summaries/snapshots", "--expected", "demo-a,demo-b"},
		whatIfArgs(),
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			calls := 0
			exporter := func(context.Context, exportdata.Options) (exportdata.Result, error) {
				calls++
				return exportdata.Result{}, errors.New("SENTINEL_DATABASE")
			}
			code, _, errs := databaseRun(args, exporter)
			if calls != 0 || code != 0 && (len(args) == 0 || args[0] != "scan") {
				t.Fatalf("offline command exit=%d calls=%d: %s", code, calls, errs)
			}
		})
	}
}

func TestDatabaseInvalidOptionsNeverExport(t *testing.T) {
	t.Setenv("SENTINEL_WEAK_SECRET", "weak")
	for command, cases := range map[string][][]string{
		"export": {
			{"litellm"}, {"other"}, {"litellm", "--out", ""}, {"litellm", "--out", "new", "--format", "text"},
			{"litellm", "--out", "new", "--from", "2026-10-01"},
			{"litellm", "--out", "new", "--from", "", "--to", ""},
			{"litellm", "--out", "new", "--from", "SENTINEL_DATE", "--to", "2026-10-02"},
			{"litellm", "--out", "new", "--from", "2026-10-03", "--to", "2026-10-02"},
			{"litellm", "--out", "new", "--from", "2026-10-01T00:00:00+01:00", "--to", "2026-10-02"},
			{"litellm", "--out", "new", "--from", "2199-10-01", "--to", "2199-10-02"},
			{"litellm", "--out", "new", "--database-url-file", ""},
			{"litellm", "--out", "new", "extra"}, {"litellm", "--out", "new", "--usage-details=bad"},
			{"litellm", "--out", "new", "--daily", "--from", "2026-10-01T01:00:00Z", "--to", "2026-10-02"},
			{"litellm", "--out", "new", "--daily", "--from", "2000-10-01", "--to", "2026-10-02"},
		},
		"investigate": {
			{"--daily"}, {"--daily=false"}, {"--routing-details"}, {"--usage-details=false"}, {"--keep-export", "SENTINEL_PATH"}, {"--database-url-file", "SENTINEL_PATH"},
			{"--litellm-db", "--litellm-spend", "SENTINEL_PATH"}, {"--litellm-db", "--litellm-spend="},
			{"--litellm-db", "--litellm-daily-spend", "SENTINEL_PATH"}, {"--litellm-db", "--before", "SENTINEL_PATH"},
			{"--litellm-db", "--expected", "SENTINEL_PRODUCER"}, {"--litellm-db", "--allow-partial"},
			{"--litellm-db", "--format", "csv"}, {"--litellm-db", "--top", "0"}, {"--litellm-db", "--top", "101"},
			{"--litellm-db", "--group-by", "SENTINEL_GROUP"}, {"--litellm-db", "--hash-secret-env", "SENTINEL_WEAK_SECRET"},
			{"--litellm-db", "--before-period", "2026-10-01"}, {"--litellm-db", "--before-period=", "--after-period="},
			{"--litellm-db", "--before-period", "2026-10-02", "--after-period", "2026-10-01"},
			{"--litellm-db", "--before-period", "2026-10-01", "--after-period", "2026-10-01"},
			{"--litellm-db", "--before-period", "2199-10-01", "--after-period", "2199-10-02"},
			{"--litellm-db", "--period", "SENTINEL_PERIOD"}, {"--litellm-db", "--period="},
			{"--litellm-db", "--period", "2026-10-01", "--before-period", "2026-10-01", "--after-period", "2026-10-02"},
			{"--litellm-db", "--session-budget-usd", "-1"}, {"--litellm-db", "--session-budget-usd", "1", "--session-counter-window", "1ms"},
			{"--litellm-db", "--session-budget-usd", "1", "--group-by", "key"},
			{"--litellm-db", "--routing-by", "route"}, {"--litellm-db", "--show-labels"},
			{"--litellm-db", "--scope-route", "SENTINEL_ROUTE"},
			{"--litellm-db", "--daily", "--scope-route", "SENTINEL_ROUTE", "--before-period", "2026-10-01", "--after-period", "2026-10-02"},
			{"--litellm-db", "--routing-audit", "--period", "2026-10-01"},
			{"--litellm-db", "--keep-export="}, {"--litellm-db", "--database-url-file="},
			{"--litellm-db", "SENTINEL_EXTRA"},
			{"--litellm-db", "--daily", "--period", "2026-10-01T01:00:00Z/2026-10-02T00:00:00Z"},
		},
		"what-if": {
			{"--daily"}, {"--keep-export", "SENTINEL_PATH"}, {"--database-url-file", "SENTINEL_PATH"},
			{"--litellm-db", "--litellm-spend", "SENTINEL_PATH"}, {"--litellm-db", "--summaries", "SENTINEL_PATH"},
			{"--litellm-db", "--expected", "SENTINEL_PRODUCER"}, {"--litellm-db", "--model-usage-contract", "SENTINEL_CONTRACT"},
			{"--litellm-db"}, {"--litellm-db", "--from", "SENTINEL_MODEL", "--to", "gpt-6.1-sol", "--fraction", "0.5"},
			{"--litellm-db", "--scenario", "SENTINEL_MISSING_FILE"},
			{"--litellm-db", "--scenario", "../../examples/what-if/scenario.json", "--period", "SENTINEL_PERIOD"},
			{"--litellm-db", "--scenario", "../../examples/what-if/scenario.json", "--period="},
			{"--litellm-db", "--scenario", "../../examples/what-if/scenario.json", "--period", "2199-10-01"},
			{"--litellm-db", "--scenario", "../../examples/what-if/scenario.json", "--fraction", "0.5"},
			{"--litellm-db", "--scenario", "../../examples/what-if/scenario.json", "--prices", "SENTINEL_MISSING_FILE"},
			{"--litellm-db", "--scenario", "../../examples/what-if/scenario.json", "--workload-map", "SENTINEL_MISSING_FILE"},
			{"--litellm-db", "--scenario", "../../examples/what-if/scenario.json", "--daily", "--period", "2026-10-01T01:00:00Z/2026-10-02T00:00:00Z"},
		},
		"compare": {{"--litellm-db"}},
	} {
		for _, flags := range cases {
			args := append([]string{command}, flags...)
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				calls := 0
				code, out, errs := databaseRun(args, func(context.Context, exportdata.Options) (exportdata.Result, error) {
					calls++
					return exportdata.Result{}, errors.New("SENTINEL_DRIVER_ERROR")
				})
				if code != 2 || calls != 0 || out != "" || strings.Contains(errs, "SENTINEL") {
					t.Fatalf("exit=%d calls=%d out=%q errors=%q", code, calls, out, errs)
				}
			})
		}
	}
}

func TestDatabaseURLArgumentsArePrivate(t *testing.T) {
	for _, args := range [][]string{
		{"export", "litellm", "postgres://SENTINEL_USER:SENTINEL_PASSWORD@SENTINEL_HOST/db"},
		{"investigate", "--litellm-db", "postgresql://SENTINEL_HOST/db"},
		{"what-if", "--litellm-db", "--database-url-file=postgres://SENTINEL_HOST/db"},
		{"export", "--help", "https://SENTINEL_HOST"},
		{"export", "litellm", "postgres:SENTINEL_CREDENTIAL"},
	} {
		code, out, errs := databaseRun(args, func(context.Context, exportdata.Options) (exportdata.Result, error) {
			t.Fatal("URL argument reached exporter")
			return exportdata.Result{}, nil
		})
		if code != 2 || out != "" || !strings.Contains(errs, "URLs are not accepted") || strings.Contains(errs, "SENTINEL") {
			t.Fatalf("exit=%d out=%q errors=%q", code, out, errs)
		}
	}
}

func TestDatabaseLibraryCallersStayOffline(t *testing.T) {
	for _, args := range [][]string{
		{"export", "litellm", "--out", "new"}, {"investigate", "--litellm-db"},
		{"what-if", "--litellm-db", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5"},
	} {
		var out, errs bytes.Buffer
		if code := Run(args, &out, &errs); code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "offline caller") {
			t.Fatalf("Run attempted database access: exit=%d %s %s", code, &out, &errs)
		}
		out.Reset()
		errs.Reset()
		if code := RunWithInput(args, strings.NewReader(""), &out, &errs); code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "offline caller") {
			t.Fatalf("RunWithInput attempted database access: exit=%d %s %s", code, &out, &errs)
		}
	}
}

func TestDatabaseDefaultInvestigationKeepsFullWindow(t *testing.T) {
	var opts exportdata.Options
	code, out, errs := databaseRun([]string{"investigate", "--litellm-db", "--format", "json"}, func(_ context.Context, o exportdata.Options) (exportdata.Result, error) {
		opts = o
		return databaseFixture(t, o, databaseSparseRows(t, o), nil), nil
	})
	if code != 0 || errs != "" {
		t.Fatalf("exit=%d: %s", code, errs)
	}
	var report spend.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if !report.Before.Period.Start.Equal(opts.Start) || !report.After.Period.End.Equal(opts.End) || report.Before.Period.End.Sub(report.Before.Period.Start) != 7*24*time.Hour || !report.Before.Period.End.Equal(report.After.Period.Start) || report.Before.Requests != 1 || report.After.Requests != 1 {
		t.Fatalf("sparse export changed requested periods: before=%+v after=%+v", report.Before, report.After)
	}
	if _, err := os.Stat(filepath.Dir(opts.Directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary parent remains: %v", err)
	}
	if !opts.Routing || !opts.UsageDetails || strings.Contains(out, "SENTINEL") {
		t.Fatalf("missing export details or identity leak: %+v %s", opts, out)
	}
}

func TestDatabaseReportsMatchExistingFilePaths(t *testing.T) {
	t.Setenv("DATABASE_TEST_HASH", "fixed-database-test-hash-secret-at-least-16-bytes")
	for _, tc := range []struct {
		name, command, fixture, daily string
		flags                         []string
	}{
		{"spend", "investigate", "litellm-spend/synthetic.csv", "", []string{"--before-period", "2026-10-07", "--after-period", "2026-10-08"}},
		{"coverage", "investigate", "litellm-spend/coverage_runtime_spend.csv", "litellm-spend/coverage_runtime_daily.csv", []string{"--before-period", "2026-10-07", "--after-period", "2026-10-08"}},
		{"routing", "investigate", "routing/synthetic.csv", "", []string{"--before-period", "2026-10-01", "--after-period", "2026-10-02", "--routing-audit"}},
		{"scope", "investigate", "routing/synthetic.csv", "", []string{"--before-period", "2026-10-01", "--after-period", "2026-10-02", "--scope-route", "coding-default"}},
		{"session", "investigate", "litellm-spend/session-budget.csv", "", []string{"--period", "2026-10-03", "--session-budget-usd", "1.00"}},
		{"whatif", "what-if", "what-if/spend.csv", "", []string{"--period", "2026-10-07", "--scenario", "../../examples/what-if/scenario.json"}},
		{"whatif-routing", "what-if", "routing/synthetic.csv", "", []string{"--period", "2026-10-02", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5", "--scope-route", "coding-default"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := append(append([]string{}, tc.flags...), "--format", "json")
			if tc.command == "investigate" {
				flags = append(flags, "--hash-secret-env", "DATABASE_TEST_HASH")
			}
			fileArgs := append([]string{tc.command, "--litellm-spend", "../../examples/" + tc.fixture}, flags...)
			dbArgs := append([]string{tc.command, "--litellm-db"}, flags...)
			contents, err := os.ReadFile("../../examples/" + tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			var daily []byte
			if tc.daily != "" {
				fileArgs = append(fileArgs, "--litellm-daily-spend", "../../examples/"+tc.daily)
				dbArgs = append(dbArgs, "--daily")
				daily, err = os.ReadFile("../../examples/" + tc.daily)
				if err != nil {
					t.Fatal(err)
				}
			}
			fileCode, want, fileErr := databaseRun(fileArgs, nil)
			var opts exportdata.Options
			code, got, errs := databaseRun(dbArgs, func(_ context.Context, o exportdata.Options) (exportdata.Result, error) {
				opts = o
				return databaseFixture(t, o, contents, daily), nil
			})
			if code != 0 || fileCode != 0 || got != want || errs != fileErr {
				t.Fatalf("database/file parity: exit=%d/%d errors=%q/%q\n%s\nwant:\n%s", code, fileCode, errs, fileErr, got, want)
			}
			if !opts.Routing || !opts.UsageDetails || opts.Daily != (tc.daily != "") {
				t.Fatalf("wrong options: %+v", opts)
			}
			if _, err := os.Stat(filepath.Dir(opts.Directory)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("temporary parent remains: %v", err)
			}
		})
	}
}

func TestDatabaseWhatIfDefaultRangeAndModelFlags(t *testing.T) {
	var opts exportdata.Options
	code, out, errs := databaseRun([]string{"what-if", "--litellm-db", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5", "--format", "json", "--usage-details=false", "--database-url-file", "SENTINEL_FILE"}, func(_ context.Context, o exportdata.Options) (exportdata.Result, error) {
		opts = o
		return databaseFixture(t, o, databaseSparseRows(t, o), nil), nil
	})
	if code != 0 || errs != "" {
		t.Fatalf("exit=%d: %s", code, errs)
	}
	var report reprice.Report
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report.Covered != 2 || !report.Source.Start.Equal(opts.Start) || !report.Source.End.Equal(opts.End) || opts.End.Sub(opts.Start) != 14*24*time.Hour || opts.UsageDetails || opts.CredentialFile != "SENTINEL_FILE" {
		t.Fatalf("wrong what-if range/model options: %+v %+v", opts, report)
	}
}

func TestDatabaseWhatIfRoutingMapWithoutScope(t *testing.T) {
	path := whatIfRoutingFile(t, []map[string]any{whatIfRoutingRow("one")})
	var encoded bytes.Buffer
	if err := spend.ReadRouting(path, func(row spend.Row) error {
		return json.NewEncoder(&encoded).Encode(row.Values)
	}); err != nil {
		t.Fatal(err)
	}
	mapping := whatIfRoutingFile(t, reprice.WorkloadMap{Version: 1, Mappings: []reprice.Mapping{{Selector: reprice.Selector{Kind: "route", Value: "private-route-sentinel"}, Workload: "qa"}}})
	args := []string{"what-if", "--litellm-db", "--period", "2020-01-02", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5", "--workload-map", mapping, "--format", "json"}
	code, out, errs := databaseRun(args, func(_ context.Context, opts exportdata.Options) (exportdata.Result, error) {
		if !opts.Routing {
			t.Fatal("route workload map needs routing columns even without a scope flag")
		}
		r := databaseFixture(t, opts, nil, nil)
		r.SpendPath = filepath.Join(opts.Directory, "spend.jsonl")
		if err := os.WriteFile(r.SpendPath, encoded.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		return r, nil
	})
	var report reprice.Report
	if code != 0 || errs != "" || json.Unmarshal([]byte(out), &report) != nil || report.Unmapped != 0 || report.Source.Attribution == nil || report.Covered != 1 || len(report.Level2) != 1 || report.Level2[0].Workload != "mapped" {
		t.Fatalf("route mapping lost: exit=%d errors=%q report=%s", code, errs, out)
	}
}

func TestDatabasePeriodsUseUTCAndExplicitEnvelope(t *testing.T) {
	now := time.Date(2026, 10, 18, 0, 30, 0, 0, time.FixedZone("east", 12*3600))
	p := defaultDatabasePeriod(now)
	if p.End.Format(time.RFC3339) != "2026-10-17T00:00:00Z" || p.Start.Format(time.RFC3339) != "2026-10-03T00:00:00Z" {
		t.Fatalf("default uses local midnight: %+v", p)
	}
	p, before, after, err := databaseInvestigationPeriods("2026-10-01", "2026-10-05", now)
	if err != nil || before != "2026-10-01" || after != "2026-10-05" || p.Start.Format(time.DateOnly) != "2026-10-01" || p.End.Format(time.DateOnly) != "2026-10-06" {
		t.Fatalf("gapped period envelope is wrong: %+v %q %q %v", p, before, after, err)
	}
	if _, _, _, err := databaseInvestigationPeriods("2026-10-01", "2026-10-03T00:00:00Z/2026-10-05T00:00:00Z", now); err == nil {
		t.Fatal("unequal durations accepted")
	}
}

func TestDatabaseDefaultSessionRange(t *testing.T) {
	var opts exportdata.Options
	code, out, errs := databaseRun([]string{"investigate", "--litellm-db", "--session-budget-usd", "1", "--format", "json"}, func(_ context.Context, o exportdata.Options) (exportdata.Result, error) {
		opts = o
		return databaseFixture(t, o, databaseSparseRows(t, o), nil), nil
	})
	var report spend.SessionReport
	if code != 0 || errs != "" || json.Unmarshal([]byte(out), &report) != nil || !report.Period.Start.Equal(opts.Start) || !report.Period.End.Equal(opts.End) || report.AutomaticPeriod {
		t.Fatalf("session range inferred from file edges: exit=%d errors=%q report=%s", code, errs, out)
	}
}

func TestDatabaseStandaloneErrorsArePrivate(t *testing.T) {
	args := []string{"export", "litellm", "--out", "SENTINEL_PATH", "--database-url-file", "SENTINEL_FILE"}
	code, out, errs := databaseRun(args, func(context.Context, exportdata.Options) (exportdata.Result, error) {
		return exportdata.Result{}, errors.New("postgres://SENTINEL_USER:SENTINEL_PASSWORD@SENTINEL_HOST SENTINEL_PATH SENTINEL_FILE")
	})
	if code != 1 || out != "" || strings.Contains(errs, "SENTINEL") {
		t.Fatalf("driver error exposed: exit=%d out=%q errors=%q", code, out, errs)
	}
	var errorsOut bytes.Buffer
	code = RunWithExporter(context.Background(), args, nil, databaseBrokenWriter{}, &errorsOut, func(context.Context, exportdata.Options) (exportdata.Result, error) {
		return exportdata.Result{Rows: 1}, nil
	})
	if code != 1 || strings.Contains(errorsOut.String(), "SENTINEL") {
		t.Fatalf("writer error exposed: exit=%d %s", code, &errorsOut)
	}
}

func TestDatabaseFixedFailuresReachUser(t *testing.T) {
	for _, failure := range []exportdata.Failure{exportdata.ErrCredentials, exportdata.ErrConfiguration, exportdata.ErrTransport, exportdata.ErrOutput, exportdata.ErrSchema, exportdata.ErrLimit, exportdata.ErrInterrupted, exportdata.ErrDatabase} {
		for _, args := range [][]string{{"export", "litellm", "--out", "new"}, {"investigate", "--litellm-db"}, {"what-if", "--litellm-db", "--scenario", "../../examples/what-if/scenario.json"}} {
			code, out, errs := databaseRun(args, func(context.Context, exportdata.Options) (exportdata.Result, error) {
				return exportdata.Result{}, fmt.Errorf("SENTINEL_DRIVER_CONTEXT: %w", failure)
			})
			if code != 1 || out != "" || errs != failure.Error()+"\n" {
				t.Fatalf("fixed failure suppressed or wrapper leaked: exit=%d out=%q errors=%q", code, out, errs)
			}
		}
	}
}

func TestDatabaseURLGuardPreservesOfflineIdentityLabels(t *testing.T) {
	for _, kind := range []string{"route", "agent"} {
		row := whatIfRoutingRow("one")
		field := "original_model_group"
		if kind == "agent" {
			field = "agent_id"
		}
		label := "https://private-" + kind + "-sentinel.invalid"
		row[field] = label
		path := whatIfRoutingFile(t, []map[string]any{row})
		for _, args := range [][]string{
			append(whatIfRoutingArgs(path), "--scope-"+kind, label),
			{"investigate", "--litellm-spend", path, "--before-period", "2020-01-01", "--after-period", "2020-01-02", "--scope-" + kind, label},
		} {
			code, out, errs := databaseRun(args, func(context.Context, exportdata.Options) (exportdata.Result, error) {
				t.Fatal("offline identity label invoked exporter")
				return exportdata.Result{}, nil
			})
			if code != 0 || errs != "" || strings.Contains(out, label) {
				t.Fatalf("offline URL identity changed semantics: exit=%d out=%q errors=%q", code, out, errs)
			}
		}
	}
}

type databaseBrokenWriter struct{}

func (databaseBrokenWriter) Write([]byte) (int, error) {
	return 0, errors.New("SENTINEL_WRITE_FAILURE")
}

func TestDatabaseTemporaryCleanupAndPrivateErrors(t *testing.T) {
	for _, command := range []string{"investigate", "what-if"} {
		for _, failure := range []string{"export", "parse", "write", "cancel", "cancel-after-export"} {
			t.Run(command+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				args := []string{command, "--litellm-db"}
				if command == "what-if" {
					args = append(args, "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5")
				}
				var directory string
				exporter := func(gotCtx context.Context, o exportdata.Options) (exportdata.Result, error) {
					if gotCtx != ctx {
						t.Fatal("context was not propagated")
					}
					directory = o.Directory
					contents := databaseSparseRows(t, o)
					if failure == "parse" {
						contents = []byte("SENTINEL_INVALID_FILE")
					}
					r := databaseFixture(t, o, contents, nil)
					if failure == "cancel" {
						cancel()
						return r, ctx.Err()
					}
					if failure == "cancel-after-export" {
						cancel()
					}
					if failure == "export" {
						return r, errors.New("postgres://SENTINEL_USER:SENTINEL_PASSWORD@SENTINEL_HOST " + o.Directory)
					}
					return r, nil
				}
				var out, errs bytes.Buffer
				var writer io.Writer = &out
				if failure == "write" {
					writer = databaseBrokenWriter{}
				}
				code := RunWithExporter(ctx, args, strings.NewReader(""), writer, &errs, exporter)
				if code != 1 || out.Len() != 0 || strings.Contains(errs.String(), "SENTINEL") || strings.Contains(errs.String(), directory) {
					t.Fatalf("exit=%d out=%q errors=%q", code, &out, &errs)
				}
				if _, err := os.Stat(filepath.Dir(directory)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("temporary parent remains after %s: %v", failure, err)
				}
			})
		}
	}
}

func TestDatabaseKeepExport(t *testing.T) {
	for _, failure := range []bool{false, true} {
		directory := filepath.Join(t.TempDir(), "SENTINEL_PRIVATE_EXPORT")
		code, out, errs := databaseRun([]string{"investigate", "--litellm-db", "--keep-export", directory}, func(_ context.Context, o exportdata.Options) (exportdata.Result, error) {
			if o.Directory != directory {
				t.Fatal("keep destination not honored")
			}
			contents := databaseSparseRows(t, o)
			if failure {
				contents = []byte("SENTINEL_INVALID_CSV")
			}
			return databaseFixture(t, o, contents, nil), nil
		})
		if code != map[bool]int{false: 0, true: 1}[failure] || strings.Contains(out+errs, "SENTINEL") {
			t.Fatalf("exit=%d out=%q errors=%q", code, out, errs)
		}
		if _, err := os.Stat(filepath.Join(directory, "spend.csv")); err != nil {
			t.Fatalf("explicitly retained export removed: %v", err)
		}
	}
}

func TestDatabaseCanceledBeforeExport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, args := range [][]string{{"export", "litellm", "--out", "new"}, {"investigate", "--litellm-db"}, {"what-if", "--litellm-db", "--scenario", "../../examples/what-if/scenario.json"}} {
		var out, errs bytes.Buffer
		code := RunWithExporter(ctx, args, nil, &out, &errs, func(context.Context, exportdata.Options) (exportdata.Result, error) {
			t.Fatal("canceled request reached exporter")
			return exportdata.Result{}, nil
		})
		if code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "canceled") {
			t.Fatalf("exit=%d out=%q errors=%q", code, &out, &errs)
		}
	}
}
