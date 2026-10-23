// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
)

// FileReport contains no IDs, custom labels, arbitrary categories or payloads.
// It describes physical rows, not deduplicated or period-qualified coverage.
type FileReport struct {
	PhysicalRows           uint64            `json:"physical_rows"`
	GenerationRows         uint64            `json:"generation_rows"`
	OtherRows              uint64            `json:"other_rows"`
	OtherTypes             map[string]uint64 `json:"other_types"`
	Components             map[string]uint64 `json:"reported_disjoint_components"`
	ReportedFields         map[string]uint64 `json:"reported_component_rows"`
	InclusiveInput         uint64            `json:"known_inclusive_input"`
	InclusiveOutput        uint64            `json:"known_inclusive_output"`
	InclusiveInputRows     uint64            `json:"known_inclusive_input_rows"`
	InclusiveOutputRows    uint64            `json:"known_inclusive_output_rows"`
	MissingUsageRows       uint64            `json:"missing_usage_rows"`
	PartialUsageRows       uint64            `json:"partial_usage_rows"`
	InvalidUsageRows       uint64            `json:"invalid_usage_rows"`
	UnknownUsageCategories uint64            `json:"unmapped_category_occurrences"`
	TotalMismatchRows      uint64            `json:"declared_total_mismatch_rows"`
	MissingCostRows        uint64            `json:"missing_recorded_cost_rows"`
	InvalidCostRows        uint64            `json:"invalid_recorded_cost_rows"`
	UsageWithZeroCostRows  uint64            `json:"usage_with_zero_recorded_cost_rows"`
	RecordedCost           string            `json:"recorded_cost_langfuse"`
}

type byteBudget struct {
	r     io.Reader
	count int64
}

func (r *byteBudget) Read(p []byte) (int, error) {
	if r.count > MaxFileBytes {
		return 0, ErrFileLimit
	}
	remaining := int64(MaxFileBytes) - r.count
	if int64(len(p)) > remaining+1 {
		p = p[:remaining+1]
	}
	n, err := r.r.Read(p)
	r.count += int64(n)
	if r.count > MaxFileBytes {
		return n, ErrFileLimit
	}
	return n, err
}

// ReadFile consumes an already-open local stream described by a completion
// manifest. It returns no partial totals on a parse, size, count, or gzip error.
// The directory planner supplies confinement, dedup and interval coverage;
// this function never opens a path or calls a service.
func ReadFile(r io.Reader, file File) (FileReport, error) {
	if file.Table != "observations_v2" {
		return FileReport{}, ErrUnsupported
	}
	return readFile(r, file, nil)
}

func readFile(r io.Reader, file File, consume func(projection) error) (FileReport, error) {
	if file.Table != "observations_v2" && file.Table != "scores" {
		return FileReport{}, ErrUnsupported
	}
	if file.SizeBytes < 0 || file.RowCount < 0 {
		return FileReport{}, ErrInvalid
	}
	if file.SizeBytes > MaxFileBytes || file.RowCount > MaxRows {
		return FileReport{}, ErrFileLimit
	}
	if err := validateFormat(file); err != nil {
		return FileReport{}, err
	}
	stored := &byteBudget{r: r}
	buffered := bufio.NewReader(stored)
	var data io.Reader = buffered
	var gz *gzip.Reader
	if file.Compressed {
		var err error
		gz, err = gzip.NewReader(buffered)
		if err != nil {
			if stored.count > MaxFileBytes || errors.Is(err, ErrFileLimit) {
				return FileReport{}, ErrFileLimit
			}
			return FileReport{}, ErrInvalid
		}
		defer gz.Close()
		gz.Multistream(false)
		data = gz
	}
	expanded := &byteBudget{r: data}
	report := FileReport{OtherTypes: map[string]uint64{}, Components: map[string]uint64{}, ReportedFields: map[string]uint64{}}
	cost := new(big.Rat)
	var projectionErr error
	visit := func(p projection) error {
		if report.PhysicalRows >= MaxRows {
			projectionErr = ErrFileLimit
			return projectionErr
		}
		if file.Table == "scores" {
			report.PhysicalRows++
			return nil
		}
		if err := report.add(p, cost); err != nil {
			projectionErr = err
			return err
		}
		if consume != nil {
			return consume(p)
		}
		return nil
	}
	var err error
	switch file.FileType {
	case "CSV":
		if file.Table == "scores" && file.RowCount == 0 {
			// Native CSV score exports have no header when the table is empty.
			var empty []byte
			empty, err = io.ReadAll(io.LimitReader(expanded, 1))
			if len(empty) != 0 {
				err = ErrInvalid
			}
		} else {
			err = readCSV(expanded, visit)
		}
	case "JSON":
		err = readJSON(expanded, true, visit)
	case "JSONL":
		err = readJSON(expanded, false, visit)
	}
	if stored.count > MaxFileBytes || expanded.count > MaxFileBytes {
		return FileReport{}, ErrFileLimit
	}
	if err != nil {
		if projectionErr != nil {
			return FileReport{}, projectionErr
		}
		if errors.Is(err, ErrFileLimit) {
			return FileReport{}, ErrFileLimit
		}
		return FileReport{}, ErrInvalid
	}
	// readJSON/readCSV drive expanded EOF, so gzip's CRC/trailer is checked.
	// Exactly one member is supported. Never expose gzip header names/comments.
	if _, err = buffered.Peek(1); err != io.EOF {
		if stored.count > MaxFileBytes || errors.Is(err, ErrFileLimit) {
			return FileReport{}, ErrFileLimit
		}
		return FileReport{}, ErrInvalid
	}
	if stored.count != file.SizeBytes || int64(report.PhysicalRows) != file.RowCount {
		return FileReport{}, ErrIncomplete
	}
	report.RecordedCost = accounting.CostText(cost)
	return report, nil
}

