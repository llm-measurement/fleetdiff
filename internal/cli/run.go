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
	"runtime"
	"runtime/debug"
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

const help = `fleetdiff compares local agent-fleet measurements. It does not enforce policy.

Usage:
  fleetdiff compare --before PATH --after PATH --expected PRODUCER,PRODUCER [options]

PATH is one canonical summary JSON file or a directory of summary JSON files.
Expected producers must observe disjoint requests and be supplied from trusted inventory.
Names in the report are aliases; producer-N refers to the sorted expected list.

Options:
  --before-window RFC3339     Select one processing-time window from the before input
  --after-window RFC3339      Select one processing-time window from the after input
  --format text|json         Output format (default text)
  --top N                    At most 1-100 tracked movers per sketch (default 20)
  --allow-partial            Permit missing producers or incomplete observation intervals
  --show-hashes              Include pseudonymous, linkable hashes in output
  --help                    Show this help
  --version                 Print version, revision, Go toolchain, and platform

No account, network access, or hashing secret is needed.
No files are modified. Default output omits paths, producer metadata, and hashes.
Exit status: 0 report/help, 1 input/comparison/output error, 2 invalid command/options.
`

func Run(args []string, out, errout io.Writer) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
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
	if args[0] != "compare" {
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
	partial := flags.Bool("allow-partial", false, "")
	hashes := flags.Bool("show-hashes", false, "")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := io.WriteString(out, help); err != nil {
				return fail(1, "cannot write help")
			}
			return 0
		}
		return fail(2, "invalid compare options; run fleetdiff compare --help")
	}
	if flags.NArg() != 0 || *before == "" || *after == "" || *expected == "" || len(*expected) > 128*129 || *top < 1 || *top > 100 || (*format != "text" && *format != "json") {
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
	r, err := compare.Compare(a, b, compare.Options{Expected: producers, Top: *top, AllowPartial: *partial, ShowHashes: *hashes})
	if err != nil {
		return fail(1, err.Error())
	}
	var buffer bytes.Buffer
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
			fmt.Fprintf(out, "  Token coverage: %d complete, %d missing, %d observed model requests\n", w.Usage.Complete, w.Usage.Missing, w.Usage.Requests)
		}
	}
	fmt.Fprintln(out, "\nCounters (before -> after; observed delta)")
	for _, c := range r.Counters {
		fmt.Fprintf(out, "  %s: %d -> %d; %+d [%s]\n", c.Name, c.Before, c.After, c.Delta, c.Unit)
	}
	if len(r.Distinct) > 0 {
		fmt.Fprintln(out, "\nDistinct activity (estimates; nominal statistical RSE, not hard bounds)")
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
	for _, note := range r.Notes {
		fmt.Fprintln(out, "- "+note)
	}
}
