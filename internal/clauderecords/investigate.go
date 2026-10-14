// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package clauderecords

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/attribution"
	"github.com/llm-measurement/fleetdiff/internal/compare"
)

type Options struct {
	BeforePeriod, AfterPeriod, GroupBy, SecretEnv, SourceScope string
	Top                                                        int
	ShowHashes, DeduplicateExact                               bool
	Now                                                        time.Time
}

// Investigate compares log observations, not logical requests or provider sends.
// It never manufactures events for cancellation or infers a final receipt.
func Investigate(paths []string, opts Options) (Report, error) {
	if opts.GroupBy == "" {
		opts.GroupBy = "user"
	}
	if opts.GroupBy != "user" && opts.GroupBy != "session" || opts.Top < 1 || opts.Top > 100 {
		return Report{}, errors.New("Claude logs support group-by user or session and top 1-100")
	}
	if len(opts.SourceScope) > 256 || opts.SecretEnv != "" && strings.TrimSpace(opts.SourceScope) == "" {
		return Report{}, errors.New("a repeatable hashing secret requires an explicit source-scope (at most 256 bytes)")
	}
	data, err := load(paths)
	if err != nil {
		return Report{}, err
	}
	key, mode, err := attribution.Secret(opts.SecretEnv)
	if err != nil {
		return Report{}, err
	}
	defer clear(key)
	p, selection, err := periods(opts, data, key)
	if err != nil {
		return Report{}, err
	}
	c := &catalog{key: key, scope: opts.SourceScope, entries: map[uint64]label{}}
	w := [2]*aggregate{newAggregate(p[0], true), newAggregate(p[1], true)}
	r := Report{Schema: "fleetdiff-claude-code-records/v1", Unit: "observed api_request records", Finality: "unknown", AccountingID: accounting.ID, Hashing: mode, GroupBy: opts.GroupBy, Deduplication: "retain exact copies", Models: []Model{}, ComparisonChecks: []string{}}
	r.PeriodSelection = selection
	r.InputFiles = len(paths)
	if opts.DeduplicateExact {
		r.Deduplication = "remove exact exported copies only"
	}
	seen := map[[32]byte]struct{}{}
	prompts := map[[32]byte][2]uint64{}
	models := map[[32]byte][2]*aggregate{}
	var modelOrder [][32]byte
	profiles := [2]map[[32]byte]string{{}, {}}
	r.ReadRecords, r.IgnoredEvents, err = read(data, key, func(row record) error {
		side := -1
		for i, period := range p {
			if !row.time.Before(period.Start) && row.time.Before(period.End) {
				side = i
				break
			}
		}
		if side < 0 {
			r.OutsidePeriods++
			return nil
		}
		a := w[side]
		if _, ok := seen[row.fingerprint]; ok {
			a.w.DuplicateCopies++
			if opts.DeduplicateExact {
				return nil
			}
		}
		seen[row.fingerprint] = struct{}{}
		switch row.event {
		case "claude_code.api_error":
			a.w.ErrorEvents++
			a.w.ErrorStatus[errorStatus(row)]++
			return nil
		case "claude_code.api_retries_exhausted":
			a.w.ExhaustedEvents++
			return nil
		}
		o := normalize(row)
		if err := a.add(o); err != nil {
			return err
		}
		unverified := false
		for j, name := range []string{"service.version", "fleetdiff.capture.profile"} {
			alias := "unavailable"
			if v, ok := row.text(name); ok {
				d := attribution.Digest(key, name, []byte(v))
				alias = profiles[j][d]
				if alias == "" {
					if len(profiles[j]) >= MaxProfiles {
						return errors.New("capture exceeds 128 version/profile values")
					}
					alias = fmt.Sprintf("source-%d", len(profiles[j])+1)
					profiles[j][d] = alias
				}
				if j == 0 && v != "2.1.296" || j == 1 && v != "claude-code-logs/v1" {
					unverified = true
				}
			} else {
				unverified = true
			}
			if j == 0 {
				a.w.Versions[alias]++
			} else {
				a.w.Profiles[alias]++
			}
		}
		if unverified {
			a.w.Quality["source_profile_unverified"]++
		}
		h, kind, ok, err := c.group(row, opts.GroupBy)
		if err != nil {
			return err
		}
		a.w.IdentityKinds[kind]++
		coverage := a.w.IdentityCoverage[kind]
		coverage.Records++
		if o.total != nil {
			coverage.TokenRecords++
		}
		if o.cost != nil {
			coverage.CostRecords++
		}
		a.w.IdentityCoverage[kind] = coverage
		if _, present := row.resolve("identity.source"); present {
			a.w.Quality["unverified_identity_regime"]++
		}
		if !ok {
			a.w.Unattributed++
		} else {
			if o.total != nil {
				if err := a.top.AddHash(h, int64(*o.total)); err != nil {
					return errors.New("cannot add token ranking weight")
				}
			}
			if o.input != nil {
				if err := a.cacheTop.AddHash(h, int64(*o.input-*o.read)); err != nil {
					return errors.New("cannot add uncached ranking weight")
				}
			}
		}
		if d, ok, err := c.prompt(row); err != nil {
			return err
		} else if ok {
			counts, exists := prompts[d]
			if !exists && len(prompts) >= MaxIdentities {
				return errors.New("capture exceeds 250000 scoped prompts; select a smaller capture")
			}
			if counts[side] == 0 {
				a.w.Prompts++
			}
			if counts[side] == 1 {
				a.w.MultipleRecordPrompts++
			}
			counts[side]++
			prompts[d] = counts
			a.w.PromptRecords++
		}
		model, ok := row.text("model")
		d := attribution.Digest(key, "model", []byte(model))
		if !ok {
			a.w.Quality["model_missing"]++
			return nil
		}
		m, exists := models[d]
		if !exists {
			if len(models) >= MaxProfiles {
				return errors.New("capture exceeds 128 model values")
			}
			m = [2]*aggregate{newAggregate(p[0], false), newAggregate(p[1], false)}
			models[d] = m
			modelOrder = append(modelOrder, d)
		}
		return m[side].add(o)
	})
	if err != nil {
		return Report{}, err
	}
	r.Before, r.After = w[0].result(), w[1].result()
	if r.Before.Records+r.After.Records+r.Before.ErrorEvents+r.After.ErrorEvents+r.Before.ExhaustedEvents+r.After.ExhaustedEvents == 0 {
		return Report{}, errors.New("no recognized Claude API events in the selected periods")
	}
	for i, d := range modelOrder {
		m := models[d]
		r.Models = append(r.Models, Model{fmt.Sprintf("model-%d", i+1), m[0].result(), m[1].result()})
	}
	r.Rankings, err = c.rank("observed_tokens", w[0], w[1], false, opts)
	if err != nil {
		return Report{}, err
	}
	r.UncachedRankings, err = c.rank("uncached_input", w[0], w[1], true, opts)
	if err != nil {
		return Report{}, err
	}
	r.ComparisonChecks = checks(r.Before, r.After, opts.DeduplicateExact)
	if len(r.ComparisonChecks) == 0 && r.Before.Total.Value != nil && r.After.Total.Value != nil {
		v := compare.Decompose(*r.Before.Total.Value, *r.After.Total.Value, r.Before.Records, r.After.Records)
		if v != nil {
			r.Decomposition = &Decomposition{v.RequestContribution, v.TokensPerRequestContribution}
		}
	}
	return r, nil
}

