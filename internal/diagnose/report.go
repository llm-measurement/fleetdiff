// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package diagnose checks a bounded, explicit subset of collector configuration.
// It never interprets configuration providers or returns arbitrary input strings.
package diagnose

// Reference is a one-based YAML node ordinal. Path contains only known schema
// keys and positional aliases; Attribute contains only a known source name.
type Finding struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"`
	Reference int    `json:"reference"`
	Path      string `json:"path,omitempty"`
	Line      int    `json:"line,omitempty"`
	Column    int    `json:"column,omitempty"`
	Attribute string `json:"attribute,omitempty"`
}

type Report struct {
	Version             int       `json:"version"`
	Status              string    `json:"status"`
	ConfigurationParsed bool      `json:"configuration_parsed"`
	Findings            []Finding `json:"findings"`
	Limits              []string  `json:"limits"`
	NextSteps           []string  `json:"next_steps"`
}

// ExitCode prioritizes uncertainty over supported blocking findings.
func (r Report) ExitCode() int {
	switch r.Status {
	case "supported_safe":
		return 0
	case "unsafe":
		return 3
	default:
		return 4
	}
}

// Description is a closed vocabulary, shared by the text renderer and tests.
func Description(id string) string {
	switch id {
	case "unsupported_yaml":
		return "YAML syntax, structure, or safety limits are unsupported."
	case "unsupported_component":
		return "A component or signal is outside the supported subset."
	case "unsupported_mapping":
		return "A mapping, type, or value cannot be verified statically."
	case "unsupported_field":
		return "A configuration field is outside the supported subset."
	case "unresolved_interpolation":
		return "Interpolation is unresolved; no environment or includes were read."
	case "unknown_slice_source":
		return "A slice source has no supported privacy or cardinality classification."
	case "sensitive_slice":
		return "A slice exposes a sensitive source as plaintext metric labels."
	case "high_cardinality_slice":
		return "A slice uses an identifier or other high-cardinality source."
	case "hashed_source_overlap":
		return "A slice overlaps a configured or default hashed-field source."
	case "missing_operation_filter":
		return "Explicit model-operation filtering is required for accounting review."
	case "unsafe_operation_filter":
		return "Operation filtering includes non-model spans or an empty selection."
	case "unknown_operation":
		return "An operation is not in the supported model-operation mapping."
	case "unknown_field_source":
		return "A hashed-field extraction mapping requires review against actual instrumentation."
	case "unsafe_token_mapping":
		return "Token mappings can mix directions or add subsets to aggregate totals."
	case "unknown_token_mapping":
		return "A token source cannot be verified against supported accounting conventions."
	case "unsafe_hashing":
		return "Hashing requires the supported keyed algorithm and an environment-variable name."
	case "pipeline_wiring":
		return "Pipeline references, signal direction, or connector branch wiring are invalid."
	case "multiple_producers":
		return "Multiple connector instances or trace inputs need independent disjointness review."
	case "missing_connector":
		return "No supported summary connector was found."
	case "findings_truncated":
		return "The finding limit was reached; remaining configuration is indeterminate."
	default:
		return ""
	}
}

func newReport() Report {
	return Report{Version: 1, Status: "supported_safe", Findings: []Finding{},
		Limits: []string{
			"Static checks only; not a certification of deployment security, actual label values, or traffic coverage.",
			"No environment, include files, network, or collector execution is used by diagnosis.",
			"Low cardinality does not establish privacy. Hashed summaries remain pseudonymous.",
			"Expected producers must come from independent trusted inventory with disjoint requests; input files cannot prove completeness or independence.",
		}, NextSteps: []string{"Review mappings and actual capture coverage; validate with the intended collector before a separate shadow trial."}}
}
