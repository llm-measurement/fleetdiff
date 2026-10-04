// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

const scanHelp = `fleetdiff scan checks recent summary windows against preceding history.

Usage: fleetdiff scan [options] PATH
       fleetdiff scan PATH [options]

  --expected PRODUCER[,PRODUCER]  Required trusted, disjoint producers
  --baseline N|DURATION           Preceding history (default 24 windows, minimum 6)
  --recent N                      Windows to report (default 1, maximum 128)
  --persist N                     Consecutive unusual windows required (default 1)
  --min-attempts N                Model-attempt volume guard (default 100)
  --z N                           Robust-score threshold (default 4)
  --relative-change DECIMAL       Minimum relative change (default 0.5)
  --share-floor DECIMAL           Minimum contributor share (default 0.25)
  --share-change DECIMAL          Minimum share increase (default 0.10)
  --coverage-change DECIMAL       Minimum missing-usage increase (default 0.05)
  --max-age DURATION              Latest closed-window age (default 3 windows)
  --as-of RFC3339                 Explicit reference time for historical scans
  --top N                         Findings displayed per window (default 20)
  --format text|json              Output format (default text)
  --show-hashes                   Include pseudonymous, linkable hashes

Reads canonical summary files, not raw traces. No network requests or file writes.
Missing history is not a quiet result. Unknown usage is never converted to savings.
Exit: 0 evaluated/quiet, 3 unusual, 4 not ready or limited, 1 input/output, 2 options.
Cron needs a wrapper that handles the exit code; scan does not send notifications.
`

func runScan(args []string, out, errout io.Writer) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
	o := compare.DefaultScanOptions()
	f := flag.NewFlagSet("scan", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	expected := f.String("expected", "", "")
	baseline := f.String("baseline", "24", "")
	format := f.String("format", "text", "")
	asOf := f.String("as-of", "", "")
	maxAge := f.String("max-age", "", "")
	f.IntVar(&o.Recent, "recent", o.Recent, "")
	f.IntVar(&o.Persist, "persist", o.Persist, "")
	f.IntVar(&o.Top, "top", o.Top, "")
	f.Uint64Var(&o.MinAttempts, "min-attempts", o.MinAttempts, "")
	f.Float64Var(&o.Z, "z", o.Z, "")
	f.Float64Var(&o.RelativeChange, "relative-change", o.RelativeChange, "")
	f.Float64Var(&o.ShareFloor, "share-floor", o.ShareFloor, "")
	f.Float64Var(&o.ShareChange, "share-change", o.ShareChange, "")
	f.Float64Var(&o.CoverageChange, "coverage-change", o.CoverageChange, "")
	f.BoolVar(&o.ShowHashes, "show-hashes", false, "")
	// Permit the documented path-first spelling without accepting flags after
	// arbitrary positional arguments or echoing any of them in diagnostics.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := io.WriteString(out, scanHelp); err != nil {
				return fail(1, "cannot write help")
			}
			return 0
		}
		return fail(2, "invalid scan options; run fleetdiff scan --help")
	}
	if f.NArg() != 1 || strings.TrimSpace(*expected) == "" || len(*expected) > 128*129 || (*format != "text" && *format != "json") {
		return fail(2, "supply one summary path, expected producers, and text or json format")
	}
	o.Expected = strings.Split(*expected, ",")
	for i := range o.Expected {
		o.Expected[i] = strings.TrimSpace(o.Expected[i])
	}
	if count, err := strconv.Atoi(*baseline); err == nil {
		o.Baseline = count
	} else {
		d, err := time.ParseDuration(*baseline)
		if err != nil || d <= 0 {
			return fail(2, "baseline must be a window count or positive duration")
		}
		o.BaselineDuration = int64(d)
	}
	o.Now = time.Now().UnixNano()
	if *asOf != "" {
		t, err := parseWindow(*asOf)
		if err != nil {
			return fail(2, "as-of must be a nonnegative RFC3339 timestamp")
		}
		o.Now = *t
	}
	if *maxAge != "" {
		d, err := time.ParseDuration(*maxAge)
		if err != nil || d <= 0 {
			return fail(2, "max-age must be a positive duration")
		}
		o.MaxAge = int64(d)
	}
	if err := compare.ValidateScanOptions(o); err != nil {
		return fail(2, err.Error())
	}
	input, err := compare.ReadSeries(f.Arg(0))
	if err != nil {
		return fail(1, err.Error())
	}
	r, err := compare.Scan(input, o)
	if err != nil {
		return fail(1, err.Error())
	}
	var buffer bytes.Buffer
	if *format == "json" {
		e := json.NewEncoder(&buffer)
		e.SetIndent("", "  ")
		if err := e.Encode(r); err != nil {
			return fail(1, "cannot encode scan")
		}
	} else {
		renderScan(&buffer, r)
	}
	if _, err := io.Copy(out, &buffer); err != nil {
		return fail(1, "cannot write scan")
	}
	if r.UnusualWindows > 0 {
		return 3
	}
	if r.Status != "evaluated" {
		return 4
	}
	return 0
}

