// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"math/big"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/localfile"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

const maxDirectoryBytes = 256 << 20
const maxProjectionBytes = 64 << 20
const maxDirectoryFiles = 4096

var duplicateFields = [...]string{"level", "type", "start_time", "end_time", "total_cost", "usage_details", "provided_model_name", "user_id", "session_id", "environment", "release", "trace_name", "prompt_name", "tags", "accounting/grouping"}

// Fixed field hashes name a conflict without retaining or printing its values.
type projectionFingerprint [len(duplicateFields)][sha256.Size]byte

type Coverage struct {
	Manifests         int               `json:"manifests"`
	Projects          int               `json:"projects"`
	ListedFiles       int               `json:"listed_files"`
	VerifiedFiles     int               `json:"verified_files"`
	MissingFiles      int               `json:"missing_files"`
	PartialFiles      int               `json:"partial_files"`
	ExtraFiles        int               `json:"unmanifested_files"`
	PhysicalRows      uint64            `json:"physical_observation_rows"`
	UniqueRows        uint64            `json:"unique_observations"`
	Duplicates        uint64            `json:"duplicates_removed"`
	OtherTypes        map[string]uint64 `json:"other_observation_types"`
	GenerationRows    uint64            `json:"generation_observations"`
	UnknownCategories uint64            `json:"unmapped_category_occurrences"`
	MissingUsage      uint64            `json:"missing_usage_rows"`
	PartialUsage      uint64            `json:"partial_usage_rows"`
	InvalidUsage      uint64            `json:"invalid_usage_rows"`
	UsageZeroCost     uint64            `json:"usage_with_zero_recorded_cost_rows"`
	Gaps              int               `json:"manifest_schedule_gaps"`
	UncoveredPeriods  int               `json:"project_periods_not_fully_manifested"`
	Incomplete        bool              `json:"incomplete"`
}

type Report struct {
	spend.Report
	Coverage    Coverage `json:"langfuse_coverage"`
	TotalsScope string   `json:"totals_scope"`
}

type nativeRun struct {
	manifest Manifest
	base     string
}
type snapshot struct {
	rows     []spend.Row
	coverage Coverage
	windows  map[string][]spend.Period
}

func inventory(root *os.Root) (map[string]fs.FileInfo, error) {
	files := map[string]fs.FileInfo{}
	var total int64
	err := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || strings.Count(name, "/") > 12 || len(files) >= maxDirectoryFiles {
			return ErrInvalid
		}
		info, err := d.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
			return ErrInvalid
		}
		if !info.IsDir() {
			total += info.Size()
		}
		if total > maxDirectoryBytes {
			return ErrInvalid
		}
		files[name] = info
		return nil
	})
	if err != nil {
		return nil, ErrInvalid
	}
	return files, nil
}

