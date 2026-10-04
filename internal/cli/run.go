// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package cli renders local comparison reports without model or network clients.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
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
  fleetdiff inspect [options] CAPTURE
  fleetdiff diagnose [options] CONFIG
  fleetdiff scan [options] PATH --expected PRODUCER[,PRODUCER]
  fleetdiff investigate --before PATH --after PATH --expected PRODUCER[,PRODUCER] [options]
  fleetdiff compare --before PATH --after PATH --expected PRODUCER,PRODUCER [options]

PATH is one canonical summary JSON file or a directory of summary JSON files.
Expected producers are the producer_id values configured by your summary collectors.
Supply them from trusted inventory; they must observe disjoint requests.
Names in the report are aliases; producer-N refers to the sorted expected list.

Options:
  --before-window RFC3339     Select one processing-time window from the before input
  --after-window RFC3339      Select one processing-time window from the after input
  --format text|json         Output format (default text)
  --top N                    At most 1-100 tracked movers per sketch (default 20)
  --flag-share DECIMAL       Session after lower-share threshold, 0..1 (default 0.25)
  --allow-partial            Permit missing producers or incomplete observation intervals
  --show-hashes              Include pseudonymous, linkable hashes in output
  --help                    Show this help
  --version                 Print version, revision, Go toolchain, and platform

inspect reads local OTLP captures; run fleetdiff inspect --help for capture options.
diagnose checks supported collector configuration; scan checks retained summary history.
Runs offline using local files. Default output uses aliases for identities.
Exit status: 0 report/help, 1 input/comparison/output error, 2 invalid command/options.
diagnose and scan additionally use 3 for findings and 4 for unavailable evidence.
`

func Run(args []string, out, errout io.Writer) int {
	return RunWithInput(args, os.Stdin, out, errout)
}

// RunWithInput makes stdin explicit for callers embedding the command.
func RunWithInput(args []string, in io.Reader, out, errout io.Writer) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
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
