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
	"time"

	"github.com/llm-measurement/fleetdiff/internal/change"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

func validateTokenCUSUMFlags(flags *flag.FlagSet, enabled bool, floor, shift float64) error {
	customized := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "cusum-scale-floor" || option.Name == "cusum-min-shift" {
			customized = true
		}
	})
	if customized && !enabled {
		return errors.New("CUSUM options require --token-cusum")
	}
	if !enabled {
		return nil
	}
	config := change.CUSUMConfig{Reference: compare.DefaultScanOptions().Baseline, ScaleFloor: floor, MinShift: shift}
	_, err := change.NewCUSUM(config, change.CUSUMCalibration{})
	return err
}

func runTokenCUSUMScan(input []summary.Envelope, o compare.ScanOptions, options compare.TokenCUSUMOptions, format string, out, errout io.Writer) int {
	report, err := compare.ScanTokenCUSUM(input, o, options)
	if err != nil {
		fmt.Fprintln(errout, err.Error())
		return 1
	}
	var buffer bytes.Buffer
	if format == "json" {
		encoder := json.NewEncoder(&buffer)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(errout, "cannot encode token CUSUM scan")
			return 1
		}
	} else {
		renderScan(&buffer, report.ScanReport)
		renderTokenCUSUM(&buffer, report.TokenCUSUM)
	}
	if _, err := io.Copy(out, &buffer); err != nil {
		fmt.Fprintln(errout, "cannot write scan")
		return 1
	}
	if report.UnusualWindows > 0 {
		return 3 // Existing scan findings only; CUSUM never supplies a verdict.
	}
	return 4 // An uncalibrated diagnostic is not an evaluated quiet result.
}

func renderTokenCUSUM(out io.Writer, analysis compare.TokenCUSUMAnalysis) {
	fmt.Fprintf(out, "\nObserved-token CUSUM: %s (collector processing time).\n", analysis.Status)
	fmt.Fprintf(out, "Reference: %d preceding windows; scale floor %g tokens; minimum shift %g scale units.\n",
		analysis.Reference, analysis.ScaleFloor, analysis.MinShift)
	for _, window := range analysis.Windows {
		fmt.Fprintf(out, "  %s: %s", time.Unix(0, window.Start).UTC().Format(time.RFC3339Nano), window.Status)
		if window.Total != nil {
			fmt.Fprintf(out, "; %d observed tokens", *window.Total)
		}
		if window.Diagnostic != nil {
			fmt.Fprintf(out, "; statistic %g; residual %g", window.Diagnostic.Statistic, window.Diagnostic.Residual)
		}
		fmt.Fprintln(out, ".")
		if window.Reason != "" {
			fmt.Fprintf(out, "    %s\n", window.Reason)
		}
	}
	for _, note := range analysis.Notes {
		fmt.Fprintln(out, note)
	}
}