// Each component is opened relative to a held root and checked against the
// inventory. No links, devices or pipes may become export streams.
func openFile(root *os.Root, names map[string]fs.FileInfo, name string) (*os.File, error) {
	if !localKey(name) {
		return nil, ErrInvalid
	}
	current := root
	var held []*os.Root
	defer func() {
		for _, r := range held {
			r.Close()
		}
	}()
	parts := strings.Split(name, "/")
	for i, part := range parts[:len(parts)-1] {
		info, err := current.Lstat(part)
		expected := names[strings.Join(parts[:i+1], "/")]
		if err != nil || expected == nil || !info.IsDir() || !os.SameFile(info, expected) {
			return nil, ErrInvalid
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return nil, ErrInvalid
		}
		held = append(held, next)
		current = next
	}
	expected := names[name]
	if expected == nil || !expected.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	f, err := current.OpenFile(parts[len(parts)-1], os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrInvalid
	}
	info, err := f.Stat()
	if err != nil || !sameFile(info, expected) {
		f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}

func sameFile(a, b fs.FileInfo) bool {
	return a != nil && b != nil && os.SameFile(a, b) && a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// Native object keys include a configured bucket prefix. Bind them to the
// project directory containing this manifest, not an arbitrary suffix match.
func resolveFile(run nativeRun, file File) (string, error) {
	marker := "/" + file.Table + "/"
	i := strings.LastIndex("/"+file.Key, marker)
	if i < 0 {
		return "", ErrInvalid
	}
	key := "/" + file.Key
	prefix, suffix := key[:i], key[i+1:]
	base := run.base
	if base == "." {
		if !strings.HasSuffix(prefix, "/"+run.manifest.ProjectID) {
			return "", ErrInvalid
		}
		return suffix, nil
	}
	if !strings.HasSuffix(prefix, "/"+base) {
		return "", ErrInvalid
	}
	if path.Base(base) != run.manifest.ProjectID {
		return "", ErrInvalid
	}
	return path.Join(base, suffix), nil
}

func loadDirectory(dir, group, tag string) (snapshot, error) {
	var out snapshot
	out.coverage.OtherTypes = map[string]uint64{}
	out.windows = map[string][]spend.Period{}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return out, ErrInvalid
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return out, ErrInvalid
	}
	defer root.Close()
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(info, rootInfo) {
		return out, ErrInvalid
	}
	names, err := inventory(root)
	if err != nil {
		return out, err
	}
	paths := make([]string, 0, len(names))
	for name := range names {
		paths = append(paths, name)
	}
	slices.Sort(paths)
	var runs []nativeRun
	for _, name := range paths {
		if path.Base(path.Dir(name)) != "manifests" || !strings.HasSuffix(name, ".json") || !names[name].Mode().IsRegular() {
			continue
		}
		if len(runs) >= 1000 {
			return out, ErrInvalid
		}
		if names[name].Size() > MaxManifestBytes {
			return out, ErrFileLimit
		}
		data, err := localfile.Read(root, name, MaxManifestBytes)
		if err != nil {
			return out, ErrInvalid
		}
		m, err := ParseManifest(bytes.NewReader(data))
		if err != nil {
			return out, err
		}
		runs = append(runs, nativeRun{m, path.Dir(path.Dir(name))})
	}
	if len(runs) == 0 {
		return out, errors.New("no completed Langfuse manifests; coverage is unavailable, not zero")
	}
	out.coverage.Manifests = len(runs)
	listed := map[string]bool{}
	seen := map[[32]byte]projectionFingerprint{}
	cost := new(big.Rat)
	projectionBytes := 0
	for _, run := range runs {
		complete := true
		hasObservations := false
		for _, file := range run.manifest.Files {
			name, err := resolveFile(run, file)
			if err != nil {
				return out, err
			}
			listed[name] = true
			out.coverage.ListedFiles++
			if file.Table == "observations_v2" {
				hasObservations = true
			}
			if names[name] == nil {
				out.coverage.MissingFiles++
				complete = false
				continue
			}
			if names[name].Size() > MaxFileBytes {
				return out, ErrFileLimit
			}
			if names[name].Size() != file.SizeBytes {
				out.coverage.PartialFiles++
				complete = false
				continue
			}
			f, err := openFile(root, names, name)
			if err != nil {
				return out, err
			}
			var pending []projection
			report, parseErr := readFile(f, file, func(p projection) error {
				project, _ := text(p.fields["project_id"])
				start, _ := text(p.fields["start_time"])
				t, e := nativeTime(start)
				// Adjacent native runs may both contain their boundary row.
				if project != run.manifest.ProjectID || e != nil || t.Before(run.manifest.Start) || t.After(run.manifest.End) {
					return ErrInvalid
				}
				pending = append(pending, p)
				return nil
			})
			f.Close()
			if parseErr != nil {
				if errors.Is(parseErr, ErrAccountingLimit) || errors.Is(parseErr, ErrFileLimit) {
					return out, parseErr
				}
				// A bad file contributes no rows or costs, including its valid prefix.
				out.coverage.PartialFiles++
				complete = false
				continue
			}
			out.coverage.VerifiedFiles++
			if file.Table != "observations_v2" {
				continue
			}
			out.coverage.PhysicalRows += report.PhysicalRows
			for _, p := range pending {
				row, id, fingerprint, err := mappedRow(p, group, tag)
				if err != nil {
					return out, err
				}
				if prior, ok := seen[id]; ok {
					if prior != fingerprint {
						for i, field := range duplicateFields {
							if prior[i] != fingerprint[i] {
								return out, errors.New("conflicting duplicate Langfuse observation field: " + field)
							}
						}
					}
					out.coverage.Duplicates++
					continue
				}
				if len(seen) >= MaxRows {
					return out, ErrInvalid
				}
				seen[id] = fingerprint
				row.Number = len(out.rows) + 1
				out.rows = append(out.rows, row)
				for k, v := range row.Values {
					projectionBytes += len(k) + len(v)
				}
				projectionBytes += len(row.ObservationLevel)
				if projectionBytes > maxProjectionBytes {
					return out, ErrInvalid
				}
				one := FileReport{OtherTypes: map[string]uint64{}, Components: map[string]uint64{}, ReportedFields: map[string]uint64{}}
				if err := one.add(p, cost); err != nil {
					return out, err
				}
				c := &out.coverage
				c.GenerationRows += one.GenerationRows
				for k, v := range one.OtherTypes {
					c.OtherTypes[k] += v
				}
				c.UnknownCategories += one.UnknownUsageCategories
				c.MissingUsage += one.MissingUsageRows
				c.PartialUsage += one.PartialUsageRows
				c.InvalidUsage += one.InvalidUsageRows
				c.UsageZeroCost += one.UsageWithZeroCostRows
			}
		}
		if !hasObservations {
			complete = false
			out.coverage.MissingFiles++
		}
		if _, ok := out.windows[run.manifest.ProjectID]; !ok {
			out.windows[run.manifest.ProjectID] = nil
		}
		if complete {
			out.windows[run.manifest.ProjectID] = append(out.windows[run.manifest.ProjectID], spend.Period{Start: run.manifest.Start, End: run.manifest.End})
		}
	}
	for _, name := range paths {
		if names[name].Mode().IsRegular() && !listed[name] && (strings.Contains("/"+name, "/observations_v2/") || strings.Contains("/"+name, "/scores/")) {
			out.coverage.ExtraFiles++
		}
	}
	after, err := inventory(root)
	if err != nil || len(after) != len(names) {
		return out, ErrInvalid
	}
	for name, prior := range names {
		if !sameFile(prior, after[name]) {
			return out, errors.New("Langfuse directory changed while reading; use a completed immutable copy")
		}
	}
	out.coverage.UniqueRows = uint64(len(out.rows))
	out.coverage.Projects = len(out.windows)
	for _, windows := range out.windows {
		slices.SortFunc(windows, func(a, b spend.Period) int { return a.Start.Compare(b.Start) })
		var end time.Time
		for _, w := range windows {
			if !end.IsZero() && w.Start.After(end) {
				out.coverage.Gaps++
			}
			if w.End.After(end) {
				end = w.End
			}
		}
	}
	out.coverage.Incomplete = out.coverage.MissingFiles+out.coverage.PartialFiles+out.coverage.ExtraFiles+out.coverage.Gaps > 0
	return out, nil
}

func nativeTime(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02 15:04:05.999999999", s)
	if err != nil || t.Year() < 1970 || t.Year() > 2200 {
		return time.Time{}, ErrInvalid
	}
	return t.UTC(), nil
}

func framed(parts ...string) string { b, _ := json.Marshal(parts); return string(b) }

func mappedRow(p projection, group, tag string) (spend.Row, [32]byte, projectionFingerprint, error) {
	row := spend.Row{Values: map[string]string{}, ObservationLevel: observationLevel(p.level)}
	v := row.Values
	project, e := text(p.fields["project_id"])
	if e != nil {
		return row, [32]byte{}, projectionFingerprint{}, e
	}
	id, e := text(p.fields["id"])
	if e != nil {
		return row, [32]byte{}, projectionFingerprint{}, e
	}
	digest := sha256.Sum256([]byte(framed(project, id)))
	v["request_id"] = hex.EncodeToString(digest[:])
	for native, column := range map[string]string{"start_time": "startTime", "end_time": "endTime"} {
		if s, err := text(p.fields[native]); err == nil {
			t, err := nativeTime(s)
			if err != nil {
				return row, digest, projectionFingerprint{}, err
			}
			v[column] = t.Format(time.RFC3339Nano)
		}
	}
	kind, err := text(p.fields["type"])
	if err != nil {
		return row, digest, projectionFingerprint{}, err
	}
	v["call_type"] = "langfuse_other"
	if kind == "GENERATION" {
		v["call_type"] = "completion"
	}
	if kind == "GENERATION" {
		for source, target := range map[string]string{"cache_read": "cache_read_input_tokens", "cache_write": "cache_write_input_tokens", "reasoning": "reasoning_output_tokens", "total": "total_tokens"} {
			n, ok, bad := token(p.usage[source])
			if ok {
				v[target] = new(big.Int).SetUint64(n).String()
			} else if bad {
				v[target] = "invalid"
			}
		}
		for target, parts := range map[string][]string{"prompt_tokens": {"input", "cache_read", "cache_write"}, "completion_tokens": {"output", "reasoning"}} {
			sum, known, bad := token(p.usage[parts[0]])
			bad = bad || p.badUsage
			for _, name := range parts[1:] {
				n, ok, invalid := token(p.usage[name])
				bad = bad || invalid
				if ok {
					sum, err = accounting.AddBounded(sum, n)
					if err != nil {
						return row, digest, projectionFingerprint{}, ErrAccountingLimit
					}
				}
			}
			if bad {
				v[target] = "invalid"
			} else if known {
				v[target] = new(big.Int).SetUint64(sum).String()
			}
		}
		if raw := strings.TrimSpace(string(p.fields["total_cost"])); raw != "" && raw != "null" {
			if n, ok := accounting.Cost(raw); ok {
				v["spend"] = accounting.CostText(n)
			} else {
				v["spend"] = "invalid"
			}
		}
	}
	for native, column := range map[string]string{"provided_model_name": "model", "user_id": "user", "session_id": "session_id"} {
		if s, err := text(p.fields[native]); err == nil {
			v[column] = s
		}
	}
	// All group identities are project-scoped; raw labels never reach output.
	if group == "agent" {
		row.AgentNamespace = project
		if raw := p.fields["tags"]; len(raw) > 0 {
			v["request_tags"] = string(raw)
		}
	}
	v["api_key"] = framed("langfuse", project)
	if group == "user_id" {
		if v["user"] != "" {
			v["user"] = framed(project, v["user"])
		}
	}
	if group != "user_id" && group != "session_id" {
		label, _ := text(p.fields[group])
		if group == "tag" {
			var tags []string
			raw := p.fields["tags"]
			if len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &tags) != nil {
				return row, digest, projectionFingerprint{}, ErrInvalid
			}
			if slices.Contains(tags, tag) {
				label = tag
			}
		}
		v["api_key"] = ""
		if label != "" {
			v["api_key"] = framed("langfuse", project, label)
		}
	}
	// Compare normalized approved fields, not encoding-specific raw JSON/CSV.
	canonical := map[string]any{"accounting/grouping": v, "type": kind, "level": row.ObservationLevel, "start_time": v["startTime"], "end_time": v["endTime"], "total_cost": v["spend"]}
	if group == "agent" {
		grouping := maps.Clone(v)
		if raw, ok := v["request_tags"]; ok {
			tags, err := spend.ParseRequestTags(raw)
			if err != nil {
				return row, digest, projectionFingerprint{}, err
			}
			slices.Sort(tags)
			normalized, _ := json.Marshal(tags)
			grouping["request_tags"] = string(normalized)
		}
		canonical["accounting/grouping"] = grouping
	}
	for _, name := range []string{"user_id", "session_id", "provided_model_name", "environment", "release", "trace_name", "prompt_name"} {
		s, _ := text(p.fields[name])
		canonical[name] = s
	}
	usage := map[string]string{}
	for _, name := range []string{"input", "output", "cache_read", "cache_write", "reasoning", "total"} {
		n, ok, bad := token(p.usage[name])
		if ok {
			usage[name] = new(big.Int).SetUint64(n).String()
		} else if bad {
			usage[name] = "invalid"
		}
	}
	canonical["usage_details"] = map[string]any{"components": usage, "unknown": p.unknown, "bad_usage": p.badUsage}
	var tags []string
	if raw := p.fields["tags"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &tags) != nil {
			return row, digest, projectionFingerprint{}, ErrInvalid
		}
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	if tags == nil {
		tags = []string{}
	}
	canonical["tags"] = tags
	var fingerprint projectionFingerprint
	for i, field := range duplicateFields {
		data, _ := json.Marshal(canonical[field])
		fingerprint[i] = sha256.Sum256(data)
	}
	return row, digest, fingerprint, nil
}

