// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package cli renders local comparison reports without model or network clients.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/attribution"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/exportdata"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

// Release stamps take precedence over Go's embedded module version.
var Version = "dev"
var Revision = "unknown"

func buildVersion(info *debug.BuildInfo) string {
	if Version != "dev" || info == nil || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return Version
	}
	return info.Main.Version
}

const help = `fleetdiff shows what changed between two windows of agent measurements.
Local and read-only.

Usage:
  fleetdiff export litellm --out NEW_DIRECTORY [options]
  fleetdiff investigate --litellm-db [--daily] [--routing-audit] [options]
  fleetdiff what-if --litellm-db --from MODEL --to MODEL --fraction 0.5 [options]
  fleetdiff inspect [options] CAPTURE
  fleetdiff diagnose [options] CONFIG
  fleetdiff scan [options] PATH --expected PRODUCER[,PRODUCER]
  fleetdiff investigate --before PATH --after PATH --expected PRODUCER[,PRODUCER] [options]
  fleetdiff investigate --litellm-spend FILE [--before-period DATE --after-period DATE] [options]
  fleetdiff investigate --litellm-spend FILE --session-budget-usd USD [--period DATE]
  fleetdiff compare --before PATH --after PATH --expected PRODUCER,PRODUCER [options]

PATH is one canonical summary JSON file or a directory of summary JSON files.
Expected producers are the producer_id values configured by your summary collectors.
Supply them from trusted inventory; they must observe disjoint requests.
Private names use aliases; producer-N refers to the sorted expected list.
Spend model names print only on an exact match to the bundled public price catalog.

Options:
  --before-window RFC3339     Select one processing-time window from the before input
  --after-window RFC3339      Select one processing-time window from the after input
  --format text|json         Output format (default text)
  --top N                    At most 1-100 tracked movers per sketch (default 20)
  --flag-share DECIMAL       Session after lower-share threshold, 0..1 (default 0.25)
  --allow-partial            Permit missing producers or incomplete observation intervals
  --show-hashes              Include pseudonymous, linkable hashes in output
  --litellm-spend FILE        Read a request-level SQL export: CSV, JSON array or JSONL
  --litellm-db                Export a private database snapshot, then analyze locally
  --database-url-file FILE    Read database credentials from an owner-only regular file
  --keep-export NEW_DIRECTORY Keep the private export; otherwise remove it after analysis
  --daily                    Export daily counters for database row coverage
  --routing-details          Include routing details in the database export
  --usage-details=false      Omit default-on cache and usage details from the export
  --litellm-daily-spend FILE  Compare row counts with the daily SQL-projection CSV
  --provider-daily-counts FILE  Compare local provider daily aggregate counts
  --provider-model-map FILE     Explicit source/target provider-model CSV
  --provider-counts-contract FILE  UTC scope and primary counting-basis JSON
  --before-period PERIOD     UTC date or RFC3339 start/end (requires after-period)
  --after-period PERIOD      UTC date or RFC3339 start/end (equal duration)
  --group-by KIND            key (default), team, user, end-user or session
  --routing-audit            Show original-route distributions and attribution coverage
  --routing-by KIND          route (default) or route-agent; requires routing-audit
  --show-labels              Include route/agent configuration labels; requires routing-audit
  --scope-route ID           Investigate one recorded original route (explicit periods)
  --scope-agent ID           Investigate one recorded agent (explicit periods)
  --period PERIOD            Single-period session review: UTC date or RFC3339 start/end
  --session-budget-usd USD    Review observed session totals against a supplied cap
  --session-counter-window DURATION  Compare positive-spend spans with this duration (default 1h)
  --hash-secret-env NAME     Use an existing secret for repeat spend comparisons
  --help                    Show this help
  --version                 Print version, revision, Go toolchain, and platform

inspect reads local OTLP captures; run fleetdiff inspect --help for capture options.
diagnose checks supported collector configuration; scan checks retained summary history.
File commands run offline. Only explicit database commands connect to Postgres.
Database periods default to the last 14 complete UTC days; export --help has details.
Default output uses aliases for identities.
Spend periods default to the latest two complete 7-day UTC intervals in the file.
With fewer than 14 complete days, supply both period flags. Daily UI aggregates
need a request-level SQL export; see examples/litellm-spend/README.md.
Exit status: 0 report/help, 1 input/comparison/output error, 2 invalid command/options.
diagnose and scan additionally use 3 for findings and 4 for unavailable evidence.
`

