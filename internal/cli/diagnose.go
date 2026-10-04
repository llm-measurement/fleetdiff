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
	"slices"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/diagnose"
)

const diagnoseHelp = `fleetdiff diagnose statically checks a supported collector configuration.

Usage: fleetdiff diagnose [options] CONFIG

CONFIG is one regular YAML file or - for stdin. Flags go before CONFIG.

Options:
  --format text|json    Report format (default text)
  --shadow-config      Print a fixed standalone proposal to stdout; report to stderr
  --help               Show this help

No configuration execution, environment resolution, include reads, or network.
Reports show YAML key paths, line numbers, and known attribute names.
Raw values, custom names, file paths, and parser errors stay hidden.
Limits: 256 KiB input, 8192 nodes, depth 32, no anchors/aliases, 128 findings.
Duplicate keys, complex keys, multiple documents, and special files are rejected.
Exit status: 0 supported checks pass, 3 blocking supported risks,
4 unsupported/indeterminate (takes precedence), 1 input/output failure, 2 options.
Static success is not a deployment or privacy certification.

--shadow-config cannot be combined with --format. Review its fixed placeholders;
add a separate shadow branch without replacing the existing collector/backend.
The exit code still describes the INPUT, not approval of the proposal.
Expected producers require independent inventory and disjoint observations.
`

func runDiagnose(args []string, in io.Reader, out, errout io.Writer) int {
	fail := func(code int, message string) int { fmt.Fprintln(errout, message); return code }
	flags := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	format := flags.String("format", "text", "")
	shadow := flags.Bool("shadow-config", false, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := io.Copy(out, strings.NewReader(diagnoseHelp)); err != nil {
				return fail(1, "cannot write diagnosis help")
			}
			return 0
		}
		return fail(2, "invalid diagnose options; run fleetdiff diagnose --help")
	}
	formatSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "format" {
			formatSet = true
		}
	})
	if flags.NArg() != 1 || (*format != "text" && *format != "json") || (*shadow && formatSet) {
		return fail(2, "supply one configuration and supported options; run fleetdiff diagnose --help")
	}
	r, err := diagnose.Diagnose(flags.Arg(0), in)
	if err != nil {
		return fail(1, err.Error())
	}
	var buffer bytes.Buffer
	if *format == "json" {
		e := json.NewEncoder(&buffer)
		e.SetIndent("", "  ")
		if err := e.Encode(r); err != nil {
			return fail(1, "cannot encode diagnosis")
		}
	} else {
		renderDiagnosis(&buffer, r)
	}
	target := out
	if *shadow {
		target = errout
	}
	if _, err := io.Copy(target, &buffer); err != nil {
		return fail(1, "cannot write diagnosis")
	}
	if *shadow && r.ConfigurationParsed {
		if _, err := io.Copy(out, strings.NewReader(diagnose.ShadowConfig())); err != nil {
			return fail(1, "cannot write shadow proposal")
		}
	}
	return r.ExitCode()
}

func renderDiagnosis(out io.Writer, r diagnose.Report) {
	if r.Status == "supported_safe" {
		fmt.Fprintln(out, "Looks safe: no blocking findings.")
		return
	}
	type location struct {
		path string
		line int
	}
	var order []location
	groups := make(map[location][]diagnose.Finding)
	for _, finding := range r.Findings {
		key := location{finding.Path, finding.Line}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], finding)
	}
	if r.Status == "unsafe" {
		word := "problems"
		if len(order) == 1 {
			word = "problem"
		}
		fmt.Fprintf(out, "%d %s to fix:\n", len(order), word)
	} else {
		fmt.Fprintf(out, "Needs review: %d configuration locations could not pass the checks.\n", len(order))
	}
	for _, key := range order {
		where := key.path
		if where == "" {
			where = "configuration"
		}
		if key.line > 0 {
			where += fmt.Sprintf(" (line %d)", key.line)
		}
		fmt.Fprintf(out, "  %s: %s\n", where, diagnosisActions(groups[key]))
	}
	for _, step := range r.NextSteps {
		fmt.Fprintln(out, "Next: "+step)
	}
}

func diagnosisActions(findings []diagnose.Finding) string {
	var ids, attributes, actions []string
	for _, f := range findings {
		if !slices.Contains(ids, f.ID) {
			ids = append(ids, f.ID)
		}
		if f.Attribute != "" && !slices.Contains(attributes, f.Attribute) {
			attributes = append(attributes, f.Attribute)
		}
	}
	labelRisk := slices.Contains(ids, "sensitive_slice") || slices.Contains(ids, "high_cardinality_slice") || slices.Contains(ids, "hashed_source_overlap")
	if labelRisk {
		source := "This attribute"
		if len(attributes) > 0 {
			source = strings.Join(attributes, ", ")
		}
		actions = append(actions, source+" is a metric label; remove it from slices and use a hashed field instead.")
	}
	for _, id := range ids {
		switch id {
		case "sensitive_slice", "high_cardinality_slice", "hashed_source_overlap":
		case "unsafe_operation_filter":
			actions = append(actions, "Keep only model operations (chat, generate_content, text_completion, embeddings), each once. Tool and agent operations count as model attempts if included.")
		case "missing_operation_filter":
			actions = append(actions, "Set operation_filter.llm_operations to the model operations you collect.")
		default:
			actions = append(actions, diagnose.Description(id))
		}
	}
	return strings.Join(actions, " ")
}