func text(raw []byte) (string, error) {
	var s string
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil || s == "" {
		return "", ErrInvalid
	}
	return s, nil
}

func token(raw []byte) (uint64, bool, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false, false
	}
	// The producer emits numeric tokens, not quoted strings, booleans or floats.
	n, err := strconv.ParseUint(string(raw), 10, 63)
	return n, err == nil, err != nil
}

func (r *FileReport) add(p projection, cost *big.Rat) error {
	for _, name := range []string{"id", "project_id"} {
		if _, err := text(p.fields[name]); err != nil {
			return err
		}
	}
	kind, err := text(p.fields["type"])
	if err != nil {
		return err
	}
	start, err := text(p.fields["start_time"])
	if err != nil {
		return err
	}
	if _, err = time.Parse("2006-01-02 15:04:05.999999999", start); err != nil {
		return ErrInvalid
	}
	r.PhysicalRows++
	if kind != "GENERATION" {
		r.OtherRows++
		switch kind {
		case "SPAN", "EVENT", "AGENT", "TOOL", "CHAIN", "RETRIEVER", "EVALUATOR", "EMBEDDING", "GUARDRAIL":
		default:
			kind = "__unknown__"
		}
		r.OtherTypes[kind]++
		return nil
	}
	r.GenerationRows++
	r.UnknownUsageCategories += p.unknown
	values := map[string]uint64{}
	known := map[string]bool{}
	invalid := p.badUsage
	inputValid, outputValid := !p.badUsage, !p.badUsage
	hasKnownUsage := false
	for _, name := range []string{"input", "output", "cache_read", "cache_write", "reasoning", "total"} {
		n, ok, bad := token(p.usage[name])
		invalid = invalid || bad
		if bad {
			switch name {
			case "input", "cache_read", "cache_write":
				inputValid = false
			case "output", "reasoning":
				outputValid = false
			}
		}
		values[name] = n
		known[name] = ok
		if ok && name != "total" {
			hasKnownUsage = true
			r.Components[name], err = accounting.AddBounded(r.Components[name], n)
			if err != nil {
				return ErrAccountingLimit
			}
			r.ReportedFields[name]++
		}
	}
	if len(p.usage) == 0 && !p.badUsage {
		r.MissingUsageRows++
	}
	// Native input/output are the base; optional disjoint detail adds to them.
	inKnown := known["input"] && inputValid
	outKnown := known["output"] && outputValid
	if !inKnown || !outKnown {
		r.PartialUsageRows++
	}
	var in, out uint64
	if inKnown {
		r.InclusiveInputRows++
		in, err = accounting.AddBounded(values["input"], values["cache_read"])
		if err != nil {
			return ErrAccountingLimit
		}
		in, err = accounting.AddBounded(in, values["cache_write"])
		if err != nil {
			return ErrAccountingLimit
		}
		r.InclusiveInput, err = accounting.AddBounded(r.InclusiveInput, in)
		if err != nil {
			return ErrAccountingLimit
		}
	}
	if outKnown {
		r.InclusiveOutputRows++
		out, err = accounting.AddBounded(values["output"], values["reasoning"])
		if err != nil {
			return ErrAccountingLimit
		}
		r.InclusiveOutput, err = accounting.AddBounded(r.InclusiveOutput, out)
		if err != nil {
			return ErrAccountingLimit
		}
	}
	if inKnown && outKnown && known["total"] {
		total, err := accounting.AddBounded(in, out)
		if err != nil {
			return ErrAccountingLimit
		}
		if total != values["total"] {
			r.TotalMismatchRows++
		}
	}
	if invalid {
		r.InvalidUsageRows++
	}
	raw := bytes.TrimSpace(p.fields["total_cost"])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		r.MissingCostRows++
		return nil
	}
	v, ok := accounting.Cost(string(raw))
	if !ok {
		r.InvalidCostRows++
		return nil
	}
	cost.Add(cost, v)
	if _, ok = accounting.Cost(accounting.CostText(cost)); !ok {
		return ErrAccountingLimit
	}
	if v.Sign() == 0 && hasKnownUsage {
		r.UsageWithZeroCostRows++
	}
	return nil
}
