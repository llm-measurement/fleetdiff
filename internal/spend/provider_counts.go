// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/boundedjson"
	"github.com/llm-measurement/fleetdiff/internal/localfile"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

const maxProviderRows = 10000
const maxProviderBytes = 4 << 20
const maxProviderLine = 2048

var errProviderInput = errors.New("invalid provider counts input; use bounded aggregate CSV, unique date/provider/model rows and nonnegative integer measurements")
var errProviderMapping = errors.New("invalid provider model map; each source provider/model pair must have exactly one target")
var errProviderContract = errors.New("invalid provider count contract; use fleetdiff-provider-counts/v1 with UTC bounds, scope declarations and a primary counter basis")

type providerModel struct{ provider, model string }

type providerMeasurements struct{ requests, input, output *uint64 }

// Scope declarations contain only match states, never account or project IDs.
// The operator retains actual filters in their private retrieval evidence.
type ProviderPopulation struct {
	Account   string `json:"account"`
	Project   string `json:"project_or_workspace"`
	Regions   string `json:"region_set"`
	Gateway   string `json:"gateway"`
	Operation string `json:"operation"`
}

type ProviderCountContract struct {
	Schema                  string             `json:"schema"`
	Start                   time.Time          `json:"start_utc"`
	End                     time.Time          `json:"end_exclusive_utc"`
	Retrieved               time.Time          `json:"retrieved_at_utc"`
	PaginationComplete      bool               `json:"pagination_complete"`
	Population              ProviderPopulation `json:"population"`
	CountBasis              string             `json:"count_basis"`
	CountingUnitsMatch      bool               `json:"counting_units_match"`
	TokenUnitsMatch         bool               `json:"token_units_match"`
	SettlementMarginSeconds uint64             `json:"settlement_margin_seconds,omitempty"`
}

type ProviderCoverage struct {
	Contract *ProviderCountContract `json:"contract,omitempty"`
	Days     []ProviderCoverageDay  `json:"days"`
}

// Daily LiteLLM totals remain day-level, including rows for unmapped models.
type ProviderCoverageDay struct {
	PopulationStatus                  string                  `json:"population_status"`
	Date                              string                  `json:"date_utc"`
	Status                            string                  `json:"status"`
	LoggedRows                        uint64                  `json:"logged_rows"`
	UnmappedLoggedRows                uint64                  `json:"unmapped_logged_rows"`
	LiteLLMDailyRequests              *uint64                 `json:"litellm_daily_requests"`
	ProviderRequests                  *uint64                 `json:"provider_requests"`
	LiteLLMTablesAgreeProviderDiffers bool                    `json:"litellm_tables_agree_provider_differs,omitempty"`
	Models                            []ProviderCoverageModel `json:"models"`
}

type ProviderCoverageModel struct {
	Provider             string  `json:"provider"`
	Model                string  `json:"model"`
	Status               string  `json:"status"`
	Comparable           bool    `json:"comparable"`
	ProviderRequests     *uint64 `json:"provider_requests"`
	LoggedRows           uint64  `json:"logged_rows"`
	FailedRows           uint64  `json:"failed_rows"`
	UnknownStatusRows    uint64  `json:"unknown_status_rows"`
	RequestDifference    *int64  `json:"provider_minus_logged_rows"`
	ProviderInputTokens  *uint64 `json:"provider_input_tokens"`
	ProviderOutputTokens *uint64 `json:"provider_output_tokens"`
	LoggedInputTokens    *uint64 `json:"logged_input_tokens"`
	LoggedOutputTokens   *uint64 `json:"logged_output_tokens"`
	InputDifference      *int64  `json:"provider_minus_logged_input_tokens"`
	OutputDifference     *int64  `json:"provider_minus_logged_output_tokens"`
}

func readProviderFile(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, errProviderInput
	}
	defer root.Close()
	data, err := localfile.Read(root, filepath.Base(path), maxProviderBytes)
	if err != nil {
		return nil, errProviderInput
	}
	return data, nil
}

