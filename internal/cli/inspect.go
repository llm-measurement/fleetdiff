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
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/inspect"
)

const inspectHelp = `fleetdiff inspect checks what your traces can answer, locally.

Usage: fleetdiff inspect [options] CAPTURE

CAPTURE is an OTLP file, a nonrecursive directory, or - for stdin.
Flags go before CAPTURE. Directories select .json, .jsonl, .otlp, .pb, .bin.
JSON includes the collector file exporter's uncompressed JSON lines.

Options:
  --format text|json                  Report format (default text)
  --input-format auto|json|proto|file-proto
                                      auto reads JSON; select binary framing explicitly
  --top N                             Show 1-100 candidates per ranking (default 10)
  --show-hashes                       Include pseudonymous, linkable hashes
  --show-names                        Reveal custom attribute names, never their values
  --secret-env NAME                   Use a secret from the environment instead of a fresh per-run key
  --help                              Show this help

No uploads, listeners, or file writes. Default reports use aliases, not raw values.
Limits: 32 MiB input, 8 MiB per OTLP record, 100000 spans, 128 attribute dimensions.
Exit status: 0 report/help (including partial readiness), 1 input/output error,
2 invalid options. Inspect describes the capture; it does not generate summaries.
`

func runInspect(args []string, in io.Reader, out, errout io.Writer) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	format := flags.String("format", "text", "")
	inputFormat := flags.String("input-format", "auto", "")
	top := flags.Int("top", 10, "")
	hashes := flags.Bool("show-hashes", false, "")
	names := flags.Bool("show-names", false, "")
	secret := flags.String("secret-env", "", "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := io.WriteString(out, inspectHelp); err != nil {
				return fail(1, "cannot write help")
			}
			return 0
		}
		return fail(2, "invalid inspect options; run fleetdiff inspect --help")
	}
	if flags.NArg() != 1 || *top < 1 || *top > 100 || !slices.Contains([]string{"text", "json"}, *format) || !slices.Contains([]string{"auto", "json", "proto", "file-proto"}, *inputFormat) {
		return fail(2, "supply one capture, supported formats, and top between 1 and 100; run fleetdiff inspect --help")
	}
	r, err := inspect.Inspect(flags.Arg(0), in, inspect.Options{InputFormat: *inputFormat, Top: *top, ShowHashes: *hashes, ShowNames: *names, SecretEnv: *secret})
	if err != nil {
		return fail(1, err.Error())
	}
	var buffer bytes.Buffer
	if *format == "json" {
		e := json.NewEncoder(&buffer)
		e.SetIndent("", "  ")
		if err = e.Encode(r); err != nil {
			return fail(1, "cannot encode inspection")
		}
	} else {
		renderInspection(&buffer, r, *top)
	}
	if _, err = io.Copy(out, &buffer); err != nil {
		return fail(1, "cannot write inspection")
	}
	return 0
}

func renderInspection(out io.Writer, r inspect.Report, top int) {
	fmt.Fprintf(out, "Your traces can fully answer %d of %d questions.\n", r.Readiness.Ready, r.Readiness.Total)
	if len(r.NextSteps) > 0 {
		fmt.Fprintln(out, "Next: "+r.NextSteps[0])
	}
	n := r.ObservedCounters["gen_ai_sketch_requests_total"]
	fmt.Fprintf(out, "\n%d model attempts across %d captured spans; %d reported tokens.\n", n, r.Capture.Spans, r.ObservedCounters["gen_ai_sketch_total_tokens_total"])
	fmt.Fprintf(out, "Usage: %d complete, %d missing either field.\n", n-r.ObservedCounters["gen_ai_sketch_missing_token_usage_total"], r.ObservedCounters["gen_ai_sketch_missing_token_usage_total"])
	fmt.Fprintf(out, "Origin unknown: input %d, output %d attempts.\n", r.UsageProvenance["input/unknown"], r.UsageProvenance["output/unknown"])
	for _, key := range slices.Sorted(maps.Keys(r.TokenObservations)) {
		if r.TokenObservations[key] > 0 && (strings.HasSuffix(key, "/invalid") || strings.HasSuffix(key, "/conflict") || strings.HasSuffix(key, "/subset_violation")) {
			fmt.Fprintf(out, "Usage issue: %s on %d attempts.\n", key, r.TokenObservations[key])
		}
	}
	fmt.Fprintln(out, "\nQuestions")
	for _, q := range r.Readiness.Questions {
		name := strings.ReplaceAll(q.ID, "_", " ")
		name = strings.ToUpper(name[:1]) + name[1:]
		fmt.Fprintf(out, "  %s: %s (%d/%d attempts)\n", name, strings.ReplaceAll(q.Status, "_", " "), q.Covered, q.Total)
		if q.NextStep != "" {
			fmt.Fprintln(out, "    "+q.NextStep)
		}
	}
	if len(r.Dimensions) > 0 {
		fmt.Fprintln(out, "\nLabel review: estimated distinct values in this capture")
		for _, d := range r.Dimensions[:min(len(r.Dimensions), top)] {
			fmt.Fprintf(out, "  %s: ~%.0f; %d observations; %s\n", strconv.QuoteToASCII(d.Attribute), d.Estimate, d.Observations, strings.ReplaceAll(d.LabelRisk, "_", " "))
		}
		fmt.Fprintf(out, "  Showing %d/%d fields; nominal HLL RSE %.2f%%. Values are not total series.\n", min(len(r.Dimensions), top), len(r.Dimensions), r.Dimensions[0].NominalRSE*100)
	}
	for _, id := range r.Identities {
		if id.Present == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s: ~%.0f distinct; identity present on %d/%d attempts\n", id.Field, id.Distinct, id.Present, n)
		for _, rank := range id.Rankings {
			fmt.Fprintf(out, "  By %s: %d attributed; %d/%d candidates shown\n", rank.Weight, rank.Total, len(rank.Items), rank.Candidates)
			for _, item := range rank.Items {
				label := item.Alias
				if item.Hash != "" {
					label += " (" + item.Hash + ")"
				}
				bounds := compare.Interval{Lower: item.Lower, Upper: item.Upper}
				share := compare.Share{Lower: item.LowerShare, Upper: item.UpperShare}
				fmt.Fprintf(out, "    %s: %s (%s)\n", label, intervalText(bounds), shareText(&share, &bounds))
			}
		}
	}
	fmt.Fprintln(out, "\nCapture notes")
	for _, note := range r.Notes {
		fmt.Fprintln(out, "  "+note)
	}
}