func covered(windows []spend.Period, p spend.Period) bool {
	end := p.Start
	for _, w := range windows {
		if w.Start.After(end) {
			return false
		}
		if w.End.After(end) {
			end = w.End
		}
		if !end.Before(p.End) {
			return true
		}
	}
	return false
}

func Investigate(dir, group, tag string, opts spend.Options) (Report, error) {
	if group == "" {
		group = "user_id"
	}
	if !slices.Contains([]string{"user_id", "session_id", "provided_model_name", "environment", "release", "trace_name", "prompt_name", "tag", "agent"}, group) || group == "tag" && (strings.TrimSpace(tag) == "" || len(tag) > MaxFieldBytes) || group != "tag" && tag != "" {
		return Report{}, errors.New("unsupported Langfuse group; tag grouping requires --langfuse-tag")
	}
	if group == "agent" && opts.AgentSource != "request-tag" {
		return Report{}, errors.New("Langfuse agent grouping requires agent-source request-tag")
	}
	check := opts
	check.GroupBy = group
	if err := spend.ValidateAgentOptions(check); err != nil {
		return Report{}, err
	}
	snap, err := loadDirectory(dir, group, tag)
	if err != nil {
		return Report{}, err
	}
	opts.GroupBy = map[string]string{"user_id": "user", "session_id": "session"}[group]
	if opts.GroupBy == "" {
		opts.GroupBy = "key"
	}
	if group == "agent" {
		opts.GroupBy = "agent"
	}
	opts.ModelAliases = group == "provided_model_name"
	r, err := spend.InvestigateRows(func(visit func(spend.Row) error) error {
		for _, row := range snap.rows {
			if row.Values["call_type"] != "completion" {
				continue
			}
			if err := visit(row); err != nil {
				return err
			}
		}
		return nil
	}, opts)
	if err != nil {
		return Report{}, err
	}
	for _, windows := range snap.windows {
		for _, p := range []spend.Period{r.Before.Period, r.After.Period} {
			if !covered(windows, p) {
				snap.coverage.UncoveredPeriods++
			}
		}
	}
	snap.coverage.Incomplete = snap.coverage.Incomplete || snap.coverage.UncoveredPeriods > 0
	r.Schema = "fleetdiff-langfuse-spend/v1"
	r.SourceContract = "langfuse-observations-v2/disjoint-usage-v1"
	r.GroupBy = group
	r.Rows = int(snap.coverage.UniqueRows)
	r.ExcludedRows = int(snap.coverage.UniqueRows - snap.coverage.GenerationRows)
	r.ProviderOrigin = "not established by Langfuse observations"
	r.Notes = []string{
		"Only GENERATION observations are analyzed as model calls; other observation types are counted separately.",
		"Periods are half-open UTC intervals selected by observation start_time. Duplicate IDs are scoped to project_id.",
		"Native input, cache-read and cache-creation are disjoint; inclusive input is their sum. Inclusive output includes disjoint reasoning.",
		"Recorded cost (Langfuse) is the exact sum of usable total_cost values, not an invoice. Zero cost with usage has unknown origin.",
		"Manifest coverage validates the supplied local copy, not upstream ingestion or hidden provider retries. Missing/partial files are not zero usage.",
		"Raw user, session and custom labels are suppressed with local keyed aliases. Status is unknown; status_message is never decoded.",
		"Level is an observation severity enum, not provider success or billing finality. DEFAULT/DEBUG/WARNING alone do not require usage review; ERROR and missing/invalid levels do. Other usage checks still apply. Per-request splits remain unavailable while call status is unknown.",
	}
	if opts.ModelAliases {
		r.Notes = append(r.Notes, "Model labels in contributor rankings match the model totals. Bracketed group aliases distinguish project-scoped contributors when the same model occurs in multiple projects; those contributors are not merged.")
	}
	for _, c := range []*compare.Concentration{&r.Rankings, r.Cache.Rankings} {
		if c != nil {
			for i := range c.Movers {
				n, ok := strings.CutPrefix(c.Movers[i].Item, opts.GroupBy+"-")
				if ok {
					c.Movers[i].Item = group + "-" + n
				}
			}
		}
	}
	if snap.coverage.Incomplete {
		r.Incomplete = true
		r.Volume = nil
		r.Increase = nil
		r.Cache.Rankings = nil
		for i := range r.Models {
			r.Models[i].Volume = nil
		}
	}
	scope := "observed GENERATION rows from verified local files; upstream completeness is not established"
	if snap.coverage.Incomplete {
		scope = "partial observed GENERATION rows from verified files only; missing or rejected files are not zero"
	}
	return Report{Report: r, Coverage: snap.coverage, TotalsScope: scope}, nil
}