// Limit physical lines before CSV allocation. This aggregate contract excludes
// multiline fields; safe errors never echo headers, values or local paths.
func providerCSV(path string, required, optional []string, visit func(map[string]string) error) error {
	data, err := readProviderFile(path)
	if err != nil {
		return err
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, maxProviderLine+2), maxProviderLine+2)
	var header []string
	rows := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > maxProviderLine || !utf8.Valid(line) {
			return errProviderInput
		}
		if len(line) == 0 {
			continue
		}
		r := csv.NewReader(bytes.NewReader(line))
		r.FieldsPerRecord = -1
		fields, err := r.Read()
		if err != nil || len(fields) > len(required)+len(optional) {
			return errProviderInput
		}
		if header == nil {
			for i, name := range fields {
				if !slices.Contains(required, name) && !slices.Contains(optional, name) || slices.Contains(fields[:i], name) {
					return errProviderInput
				}
			}
			for _, name := range required {
				if !slices.Contains(fields, name) {
					return errProviderInput
				}
			}
			header = fields
			continue
		}
		rows++
		if rows > maxProviderRows || len(fields) != len(header) {
			return errProviderInput
		}
		values := make(map[string]string, len(header))
		for i, name := range header {
			values[name] = fields[i]
		}
		if err := visit(values); err != nil {
			return err
		}
	}
	if scanner.Err() != nil || header == nil {
		return errProviderInput
	}
	return nil
}

func providerInteger(value string) (*uint64, error) {
	if value == "" {
		return nil, nil
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return nil, errProviderInput
		}
	}
	n, err := strconv.ParseUint(value, 10, 63)
	if err != nil {
		return nil, errProviderInput
	}
	return &n, nil
}