func Run(args []string, out, errout io.Writer) int {
	return RunWithInput(args, os.Stdin, out, errout)
}

// RunWithInput makes stdin explicit for callers embedding the command.
func RunWithInput(args []string, in io.Reader, out, errout io.Writer) int {
	return RunWithExporter(context.Background(), args, in, out, errout, nil)
}

// RunWithExporter injects the only database capability. Run and RunWithInput
// remain offline, including when their callers supply database options.
func RunWithExporter(ctx context.Context, args []string, in io.Reader, out, errout io.Writer, exporter exportdata.Exporter) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
	if len(args) > 0 && args[0] == "export" {
		return runExport(ctx, args[1:], out, errout, exporter)
	}
	if len(args) > 0 && args[0] == "what-if" {
		return runWhatIf(ctx, args[1:], out, errout, exporter)
	}
	if len(args) > 0 && args[0] == "inspect" {
		return runInspect(args[1:], in, out, errout)
	}
	if len(args) > 0 && args[0] == "diagnose" {
		return runDiagnose(args[1:], in, out, errout)
	}
	if len(args) > 0 && args[0] == "scan" {
		return runScan(args[1:], out, errout)
	}
	if len(args) == 1 && args[0] == "--version" {
		info, _ := debug.ReadBuildInfo()
		if _, err := fmt.Fprintf(out, "fleetdiff %s (%s) %s %s/%s\n", buildVersion(info), Revision, runtime.Version(), runtime.GOOS, runtime.GOARCH); err != nil {
			return fail(1, "cannot write version")
		}
		return 0
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		if _, err := io.WriteString(out, help); err != nil {
			return fail(1, "cannot write help")
		}
		return 0
	}
	if args[0] != "compare" && args[0] != "investigate" {
		return fail(2, "unknown command; run fleetdiff --help")
	}
	flags := flag.NewFlagSet("compare", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var database databaseFlags
	database.register(flags)
	before := flags.String("before", "", "")
	after := flags.String("after", "", "")
	expected := flags.String("expected", "", "")
	format := flags.String("format", "text", "")
	beforeWindow := flags.String("before-window", "", "")
	afterWindow := flags.String("after-window", "", "")
	top := flags.Int("top", 20, "")
	flagShareText := flags.String("flag-share", strconv.FormatFloat(compare.DefaultFlagShare, 'f', -1, 64), "")
	partial := flags.Bool("allow-partial", false, "")
	hashes := flags.Bool("show-hashes", false, "")
	spendPath := flags.String("litellm-spend", "", "")
	langfusePath := flags.String("langfuse-export", "", "")
	langfuseTag := flags.String("langfuse-tag", "", "")
	dailySpendPath := flags.String("litellm-daily-spend", "", "")
	providerCountsPath := flags.String("provider-daily-counts", "", "")
	providerModelMapPath := flags.String("provider-model-map", "", "")
	providerContractPath := flags.String("provider-counts-contract", "", "")
	beforePeriod := flags.String("before-period", "", "")
	afterPeriod := flags.String("after-period", "", "")
	group := flags.String("group-by", "key", "")
	secret := flags.String("hash-secret-env", "", "")
	period := flags.String("period", "", "")
	budget := flags.String("session-budget-usd", "", "")
	counterWindow := flags.Duration("session-counter-window", time.Hour, "")
	routingAudit := flags.Bool("routing-audit", false, "")
	routingBy := flags.String("routing-by", "", "")
	showLabels := flags.Bool("show-labels", false, "")
	var scopeRoute, scopeAgent string
	var routeSet, agentSet bool
	flags.Func("scope-route", "", func(value string) error {
		if routeSet {
			return errors.New("scope option cannot be repeated")
		}
		routeSet, scopeRoute = true, value
		return nil
	})
	flags.Func("scope-agent", "", func(value string) error {
		if agentSet {
			return errors.New("scope option cannot be repeated")
		}
		agentSet, scopeAgent = true, value
		return nil
	})
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := io.WriteString(out, help); err != nil {
				return fail(1, "cannot write help")
			}
			return 0
		}
		for _, name := range []string{"before", "after", "expected", "format", "before-window", "after-window", "top", "flag-share"} {
			if err.Error() == "flag needs an argument: -"+name {
				return fail(2, "missing value for --"+name+"; run fleetdiff --help")
			}
		}
		return fail(2, "invalid compare options; run fleetdiff compare --help")
	}
	if database.enabled && databaseURLArgument(args) {
		return fail(2, databaseURLMessage)
	}
	if err := database.validate(flags); err != nil {
		return fail(2, err.Error())
	}
	spendMode, incompatible := database.enabled, false
	sessionMode, windowSet, emptySessionOption, twoPeriods := false, false, false, false
	groupExplicit := false
	emptyDailyOption := false
	providerSet, emptyProviderOption := false, false
	routingSet, routingBySet := false, false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "litellm-db", "keep-export", "database-url-file", "daily", "routing-details", "usage-details":
		case "routing-audit", "routing-by", "scope-route", "scope-agent", "show-labels":
			spendMode, routingSet = true, true
			routingBySet = routingBySet || f.Name == "routing-by"
		case "litellm-daily-spend":
			spendMode = true
			emptyDailyOption = f.Value.String() == ""
		case "provider-daily-counts", "provider-model-map", "provider-counts-contract":
			spendMode, providerSet = true, true
			emptyProviderOption = emptyProviderOption || f.Value.String() == ""
		case "period", "session-budget-usd":
			spendMode, sessionMode = true, true
			emptySessionOption = emptySessionOption || f.Value.String() == ""
		case "session-counter-window":
			spendMode, windowSet = true, true
		case "group-by":
			spendMode, groupExplicit = true, true
		case "litellm-spend", "langfuse-export", "langfuse-tag", "before-period", "after-period", "hash-secret-env":
			spendMode = true
		case "format", "top", "show-hashes":
		default:
			incompatible = true
		}
		if f.Name == "before-period" || f.Name == "after-period" {
			twoPeriods = true
		}
	})
	if spendMode {
		langfuseSet := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "langfuse-export" || f.Name == "langfuse-tag" {
				langfuseSet = true
			}
		})
		if langfuseSet {
			if args[0] != "investigate" || *langfusePath == "" || *spendPath != "" || database.enabled || *dailySpendPath != "" || emptyDailyOption || incompatible || sessionMode || windowSet || routingSet || providerSet || flags.NArg() != 0 {
				return fail(2, "use investigate --langfuse-export DIR with two-period options only; do not combine sources, SQL coverage, provider counts, routing or session-budget options")
			}
			if !groupExplicit {
				*group = "user_id"
			}
			return runLangfuse(*langfusePath, *group, *langfuseTag, *format, spend.Options{BeforePeriod: *beforePeriod, AfterPeriod: *afterPeriod, SecretEnv: *secret, Top: *top, ShowHashes: *hashes}, out, errout)
		}
		if providerSet && (emptyProviderOption || *providerCountsPath == "" || sessionMode || windowSet || routeSet || agentSet) {
			return fail(2, "provider coverage needs a nonempty aggregate CSV and an unscoped two-period spend investigation")
		}
		if emptyDailyOption {
			return fail(2, "litellm-daily-spend needs a daily SQL-projection CSV file")
		}
		if args[0] != "investigate" || *spendPath == "" && !database.enabled || incompatible || flags.NArg() != 0 {
			return fail(2, "use investigate --litellm-spend FILE with period flags, not summary paths or expected producers")
		}
		if routingSet {
			if sessionMode || windowSet {
				return fail(2, "routing options require a two-period spend investigation, not a session-budget review")
			}
			if routingBySet && (!*routingAudit || *routingBy != "route" && *routingBy != "route-agent") {
				return fail(2, "routing-by must be route or route-agent and requires routing-audit")
			}
			if *showLabels && !*routingAudit {
				return fail(2, "show-labels requires routing-audit")
			}
			if routeSet && agentSet || routeSet && (strings.TrimSpace(scopeRoute) == "" || len(scopeRoute) > spend.MaxFieldBytes || database.enabled && !utf8.ValidString(scopeRoute)) || agentSet && (strings.TrimSpace(scopeAgent) == "" || len(scopeAgent) > spend.MaxFieldBytes || database.enabled && !utf8.ValidString(scopeAgent)) {
				return fail(2, "choose one nonempty route or agent scope of at most 8192 bytes")
			}
			if routeSet || agentSet {
				if *beforePeriod == "" || *afterPeriod == "" || *dailySpendPath != "" || database.daily || *group == "session" {
					return fail(2, "route or agent scopes require both explicit periods; do not combine with daily coverage or session grouping")
				}
			}
		}
		if sessionMode || windowSet {
			if !sessionMode || groupExplicit && *group != "session" || twoPeriods || emptySessionOption || *counterWindow <= 0 {
				return fail(2, "session review needs --period or --session-budget-usd; grouping is session; do not combine with other groups or two-period flags")
			}
		}
		if database.enabled {
			if *format != "text" && *format != "json" || *top < 1 || *top > 100 || !slices.Contains([]string{"key", "team", "user", "end-user", "session"}, *group) {
				return fail(2, "use text or json output, top between 1 and 100, and a supported spend group")
			}
			key, _, err := attribution.Secret(*secret)
			clear(key)
			if err != nil {
				return fail(2, err.Error())
			}
			var p spend.Period
			if sessionMode || windowSet {
				if *counterWindow < time.Second || *counterWindow > 365*24*time.Hour {
					return fail(2, "session-counter-window must be between 1s and 8760h")
				}
				if *budget != "" {
					cap, ok := accounting.Cost(*budget)
					if !ok || cap.Sign() <= 0 {
						return fail(2, "session-budget-usd must be a positive decimal no greater than 1e18")
					}
				}
				p, err = databasePeriod(*period, time.Now())
				*period = periodText(p)
			} else {
				if twoPeriods && (*beforePeriod == "" || *afterPeriod == "") {
					return fail(2, "supply both nonempty before-period and after-period")
				}
				p, *beforePeriod, *afterPeriod, err = databaseInvestigationPeriods(*beforePeriod, *afterPeriod, time.Now())
			}
			if err != nil {
				return fail(2, err.Error())
			}
			if err := validateDatabaseDaily(p, database.daily); err != nil {
				return fail(2, err.Error())
			}
			r, cleanup, err := database.export(ctx, p, exporter)
			defer cleanup()
			if err != nil {
				return fail(1, err.Error())
			}
			*spendPath, *dailySpendPath = r.SpendPath, r.DailyPath
		}
		if sessionMode || windowSet {
			return runSessionBudget(*spendPath, *period, *budget, *secret, *format, *dailySpendPath, *top, *hashes, *counterWindow, out, errout)
		}
		return runSpend(*spendPath, *format, spend.Options{
			BeforePeriod: *beforePeriod, AfterPeriod: *afterPeriod, GroupBy: *group,
			SecretEnv: *secret, Top: *top, ShowHashes: *hashes, DailySpendPath: *dailySpendPath,
			RoutingAudit: *routingAudit, RoutingBy: *routingBy, ScopeRoute: scopeRoute, ScopeAgent: scopeAgent,
			ShowLabels:         *showLabels,
			ProviderCountsPath: *providerCountsPath, ProviderModelMapPath: *providerModelMapPath,
			ProviderContractPath: *providerContractPath,
		}, out, errout)
	}
	for _, required := range []struct{ name, value string }{{"before", *before}, {"after", *after}, {"expected", *expected}} {
		if strings.TrimSpace(required.value) == "" {
			return fail(2, "missing required flag --"+required.name+"; run fleetdiff --help")
		}
	}
	flagShare, shareErr := strconv.ParseFloat(*flagShareText, 64)
	if shareErr != nil || strings.ContainsAny(*flagShareText, "xXpP") || math.IsNaN(flagShare) || math.IsInf(flagShare, 0) || flagShare < 0 || flagShare > 1 {
		return fail(2, "flag-share must be a finite decimal between 0 and 1")
	}
	if flags.NArg() != 0 || len(*expected) > 128*129 || *top < 1 || *top > 100 || (*format != "text" && *format != "json") {
		return fail(2, "supply before, after, expected producers, a supported format, and top between 1 and 100")
	}
	bs, err := parseWindow(*beforeWindow)
	if err != nil {
		return fail(2, "before-window must be a nonnegative RFC3339 timestamp within nanosecond range")
	}
	as, err := parseWindow(*afterWindow)
	if err != nil {
		return fail(2, "after-window must be a nonnegative RFC3339 timestamp within nanosecond range")
	}
	a, err := compare.ReadWindow(*before, bs)
	if err != nil {
		return fail(1, "before: "+err.Error())
	}
	b, err := compare.ReadWindow(*after, as)
	if err != nil {
		return fail(1, "after: "+err.Error())
	}
	producers := strings.Split(*expected, ",")
	for i := range producers {
		producers[i] = strings.TrimSpace(producers[i])
	}
	var buffer bytes.Buffer
	if args[0] == "investigate" {
		investigation, err := compare.Investigate(a, b, compare.Options{Expected: producers, Top: *top, AllowPartial: *partial, ShowHashes: *hashes, FlagShare: flagShare, FlagShareSet: true})
		if err != nil {
			return fail(1, err.Error())
		}
		if *format == "json" {
			encoder := json.NewEncoder(&buffer)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(investigation); err != nil {
				return fail(1, "cannot encode investigation")
			}
		} else {
			renderInvestigation(&buffer, investigation, flagShare)
		}
		if _, err := io.Copy(out, &buffer); err != nil {
			return fail(1, "cannot write investigation")
		}
		return 0
	}
	r, err := compare.Compare(a, b, compare.Options{Expected: producers, Top: *top, AllowPartial: *partial, ShowHashes: *hashes, FlagShare: flagShare, FlagShareSet: true})
	if err != nil {
		return fail(1, err.Error())
	}
	if *format == "json" {
		encoder := json.NewEncoder(&buffer)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(r); err != nil {
			return fail(1, "cannot encode comparison")
		}
	} else {
		renderText(&buffer, r)
	}
	if _, err := io.Copy(out, &buffer); err != nil {
		return fail(1, "cannot write comparison")
	}
	return 0
}

