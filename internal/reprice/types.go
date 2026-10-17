// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package reprice calculates local, assumption-driven costs of recorded usage.
package reprice

import "time"

type Selector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Move struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Fraction string    `json:"fraction"`
	Scope    *Selector `json:"scope,omitempty"`
}

type Scenario struct {
	Version int    `json:"version"`
	Moves   []Move `json:"moves"`
}

type Mapping struct {
	Selector Selector `json:"selector"`
	Workload string   `json:"workload"`
}

type WorkloadMap struct {
	Version  int       `json:"version"`
	Mappings []Mapping `json:"mappings"`
}

// Record exists only during a reader callback. Identity fields must never enter
// a report. Nil components are unknown; an explicit zero is a recorded value.
type Record struct {
	Model, Key, Team, Route, Agent string
	// Only routing-aware readers attest selected model plus deployment evidence.
	ServedIdentityKnown                  bool
	Input, Output, CacheRead, CacheWrite *uint64
	// Summary cohorts retain per-request extrema; totals are not request sizes.
	InputMin, InputMax *uint64
	Count              uint64
	Issue              string
	Failed             bool
}

type Source struct {
	Kind        string               `json:"kind"`
	Start       time.Time            `json:"start"`
	End         time.Time            `json:"end_exclusive"`
	Rows        uint64               `json:"observations"`
	Outside     uint64               `json:"outside_period"`
	Gaps        []string             `json:"gaps"`
	Attribution *AttributionCoverage `json:"attribution_coverage,omitempty"`
}

type IdentityCoverage struct {
	Known   uint64 `json:"known_rows"`
	Unknown uint64 `json:"unknown_rows"`
}

type AttributionFields struct {
	Rows           uint64           `json:"rows"`
	Route          IdentityCoverage `json:"original_route"`
	Agent          IdentityCoverage `json:"agent"`
	Model          IdentityCoverage `json:"selected_model"`
	Deployment     IdentityCoverage `json:"deployment_id"`
	ServedIdentity IdentityCoverage `json:"served_identity"`
}

type CallTypeAttribution struct {
	CallType string `json:"call_type"`
	AttributionFields
}

// AttributionCoverage covers every source-period row before pricing exclusions.
// CallType is bounded to supported names, pass_through_endpoint, and unknown.
type AttributionCoverage struct {
	IdentityBasis string `json:"identity_basis"`
	AttributionFields
	ByCallType []CallTypeAttribution `json:"by_call_type"`
}
