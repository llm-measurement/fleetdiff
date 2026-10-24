// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/attribution"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/fleetdiff/internal/reprice"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
)

const MaxModels = 128

type Options struct {
	BeforePeriod, AfterPeriod, GroupBy, SecretEnv string
	DailySpendPath                                string
	ProviderCountsPath, ProviderModelMapPath      string
	ProviderContractPath                          string
	ScopeRoute, ScopeAgent                        string
	RoutingAudit                                  bool
	RoutingBy                                     string
	Top                                           int
	ShowHashes                                    bool
	ShowLabels                                    bool
	Now                                           time.Time
}

type aggregate struct {
	window   Window
	cost     big.Rat
	top      *frequentitems.Sketch
	cacheTop *frequentitems.Sketch
	distinct *hllpp.Sketch
}

func newAggregate(p Period, rankings bool) *aggregate {
	a := &aggregate{window: Window{Period: p, Counters: map[string]uint64{}, RecordedSpend: "0"}}
	for _, d := range []*compare.CacheDetail{&a.window.Cache.Read, &a.window.Cache.Write} {
		d.Covered, d.Missing, d.Invalid, d.ZeroOriginUnknown = new(uint64), new(uint64), new(uint64), new(uint64)
	}
	if rankings {
		a.top, _ = frequentitems.New(frequentitems.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
		a.cacheTop, _ = frequentitems.New(frequentitems.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
		a.distinct, _ = hllpp.New(hllpp.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
	}
	return a
}

func (a *aggregate) add(row Row, obs accounting.Observation, q Quality) error {
	if err := addCache(&a.window.Cache, row, obs, q); err != nil {
		return err
	}
	if err := accounting.MergeCounts(a.window.Counters, obs.Metrics); err != nil {
		return err
	}
	total, err := accounting.AddBounded(a.window.Tokens, obs.Metrics[accounting.Prefix+"total_tokens_total"])
	if err != nil {
		return err
	}
	a.window.Tokens = total
	for _, subset := range []struct {
		count uint64
		dst   **FailureTokens
	}{
		{q.Failed, &a.window.FailureTokens},
		{q.ErrorObservations, &a.window.ErrorObservationTokens},
	} {
		if subset.count == 0 || obs.Metrics[accounting.Prefix+"total_tokens_total"] == 0 {
			continue
		}
		if *subset.dst == nil {
			*subset.dst = &FailureTokens{Finality: "unknown"}
		}
		f := *subset.dst
		for _, field := range []struct {
			dst   *uint64
			value uint64
		}{
			{&f.Records, 1},
			{&f.Input, obs.Metrics[accounting.Prefix+"input_tokens_total"]},
			{&f.Output, obs.Metrics[accounting.Prefix+"output_tokens_total"]},
			{&f.Total, obs.Metrics[accounting.Prefix+"total_tokens_total"]},
		} {
			n, err := accounting.AddBounded(*field.dst, field.value)
			if err != nil {
				return err
			}
			*field.dst = n
		}
	}
	a.window.Requests++
	if v := row.Values["spend"]; v == "" {
		q.SpendMissing = 1
	} else if n, ok := costValue(v); !ok {
		q.SpendInvalid = 1
	} else {
		a.cost.Add(&a.cost, n)
		if n.Sign() == 0 {
			q.SpendZeroUnknown = 1
		}
	}
	a.window.Quality.add(q)
	return nil
}

func (a *aggregate) result() Window {
	w := a.window
	w.RecordedSpend = costText(&a.cost)
	if a.distinct != nil {
		w.Distinct = a.distinct.Estimate()
		w.DistinctNominalRSE = 1.04 / 128
	}
	return w
}

func volume(a, b Window) *compare.VolumeChange {
	if a.Quality.Unclear != 0 || b.Quality.Unclear != 0 || a.Quality.UnknownStatus != 0 || b.Quality.UnknownStatus != 0 {
		return nil
	}
	return compare.Decompose(a.Tokens, b.Tokens, a.Requests, b.Requests)
}

func secret(env string) ([]byte, string, error) {
	return attribution.Secret(env)
}

// This is the same registered-domain HMAC construction as sketchkit/inspect.
// Length-framed components separate source and identity kinds inside that domain.
func identity(key []byte, kind string, parts ...string) ([32]byte, error) {
	return attribution.Identity(key, "litellm-spend/v1", kind, parts...)
}

func requestDigest(key []byte, id string) [32]byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("litellm-spend/request-id\x00"))
	mac.Write([]byte(id))
	return [32]byte(mac.Sum(nil))
}

func groupIdentity(row Row, group string, key []byte) ([32]byte, bool, error) {
	field := map[string]string{"key": "api_key", "team": "team_id", "user": "user", "end-user": "end_user", "session": "session_id"}[group]
	value := row.Values[field]
	if strings.TrimSpace(value) == "" {
		return [32]byte{}, false, nil
	}
	parts := []string{value}
	if group == "session" {
		if strings.TrimSpace(row.Values["api_key"]) == "" {
			return [32]byte{}, false, nil
		}
		parts = []string{row.Values["api_key"], value}
	}
	digest, err := identity(key, group, parts...)
	return digest, true, err
}

// Investigate reads request-level rows locally. It never exports collector
// envelopes: a logged request is a different observation unit from a provider send.
func Investigate(path string, opts Options) (Report, error) {
	return investigate(path, opts, nil)
}

// InvestigateRows composes an offline source with the existing investigation.
// The reader must replay the same completed snapshot on every pass.
func InvestigateRows(read func(func(Row) error) error, opts Options) (Report, error) {
	if read == nil || opts.DailySpendPath != "" || opts.RoutingAudit || opts.RoutingBy != "" || opts.ScopeRoute != "" || opts.ScopeAgent != "" || opts.ShowLabels || opts.ProviderCountsPath != "" || opts.ProviderModelMapPath != "" || opts.ProviderContractPath != "" {
		return Report{}, errors.New("row sources do not support SQL coverage, provider counts or routing options")
	}
	return investigate("", opts, func(_ string, visit func(Row) error) error { return read(visit) })
}

func investigate(path string, opts Options, source func(string, func(Row) error) error) (Report, error) {
	if opts.GroupBy == "" {
		opts.GroupBy = "key"
	}
	if !slices.Contains([]string{"key", "team", "user", "end-user", "session"}, opts.GroupBy) || opts.Top < 1 || opts.Top > 100 {
		return Report{}, errors.New("group-by must be key, team, user, end-user or session; top must be 1-100")
	}
	if err := validateRoutingOptions(&opts); err != nil {
		return Report{}, err
	}
	if opts.ProviderCountsPath != "" && (opts.ScopeRoute != "" || opts.ScopeAgent != "") {
		return Report{}, errors.New("provider coverage requires an unscoped spend export")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	key, mode, err := secret(opts.SecretEnv)
	if err != nil {
		return Report{}, err
	}
	defer clear(key)
	selection, err := newScopeSelection(opts, key)
	if err != nil {
		return Report{}, err
	}
	opts.ScopeRoute, opts.ScopeAgent = "", ""
	routing := newRoutingAccumulator(opts, selection)
	read := Read
	if routing != nil {
		read = ReadRouting
	}
	if source != nil {
		read = source
	}
	var earliest, latest time.Time
	firstDigest := sha256.New()
	automatic := opts.BeforePeriod == "" && opts.AfterPeriod == ""
	if automatic {
		enc := json.NewEncoder(firstDigest)
		err = read(path, func(row Row) error {
			t, e := timestamp(row.Values["startTime"])
			if e != nil {
				return e
			}
			if earliest.IsZero() || t.Before(earliest) {
				earliest = t
			}
			if latest.IsZero() || t.After(latest) {
				latest = t
			}
			return enc.Encode(row.replayProjection())
		})
		if err != nil {
			return Report{}, err
		}
	}
	before, after, err := selectPeriods(opts.BeforePeriod, opts.AfterPeriod, earliest, latest, opts.Now)
	if err != nil {
		return Report{}, err
	}
	coverage, err := newCoverage(opts.DailySpendPath, before, after)
	if err != nil {
		return Report{}, err
	}
	provider, err := newProviderCoverage(opts, before, after)
	if err != nil {
		return Report{}, err
	}
	windows := [2]*aggregate{newAggregate(before, true), newAggregate(after, true)}
	models := map[[32]byte][2]*aggregate{}
	modelOrder := map[[32]byte]int{}
	modelLabels := map[[32]byte]string{}
	// Sort full HMAC digests after reading instead of retaining a hash table.
	// This keeps duplicate detection exact at its existing 256-bit identity.
	var seen requestDigests
	callTypes := map[string]int{}
	r := Report{Schema: "fleetdiff-litellm-spend/v1", AccountingID: accounting.ID, SourceContract: "litellm-spend-rows/v1", GroupBy: opts.GroupBy, Hashing: mode, Models: []Model{}, ZeroFilled: "cannot determine from this export", ProviderOrigin: "not declared by the supported SQL projection"}
	secondDigest := sha256.New()
	enc := json.NewEncoder(secondDigest)
	err = read(path, func(row Row) error {
		r.Rows++
		if e := enc.Encode(row.replayProjection()); e != nil {
			return e
		}
		if row.Values["request_id"] == "" {
			return errors.New("each request-level row needs a nonempty request_id")
		}
		id := requestDigest(key, row.Values["request_id"])
		seen.add(id)
		start, e := timestamp(row.Values["startTime"])
		if e != nil {
			return e
		}
		side := -1
		if before.contains(start) {
			side = 0
		} else if after.contains(start) {
			side = 1
		}
		supported := operation(row.Values["call_type"]) != ""
		if !supported {
			r.ExcludedRows++
		}
		if side < 0 {
			r.OutsidePeriods++
			return nil
		}
		coverage.add(start)
		if err := provider.add(row, start); err != nil {
			return err
		}
		matched := routing.observe(row, side, key)
		// Validate completed supported rows even if the new scope excludes them.
		if !matched && supported {
			if e := validateRoutingEnd(row, start, opts.Now); e != nil {
				return e
			}
		}
		if !matched {
			return nil
		}
		if e := recordCallType(&r, callTypes, row, side); e != nil {
			return e
		}
		if !supported {
			r.Incomplete = true
			return routing.add(row, side, key, accounting.Observation{}, Quality{})
		}
		if e := validateRoutingEnd(row, start, opts.Now); e != nil {
			return e
		}
		a, e := accounting.Tokens(attributes(row))
		if e != nil {
			return e
		}
		q := classify(row, a)
		if row.Values["endTime"] == "" {
			q.Unclear = 1
		}
		if e := routing.add(row, side, key, a, q); e != nil {
			return e
		}
		group, present, e := groupIdentity(row, opts.GroupBy, key)
		if e != nil {
			return e
		}
		if !present {
			q.MissingIdentity = 1
		}
		w := windows[side]
		if e = w.add(row, a, q); e != nil {
			return e
		}
		if present {
			h := binary.BigEndian.Uint64(group[:8])
			w.distinct.AddHash(h)
			if validCacheField(a, "input") && validCacheField(a, "cache_read_input") {
				weight := a.Metrics[accounting.Prefix+"input_tokens_total"] - a.Metrics[accounting.Prefix+"cache_read_input_tokens_total"]
				if e = w.cacheTop.AddHash(h, int64(weight)); e != nil {
					return errors.New("cache ranking weight exceeds supported range")
				}
			}
			// Match collector token-weight ranking: both primary fields must be present.
			if a.Metrics[accounting.Prefix+"missing_token_usage_total"] == 0 {
				weight := a.Metrics[accounting.Prefix+"total_tokens_total"]
				if e = w.top.AddHash(h, int64(weight)); e != nil {
					return errors.New("ranking weight exceeds supported range")
				}
				w.window.AttributedTokens += weight
			}
		}
		rawModel := row.Values["model"]
		publicModel := reprice.PublicModelID(rawModel, row.Values["custom_llm_provider"])
		model, e := identity(key, "model", rawModel)
		if e != nil {
			return e
		}
		pair, ok := models[model]
		if !ok {
			if len(models) >= MaxModels {
				return errors.New("export exceeds 128 models; select a narrower export")
			}
			pair = [2]*aggregate{newAggregate(before, false), newAggregate(after, false)}
			models[model] = pair
			modelOrder[model] = row.Number
			modelLabels[model] = publicModel
		} else if modelLabels[model] != publicModel {
			// Hash-equivalent raw variants permanently keep this group private.
			modelLabels[model] = ""
		}
		return pair[side].add(row, a, q)
	})
	if err != nil {
		return Report{}, err
	}
	if seen.duplicated() {
		return Report{}, errors.New("duplicate request_id; export each request once from one consistent database snapshot")
	}
	seen = nil
	if automatic && !bytes.Equal(firstDigest.Sum(nil), secondDigest.Sum(nil)) {
		return Report{}, errors.New("export changed while reading; use a completed immutable export")
	}
	r.Before, r.After = windows[0].result(), windows[1].result()
	r.RowCoverage = coverage.result()
	r.ProviderCoverage, err = provider.result(r.RowCoverage)
	if err != nil {
		return Report{}, err
	}
	r.RoutingAudit, r.ScopeCoverage, err = routing.result(opts.Top, opts.ShowHashes)
	if err != nil {
		return Report{}, err
	}
	if r.Before.Requests == 0 && r.After.Requests == 0 {
		if r.RowCoverage != nil || r.ProviderCoverage != nil || routing != nil || source != nil {
			r.Incomplete = true
			r.AnalysisGap = "no analyzable model requests in either selected period"
			return r, nil
		}
		names := []string{}
		for _, c := range r.CallTypes {
			names = append(names, c.Name)
		}
		if len(names) == 0 {
			return Report{}, errors.New("no rows in either selected period; choose periods containing request-level rows")
		}
		return Report{}, fmt.Errorf("no analyzable model requests in either period; call types: %s; use Chat Completions, Messages or Responses request-level rows", strings.Join(names, ", "))
	}
	if !r.Incomplete {
		r.Volume = volume(r.Before, r.After)
	}
	type modelEntry struct {
		hash [32]byte
		pair [2]*aggregate
	}
	ordered := make([]modelEntry, 0, len(models))
	for h, p := range models {
		ordered = append(ordered, modelEntry{h, p})
	}
	slices.SortFunc(ordered, func(a, b modelEntry) int {
		da, db := int64(a.pair[1].window.Tokens)-int64(a.pair[0].window.Tokens), int64(b.pair[1].window.Tokens)-int64(b.pair[0].window.Tokens)
		if da > db {
			return -1
		}
		if da < db {
			return 1
		}
		return modelOrder[a.hash] - modelOrder[b.hash]
	})
	publicCounts := map[string]int{}
	for _, public := range modelLabels {
		if public != "" {
			publicCounts[public]++
		}
	}
	for i, v := range ordered {
		m := Model{Item: fmt.Sprintf("model-%d", i+1), Before: v.pair[0].result(), After: v.pair[1].result()}
		if public := modelLabels[v.hash]; public != "" {
			if publicCounts[public] > 1 {
				m.Item = public + " [" + m.Item + "]"
			} else {
				m.Item = public
			}
		}
		m.Volume = volume(m.Before, m.After)
		if opts.ShowHashes {
			m.Hash = fmt.Sprintf("%x", v.hash)
		}
		r.Models = append(r.Models, m)
	}
	r.Rankings, err = compare.RankCandidates("top_"+opts.GroupBy, windows[0].top, windows[1].top)
	if err != nil {
		return Report{}, err
	}
	r.Rankings.WeightUnit = "attributed-recorded-tokens"
	r.Cache = compare.CacheView{Before: r.Before.Cache, After: r.After.Cache}
	cacheRanks, err := compare.RankCandidates("uncached_"+opts.GroupBy, windows[0].cacheTop, windows[1].cacheTop)
	if err != nil {
		return Report{}, err
	}
	cacheRanks.WeightUnit = "attributed-recorded-uncached-input-tokens"
	r.Cache.Rankings = &cacheRanks
	if r.Before.Cache.Read.Covered == nil || r.After.Cache.Read.Covered == nil || *r.Before.Cache.Read.Covered == 0 || *r.After.Cache.Read.Covered == 0 {
		r.Cache.Rankings = nil
	}
	if err := orderCandidatesRead(path, &r, key, secondDigest.Sum(nil), selection, read); err != nil {
		return Report{}, err
	}
	if !r.Incomplete {
		r.Increase = increase(r.Rankings, r.Before.Tokens, r.After.Tokens)
	}
	r.Rankings.Movers = r.Rankings.Movers[:min(opts.Top, len(r.Rankings.Movers))]
	groups := [][]compare.Mover{r.Rankings.Movers}
	if r.Cache.Rankings != nil {
		r.Cache.Rankings.Movers = r.Cache.Rankings.Movers[:min(opts.Top, len(r.Cache.Rankings.Movers))]
		groups = append(groups, r.Cache.Rankings.Movers)
	}
	aliases := map[string]string{}
	for _, movers := range groups {
		for i := range movers {
			m := &movers[i]
			if aliases[m.Hash] == "" {
				aliases[m.Hash] = fmt.Sprintf("%s-%d", opts.GroupBy, len(aliases)+1)
			}
			m.Item = aliases[m.Hash]
			if !opts.ShowHashes {
				m.Hash = ""
			}
		}
	}
	r.Notes = []string{
		"Periods are half-open UTC intervals selected by request startTime; rows crossing a boundary remain in their start period.",
		"Counts describe supplied logged model requests. Export completeness and hidden provider retries are not established by spend rows.",
		"Zero-only rows have unknown origin. Failures overlap usage categories; valid counts on failures remain recorded.",
		"Aliases use local keyed hashes; session identities are scoped to their API key. A recorded session need not be an agent conversation.",
		"Recorded spend is the exact decimal sum of usable source values in USD, not an invoice. Default-zero costs have unknown origin.",
		"Rankings describe tracked candidates; concentration uses the net recorded increase and can exceed 100% when other contributors decreased.",
	}
	return r, nil
}

func increase(c compare.Concentration, before, after uint64) *IncreaseShare {
	if after <= before {
		return nil
	}
	candidates := slices.Clone(c.Movers)
	slices.SortFunc(candidates, func(a, b compare.Mover) int {
		if a.Delta.Lower > b.Delta.Lower {
			return -1
		}
		if a.Delta.Lower < b.Delta.Lower {
			return 1
		}
		return 0
	})
	out := &IncreaseShare{}
	lo, hi := new(big.Int), new(big.Int)
	for _, m := range candidates {
		if m.Delta.Lower <= 0 || out.Count == 2 {
			continue
		}
		out.Count++
		lo.Add(lo, big.NewInt(m.Delta.Lower))
		hi.Add(hi, big.NewInt(m.Delta.Upper))
	}
	if out.Count == 0 {
		return nil
	}
	// The sum of any group's positive changes cannot exceed all after tokens.
	upper := new(big.Int).SetUint64(after)
	if hi.Cmp(upper) > 0 {
		hi = upper
	}
	out.Delta = compare.Interval{Lower: lo.Int64(), Upper: hi.Int64()}
	out.Share = compare.Share{Lower: float64(out.Delta.Lower) / float64(after-before), Upper: float64(out.Delta.Upper) / float64(after-before)}
	return out
}