func parseWindow(value string) (*int64, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, errors.New("invalid timestamp")
	}
	n := t.UnixNano()
	if n < 0 || !time.Unix(0, n).Equal(t) {
		return nil, errors.New("timestamp outside supported range")
	}
	return &n, nil
}

func renderText(out io.Writer, r compare.Report) {
	fmt.Fprintln(out, "Fleet comparison (observed measurements)")
	for _, entry := range []struct {
		name   string
		window compare.Window
	}{{"Before", r.Before}, {"After", r.After}} {
		w := entry.window
		fmt.Fprintf(out, "%s: %s for %s; %d selected snapshots\n", entry.name, time.Unix(0, w.Start).UTC().Format(time.RFC3339Nano), time.Duration(w.Duration), w.SelectedSnapshots)
		fmt.Fprintf(out, "  Missing producers: %v; partial producers: %v\n", w.MissingProducers, w.PartialProducers)
		if w.Usage != nil {
			fmt.Fprintf(out, "  Token coverage: %d complete, %d missing, %d observed model attempts\n", w.Usage.Complete, w.Usage.Missing, w.Usage.Requests)
		}
	}
	fmt.Fprintln(out, "\nCounters (before -> after; observed delta)")
	for _, c := range r.Counters {
		fmt.Fprintf(out, "  %s: %d -> %d; %+d [%s]\n", c.Name, c.Before, c.After, c.Delta, c.Unit)
	}
	if len(r.Distinct) > 0 {
		fmt.Fprintln(out, "\nDistinct activity (estimates with nominal statistical RSE)")
	}
	for _, d := range r.Distinct {
		fmt.Fprintf(out, "  %s: %.2f -> %.2f; estimated delta %+.2f; nominal RSE %.2f%% / %.2f%%\n", d.Name, d.Before.Estimate, d.After.Estimate, d.EstimatedDelta, d.Before.NominalRSE*100, d.After.NominalRSE*100)
	}
	for _, c := range r.Concentration {
		fmt.Fprintf(out, "\n%s: %d -> %d [%s]; %d of %d candidates shown\n", c.Name, c.BeforeWeight, c.AfterWeight, c.WeightUnit, len(c.Movers), c.CandidateCount)
		fmt.Fprintf(out, "  Any key outside the candidate set has observed delta within [%d, %d].\n", c.UntrackedDelta.Lower, c.UntrackedDelta.Upper)
		for _, m := range c.Movers {
			label := m.Item
			if m.Hash != "" {
				label += " (" + m.Hash + ")"
			}
			fmt.Fprintf(out, "  %s: [%d, %d] -> [%d, %d]; delta [%+d, %+d] %s\n", label, m.Before.Lower, m.Before.Upper, m.After.Lower, m.After.Upper, m.Delta.Lower, m.Delta.Upper, m.Direction)
		}
	}
	fmt.Fprintf(out, "\nMeasurements omitted by the output allowlist: %d\n", r.OmittedMeasurements)
	if len(r.DroppedMeasurements) != 0 {
		fmt.Fprintf(out, "Optional attribution absent from some snapshots: %s\n", strings.Join(r.DroppedMeasurements, ", "))
	}
	for _, note := range r.Notes {
		fmt.Fprintln(out, "- "+note)
	}
}
