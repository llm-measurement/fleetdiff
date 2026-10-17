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
	Model, Key, Team                     string
	Input, Output, CacheRead, CacheWrite *uint64
	// Summary cohorts retain per-request extrema; totals are not request sizes.
	InputMin, InputMax *uint64
	Count              uint64
	Issue              string
	Failed             bool
}

type Source struct {
	Kind    string    `json:"kind"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end_exclusive"`
	Rows    uint64    `json:"observations"`
	Outside uint64    `json:"outside_period"`
	Gaps    []string  `json:"gaps"`
}