func sameMix(a, b map[string]uint64, n, m uint64) bool {
	for k, v := range a {
		if v*m != b[k]*n {
			return false
		}
	}
	for k, v := range b {
		if v*n != a[k]*m {
			return false
		}
	}
	return true
}
func checks(a, b Window, dedup bool) []string {
	var out = []string{}
	if a.Records == 0 || b.Records == 0 {
		out = append(out, "period_without_api_request_records")
	}
	if a.Total.Records != a.Records || b.Total.Records != b.Records {
		out = append(out, "incomplete_token_components")
	}
	if a.Quality["source_profile_unverified"]+b.Quality["source_profile_unverified"] > 0 {
		out = append(out, "unverified_source_profile")
	}
	if !sameMix(a.Versions, b.Versions, a.Records, b.Records) {
		out = append(out, "source_version_mix_changed")
	}
	if !sameMix(a.Profiles, b.Profiles, a.Records, b.Records) {
		out = append(out, "capture_profile_mix_changed")
	}
	if !sameMix(a.IdentityKinds, b.IdentityKinds, a.Records, b.Records) {
		out = append(out, "identity_source_mix_changed")
	}
	if a.PromptRecords != a.Records || b.PromptRecords != b.Records {
		out = append(out, "incomplete_prompt_identity")
	}
	if a.PromptRecords*b.Prompts != b.PromptRecords*a.Prompts || a.MultipleRecordPrompts*b.Prompts != b.MultipleRecordPrompts*a.Prompts {
		out = append(out, "records_per_prompt_changed")
	}
	if a.ErrorEvents*b.Records != b.ErrorEvents*a.Records || a.ExhaustedEvents*b.Records != b.ExhaustedEvents*a.Records {
		out = append(out, "error_or_exhaustion_mix_changed")
	}
	if !sameMix(a.ErrorStatus, b.ErrorStatus, a.ErrorEvents, b.ErrorEvents) {
		out = append(out, "error_status_mix_changed")
	}
	if a.Unattributed*b.Records != b.Unattributed*a.Records {
		out = append(out, "attribution_coverage_changed")
	}
	if !dedup && a.DuplicateCopies+b.DuplicateCopies > 0 {
		out = append(out, "exact_export_copies_retained")
	}
	return out
}

func errorStatus(r record) string {
	v, ok := r.resolve("status_code")
	if !ok {
		return "missing"
	}
	n, ok := number(v)
	if !ok || n < 100 || n > 599 {
		return "invalid"
	}
	if n == 429 {
		return "http_429"
	}
	if n >= 500 {
		return "http_5xx"
	}
	if n >= 400 {
		return "other_http_4xx"
	}
	return "other_http_status"
}