func providerIdentity(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func readProviderContract(path string, now time.Time) (*ProviderCountContract, error) {
	if path == "" {
		return nil, nil
	}
	data, err := readProviderFile(path)
	if err != nil || len(data) > 8192 {
		return nil, errProviderContract
	}
	value, err := boundedjson.Decode(data, 2, 32)
	if err != nil {
		return nil, errProviderContract
	}
	fields, ok := value.(map[string]any)
	if !ok || len(fields) < 9 || len(fields) > 10 {
		return nil, errProviderContract
	}
	for _, name := range []string{"schema", "start_utc", "end_exclusive_utc", "retrieved_at_utc", "pagination_complete", "population", "count_basis", "counting_units_match", "token_units_match"} {
		if _, present := fields[name]; !present {
			return nil, errProviderContract
		}
	}
	if len(fields) == 10 {
		number, ok := fields["settlement_margin_seconds"].(json.Number)
		if !ok {
			return nil, errProviderContract
		}
		seconds, err := strconv.ParseUint(string(number), 10, 64)
		if err != nil || seconds > uint64((1<<63-1)/int64(time.Second)) {
			return nil, errProviderContract
		}
	}
	for _, name := range []string{"pagination_complete", "counting_units_match", "token_units_match"} {
		if _, ok := fields[name].(bool); !ok {
			return nil, errProviderContract
		}
	}
	population, ok := fields["population"].(map[string]any)
	if !ok || len(population) != 5 {
		return nil, errProviderContract
	}
	for _, name := range []string{"account", "project_or_workspace", "region_set", "gateway", "operation"} {
		if _, present := population[name]; !present {
			return nil, errProviderContract
		}
	}
	var c ProviderCountContract
	if json.Unmarshal(data, &c) != nil || c.Schema != "fleetdiff-provider-counts/v1" {
		return nil, errProviderContract
	}
	for _, stamp := range []time.Time{c.Start, c.End, c.Retrieved} {
		_, offset := stamp.Zone()
		if offset != 0 || stamp.Year() < 1970 || stamp.Year() > 2200 || stamp.After(now) {
			return nil, errProviderContract
		}
	}
	if !c.Start.Before(c.End) || c.Start != c.Start.Truncate(24*time.Hour) || c.End != c.End.Truncate(24*time.Hour) || c.Retrieved.Before(c.End) {
		return nil, errProviderContract
	}
	for _, state := range c.populationStates() {
		if !slices.Contains([]string{"matched", "unverified", "mismatch"}, state) {
			return nil, errProviderContract
		}
	}
	if !slices.Contains([]string{"bedrock_invocations", "openai_model_requests", "primary_requests", "tokens_only"}, c.CountBasis) || c.CountBasis == "tokens_only" && c.CountingUnitsMatch {
		return nil, errProviderContract
	}
	return &c, nil
}

func (c *ProviderCountContract) populationStates() []string {
	return []string{c.Population.Account, c.Population.Project, c.Population.Regions, c.Population.Gateway, c.Population.Operation}
}

func (c *ProviderCountContract) populationStatus() string {
	if c == nil {
		return "population_unverified"
	}
	if !c.PaginationComplete {
		return "provider_export_incomplete"
	}
	states := c.populationStates()
	if slices.Contains(states, "mismatch") {
		return "population_mismatch"
	}
	if slices.Contains(states, "unverified") {
		return "population_unverified"
	}
	return "matched"
}

type providerKey struct {
	date   string
	id     providerModel
	mapped bool
}

type providerEntry struct {
	measurements                        *providerMeasurements
	rows, failed, unknown               uint64
	input, output                       uint64
	inputUnavailable, outputUnavailable bool
}

type providerBuilder struct {
	entries  map[providerKey]*providerEntry
	order    []providerKey
	targets  map[providerModel]bool
	mapping  map[providerModel]providerModel
	contract *ProviderCountContract
	periods  []Period
	now      time.Time
}

func newProviderCoverage(opts Options, periods ...Period) (*providerBuilder, error) {
	if opts.ProviderCountsPath == "" {
		if opts.ProviderModelMapPath != "" || opts.ProviderContractPath != "" {
			return nil, errProviderInput
		}
		return nil, nil
	}
	if opts.ScopeRoute != "" || opts.ScopeAgent != "" {
		return nil, errors.New("provider coverage requires an unscoped spend export")
	}
	b := &providerBuilder{entries: map[providerKey]*providerEntry{}, targets: map[providerModel]bool{}, mapping: map[providerModel]providerModel{}, periods: periods, now: opts.Now}
	var err error
	b.contract, err = readProviderContract(opts.ProviderContractPath, opts.Now)
	if err != nil {
		return nil, err
	}
	days := map[string]bool{}
	for _, p := range periods {
		if b.contract != nil && (p.Start.Before(b.contract.Start) || p.End.After(b.contract.End)) {
			return nil, errProviderContract
		}
		for day := p.Start.UTC().Truncate(24 * time.Hour); day.Before(p.End); day = day.AddDate(0, 0, 1) {
			days[day.Format(time.DateOnly)] = true
			if len(days) > maxCoverageDays {
				return nil, errProviderInput
			}
		}
	}
	err = providerCSV(opts.ProviderCountsPath, []string{"date_utc", "provider", "provider_model", "requests"}, []string{"input_tokens", "output_tokens"}, func(v map[string]string) error {
		day, err := time.Parse(time.DateOnly, v["date_utc"])
		id := providerModel{v["provider"], v["provider_model"]}
		if err != nil || day.Year() < 1970 || day.Year() > 2199 || day.Format(time.DateOnly) != v["date_utc"] || !providerIdentity(id.provider) || !providerIdentity(id.model) {
			return errProviderInput
		}
		if b.contract != nil && (b.contract.CountBasis == "bedrock_invocations" && id.provider != "bedrock" || b.contract.CountBasis == "openai_model_requests" && id.provider != "openai") {
			return errProviderContract
		}
		if b.contract != nil && (!b.contract.Start.Before(day.AddDate(0, 0, 1)) || b.contract.End.Before(day.AddDate(0, 0, 1))) {
			return errProviderContract
		}
		m := &providerMeasurements{}
		for i, dst := range []**uint64{&m.requests, &m.input, &m.output} {
			*dst, err = providerInteger(v[[]string{"requests", "input_tokens", "output_tokens"}[i]])
			if err != nil {
				return err
			}
		}
		if m.requests == nil && m.input == nil && m.output == nil || b.contract != nil && b.contract.CountBasis == "tokens_only" && m.requests != nil {
			return errProviderInput
		}
		key := providerKey{v["date_utc"], id, true}
		if _, duplicate := b.entries[key]; duplicate {
			return errProviderInput
		}
		e, err := b.entry(key)
		if err != nil {
			return err
		}
		e.measurements = m
		b.targets[id] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	if opts.ProviderModelMapPath != "" {
		err = providerCSV(opts.ProviderModelMapPath, []string{"source_provider", "source_model", "provider", "provider_model"}, nil, func(v map[string]string) error {
			source := providerModel{v["source_provider"], v["source_model"]}
			target := providerModel{v["provider"], v["provider_model"]}
			if !providerIdentity(source.provider) || !providerIdentity(source.model) || !providerIdentity(target.provider) || !providerIdentity(target.model) {
				return errProviderMapping
			}
			if _, duplicate := b.mapping[source]; duplicate {
				return errProviderMapping
			}
			prefix, model, qualified := strings.Cut(source.model, "/")
			exact := providerModel{prefix, model}
			if qualified && prefix == source.provider && b.targets[exact] && exact != target {
				return errProviderMapping
			}
			b.mapping[source] = target
			return nil
		})
		if err != nil {
			return nil, errProviderMapping
		}
	}
	return b, nil
}

func (b *providerBuilder) entry(key providerKey) (*providerEntry, error) {
	if e := b.entries[key]; e != nil {
		return e, nil
	}
	if len(b.entries) == maxProviderRows {
		return nil, errProviderInput
	}
	e := &providerEntry{}
	b.entries[key] = e
	b.order = append(b.order, key)
	return e, nil
}

func (b *providerBuilder) add(row Row, start time.Time) error {
	if b == nil {
		return nil
	}
	source := providerModel{row.Values["custom_llm_provider"], row.Values["model"]}
	target, mapped := b.mapping[source]
	if !mapped {
		prefix, model, qualified := strings.Cut(source.model, "/")
		exact := providerModel{prefix, model}
		if qualified && prefix != "" && prefix == source.provider && b.targets[exact] {
			target, mapped = exact, true
		} else {
			target = source
		}
	}
	e, err := b.entry(providerKey{start.UTC().Format(time.DateOnly), target, mapped})
	if err != nil {
		return err
	}
	e.rows++
	switch row.Values["status"] {
	case "failure":
		e.failed++
	case "success":
	default:
		e.unknown++
	}
	for i, field := range []string{"prompt_tokens", "completion_tokens"} {
		sum, unavailable := &e.input, &e.inputUnavailable
		if i == 1 {
			sum, unavailable = &e.output, &e.outputUnavailable
		}
		n, valid, err := accounting.Number(&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: row.Values[field]}})
		if !valid || err != nil {
			*unavailable = true
			continue
		}
		*sum, err = accounting.AddBounded(*sum, n)
		if err != nil {
			return err
		}
	}
	return nil
}

func providerDifference(provider, logged *uint64) *int64 {
	if provider == nil || logged == nil {
		return nil
	}
	n := int64(*provider) - int64(*logged)
	return &n
}

func (b *providerBuilder) dayStatus(day time.Time) string {
	end := day.AddDate(0, 0, 1)
	for _, p := range b.periods {
		if !p.Start.After(day) && !p.End.Before(end) {
			if end.After(b.now.UTC().Truncate(24 * time.Hour)) {
				return "unsettled_day"
			}
			if c := b.contract; c != nil && end.Add(time.Duration(c.SettlementMarginSeconds)*time.Second).After(c.Retrieved) {
				return "unsettled_day"
			}
			return "complete_day"
		}
	}
	return "partial_day"
}

func (b *providerBuilder) result(daily *RowCoverage) (*ProviderCoverage, error) {
	if b == nil {
		return nil, nil
	}
	r := &ProviderCoverage{Contract: b.contract, Days: []ProviderCoverageDay{}}
	dayIndex := map[string]int{}
	for _, p := range b.periods {
		for day := p.Start.UTC().Truncate(24 * time.Hour); day.Before(p.End); day = day.AddDate(0, 0, 1) {
			date := day.Format(time.DateOnly)
			if _, exists := dayIndex[date]; !exists {
				dayIndex[date] = len(r.Days)
				r.Days = append(r.Days, ProviderCoverageDay{Date: date, Status: b.dayStatus(day), PopulationStatus: b.contract.populationStatus(), ProviderRequests: new(uint64), Models: []ProviderCoverageModel{}})
			}
		}
	}
	labels := map[providerModel]string{}
	for _, key := range b.order {
		day, _ := time.Parse(time.DateOnly, key.date)
		selected := false
		for _, p := range b.periods {
			selected = selected || day.Before(p.End) && day.AddDate(0, 0, 1).After(p.Start)
		}
		if !selected {
			continue
		}
		i := dayIndex[key.date]
		d := &r.Days[i]
		e := b.entries[key]
		m := ProviderCoverageModel{LoggedRows: e.rows, FailedRows: e.failed, UnknownStatusRows: e.unknown, Status: "unmapped_spend_model"}
		// Catalog membership controls public display only, never identity joins.
		if key.mapped && reprice.PublicModelID(key.id.provider+"/"+key.id.model, key.id.provider) == key.id.model {
			m.Provider, m.Model = key.id.provider, key.id.model
		} else {
			if labels[key.id] == "" {
				labels[key.id] = fmt.Sprintf("provider-model-%d", len(labels)+1)
			}
			m.Provider, m.Model = "undisclosed", labels[key.id]
		}
		if !e.inputUnavailable {
			m.LoggedInputTokens = &e.input
		}
		if !e.outputUnavailable {
			m.LoggedOutputTokens = &e.output
		}
		if key.mapped {
			m.Status = "provider_counts_missing"
		}
		if v := e.measurements; v != nil {
			m.ProviderRequests, m.ProviderInputTokens, m.ProviderOutputTokens = v.requests, v.input, v.output
			m.Status = "request_count_unavailable"
			if v.requests != nil {
				m.RequestDifference = providerDifference(v.requests, &e.rows)
				m.Status = "matched"
				if *m.RequestDifference > 0 {
					m.Status = "provider_exceeds_logged_rows"
				} else if *m.RequestDifference < 0 {
					m.Status = "logged_rows_exceed_provider"
				}
			}
		}
		population := b.contract.populationStatus()
		if d.Status != "complete_day" {
			m.Status, m.RequestDifference = d.Status, nil
		} else if population == "population_mismatch" || population == "provider_export_incomplete" {
			m.Status, m.RequestDifference = population, nil
		}
		m.Comparable = d.Status == "complete_day" && population == "matched" && b.contract.CountingUnitsMatch && m.RequestDifference != nil
		if d.Status == "complete_day" && population == "matched" && b.contract.TokenUnitsMatch {
			m.InputDifference = providerDifference(m.ProviderInputTokens, m.LoggedInputTokens)
			m.OutputDifference = providerDifference(m.ProviderOutputTokens, m.LoggedOutputTokens)
		}
		d.LoggedRows += e.rows
		if !key.mapped {
			d.UnmappedLoggedRows += e.rows
		}
		if m.ProviderRequests == nil || !key.mapped {
			d.ProviderRequests = nil
		} else if d.ProviderRequests != nil {
			n, err := accounting.AddBounded(*d.ProviderRequests, *m.ProviderRequests)
			if err != nil {
				return nil, err
			}
			d.ProviderRequests = &n
		}
		d.Models = append(d.Models, m)
	}
	for i := range r.Days {
		d := &r.Days[i]
		if d.UnmappedLoggedRows != 0 {
			for j := range d.Models {
				d.Models[j].Comparable = false
				d.Models[j].InputDifference, d.Models[j].OutputDifference = nil, nil
			}
		}
		if len(d.Models) == 0 {
			d.ProviderRequests = nil
			if d.Status == "complete_day" {
				d.Status = "provider_counts_missing"
			}
		}
		if daily != nil {
			for _, reference := range daily.Days {
				if reference.Date == d.Date {
					d.LiteLLMDailyRequests = reference.APIRequests
				}
			}
		}
		d.LiteLLMTablesAgreeProviderDiffers = d.Status == "complete_day" && d.PopulationStatus == "matched" && b.contract.CountingUnitsMatch && d.LiteLLMDailyRequests != nil && d.LoggedRows == *d.LiteLLMDailyRequests && d.ProviderRequests != nil && *d.ProviderRequests != d.LoggedRows
	}
	slices.SortFunc(r.Days, func(a, b ProviderCoverageDay) int { return strings.Compare(a.Date, b.Date) })
	return r, nil
}