func renderScan(out io.Writer, r compare.ScanReport) {
	switch {
	case r.UnusualWindows > 0:
		fmt.Fprintf(out, "%d unusual windows among %d checked.\n", r.UnusualWindows, len(r.Windows))
	case r.Status != "evaluated":
		fmt.Fprintln(out, "Scan needs more evidence; this is not a quiet result.")
	default:
		fmt.Fprintf(out, "No unusual windows among %d checked, for the available signals.\n", len(r.Windows))
	}
	if r.Status == "not_ready" && len(r.Notes) > 0 {
		fmt.Fprintln(out, r.Notes[len(r.Notes)-1])
	}
	for _, w := range r.Windows {
		fmt.Fprintf(out, "\n%s\n", time.Unix(0, w.Start).UTC().Format(time.RFC3339Nano))
		for _, f := range w.Findings {
			label := scanSignalLabel(f.Signal)
			if f.Item != "" {
				label = f.Item + " " + label
			}
			if f.Hash != "" {
				label += " (" + f.Hash + ")"
			}
			if f.Current == nil {
				fmt.Fprintf(out, "  %s: unavailable.\n", label)
				continue
			}
			if f.Signal == "top_share" || f.Signal == "newly_prominent" {
				unit := "tokens"
				if strings.HasSuffix(f.Measurement, "_requests") {
					unit = "model attempts"
				} else if f.Measurement == "top_prompts" {
					unit = "configured prompt weight"
				}
				item := f.Item
				if item == "" {
					item = "Contributor"
				}
				if f.Hash != "" {
					item += " (" + f.Hash + ")"
				}
				percent := shareText(&compare.Share{Lower: f.Current.Lower, Upper: f.Current.Upper}, f.WeightBounds)
				if !strings.HasPrefix(percent, "[") && strings.HasSuffix(percent, ".00%") {
					percent = strings.TrimSuffix(percent, ".00%") + "%"
				}
				description := "higher share in this scan"
				if f.Signal == "newly_prominent" {
					description = "newly prominent in this scan"
				}
				fmt.Fprintf(out, "  %s now holds %s of %s (%s).\n", item, percent, unit, description)
			} else if f.Signal == "missing_usage_share" {
				fmt.Fprintf(out, "  Missing usage: %s of model attempts; typical %.2f%% (coverage change, not lower usage).\n", shareText(&compare.Share{Lower: f.Current.Lower, Upper: f.Current.Upper}, f.WeightBounds), 100*f.Median)
			} else if f.Signal == "tool_error_surge" {
				count := fmt.Sprintf("[%.0f, %.0f]", f.Current.Lower, f.Current.Upper)
				if f.Current.Lower == f.Current.Upper {
					count = fmt.Sprintf("%.0f", f.Current.Lower)
				}
				if f.WeightBounds != nil {
					count = intervalText(*f.WeightBounds)
				}
				fmt.Fprintf(out, "  %s: %s events; typical upper bound %.0f.\n", label, count, f.Median)
			} else {
				fmt.Fprintf(out, "  %s: %.2f; typical %.2f", label, f.Current.Lower, f.Median)
				if f.Median > 0 {
					fmt.Fprintf(out, " (%.2fx)", f.Current.Lower/f.Median)
				}
				fmt.Fprintln(out, ".")
			}
		}
		if w.OmittedFindings > 0 {
			fmt.Fprintf(out, "  %d additional findings; increase --top to display them.\n", w.OmittedFindings)
		}
		if w.PendingPersistence > 0 {
			fmt.Fprintf(out, "  %d changes await the configured persistence.\n", w.PendingPersistence)
		}
		if w.Status == "not_ready" {
			fmt.Fprintln(out, "  Not enough evidence to assess this window.")
		} else if w.Status != "evaluated" {
			fmt.Fprintln(out, "  Coverage is incomplete for some signals.")
		} else if len(w.Findings) == 0 && w.PendingPersistence == 0 {
			fmt.Fprintln(out, "  No unusual changes in the available signals.")
		}
	}
	fmt.Fprintf(out, "\nBaseline: %d of %d requested windows", r.BaselineWindows, r.RequestedBaseline)
	if r.BaselineWindows > 0 && len(r.Windows) > 0 {
		duration := r.Windows[0].End - r.Windows[0].Start
		fmt.Fprintf(out, ", %s to %s (window starts)", time.Unix(0, r.BaselineStart).UTC().Format(time.RFC3339Nano), time.Unix(0, r.BaselineEnd-duration).UTC().Format(time.RFC3339Nano))
	}
	fmt.Fprintln(out, ".")
	fmt.Fprintf(out, "Thresholds: robust score %.1f, relative change %.0f%%, persistence %d window(s).\n", r.Thresholds.Z, 100*r.Thresholds.RelativeChange, r.Thresholds.Persist)
	fmt.Fprintln(out, "Notes: Shares use ranked activity, not all traffic. Newly prominent does not mean a new identity. Use --format json for full details.")
}

func scanSignalLabel(name string) string {
	switch name {
	case "attempt_volume":
		return "model attempts"
	case "tokens_per_attempt":
		return "reported tokens per attempt"
	case "missing_usage_share":
		return "missing usage"
	case "top_share":
		return "top attributed share"
	case "newly_prominent":
		return "newly prominent attributed share"
	case "tool_error_surge":
		return "tool-error surge"
	}
	return "signal"
}
