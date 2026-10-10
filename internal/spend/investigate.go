// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/frequentitems"
	sketchhash "github.com/llm-measurement/llm-sketchkit/go/sketchkit/hash"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/hllpp"
)

const MaxModels = 128

type Options struct {
	BeforePeriod, AfterPeriod, GroupBy, SecretEnv string
	Top                                           int
	ShowHashes                                    bool
	Now                                           time.Time
}

type aggregate struct {
	window   Window
	cost     big.Rat
	top      *frequentitems.Sketch
	distinct *hllpp.Sketch
}

func newAggregate(p Period, rankings bool) *aggregate {
	a := &aggregate{window: Window{Period: p, Counters: map[string]uint64{}, RecordedSpend: "0"}}
	if rankings {
		a.top, _ = frequentitems.New(frequentitems.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
		a.distinct, _ = hllpp.New(hllpp.ProfileSmall, sketchhash.UserV1, sketchhash.HMACSHA25664)
	}
	return a
}

func (a *aggregate) add(row Row, obs accounting.Observation, q Quality) error {
	if err := accounting.MergeCounts(a.window.Counters, obs.Metrics); err != nil {
		return err
	}
	total, err := accounting.AddBounded(a.window.Tokens, obs.Metrics[accounting.Prefix+"total_tokens_total"])
	if err != nil {
		return err
	}
	a.window.Tokens = total
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
	if a.Quality.Unclear != 0 || b.Quality.Unclear != 0 {
		return nil
	}
	return compare.Decompose(a.Tokens, b.Tokens, a.Requests, b.Requests)
}

func secret(env string) ([]byte, string, error) {
	if env == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, "", errors.New("cannot generate a local hashing secret")
		}
		return b, "ephemeral", nil
	}
	if len(env) > 128 || len(os.Getenv(env)) > 4096 {
		return nil, "", errors.New("invalid hash secret environment setting")
	}
	if _, err := sketchhash.SecretFromEnv(env); err != nil {
		return nil, "", errors.New("hash secret missing or weak; use a random secret of at least 16 bytes")
	}
	return []byte(os.Getenv(env)), "environment", nil
}

// This is the same registered-domain HMAC construction as sketchkit/inspect.
// Length-framed components separate source and identity kinds inside that domain.
func identity(key []byte, kind string, parts ...string) ([32]byte, error) {
	canonical := make([]string, 0, len(parts)+2)
	canonical = append(canonical, "litellm-spend/v1", kind)
	for _, part := range parts {
		b, err := canon.CanonicalizeString(canon.TextV1, part)
		if err != nil {
			return [32]byte{}, errors.New("identity is not valid UTF-8")
		}
		canonical = append(canonical, string(b))
	}
	b, _ := json.Marshal(canonical)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(sketchhash.UserV1))
	mac.Write([]byte{0})
	mac.Write(b)
	return [32]byte(mac.Sum(nil)), nil
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
	if opts.GroupBy == "" {
		opts.GroupBy = "key"
	}
	if !slices.Contains([]string{"key", "team", "user", "end-user", "session"}, opts.GroupBy) || opts.Top < 1 || opts.Top > 100 {
		return Report{}, errors.New("group-by must be key, team, user, end-user or session; top must be 1-100")
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	key, mode, err := secret(opts.SecretEnv)
	if err != nil {
		return Report{}, err
	}
	defer clear(key)
	var earliest, latest time.Time
	firstDigest := sha256.New()
	automatic := opts.BeforePeriod == "" && opts.AfterPeriod == ""
	if automatic {
		enc := json.NewEncoder(firstDigest)
		err = Read(path, func(row Row) error {
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
			return enc.Encode(row.Values)
		})
		if err != nil {
			return Report{}, err
		}
	}
	before, after, err := selectPeriods(opts.BeforePeriod, opts.AfterPeriod, earliest, latest, opts.Now)
	if err != nil {
		return Report{}, err
	}
	windows := [2]*aggregate{newAggregate(before, true), newAggregate(after, true)}
	models := map[[32]byte][2]*aggregate{}
	seen := map[[32]byte]struct{}{}
	r := Report{Schema: "fleetdiff-litellm-spend/v1", AccountingID: accounting.ID, SourceContract: "litellm-spend-rows/v1", GroupBy: opts.GroupBy, Hashing: mode, Models: []Model{}, ZeroFilled: "cannot determine from this export", ProviderOrigin: "not declared by the supported SQL projection"}
	secondDigest := sha256.New()
	enc := json.NewEncoder(secondDigest)
	err = Read(path, func(row Row) error {
		r.Rows++
		if automatic {
			if e := enc.Encode(row.Values); e != nil {
				return e
			}
		}
		if row.Values["request_id"] == "" {
			return errors.New("each request-level row needs a nonempty request_id")
		}
		id := requestDigest(key, row.Values["request_id"])
		if _, ok := seen[id]; ok {
			return errors.New("duplicate request_id; export each request once from one consistent database snapshot")
		}
		seen[id] = struct{}{}
		start, e := timestamp(row.Values["startTime"])
		if e != nil {
			return e
		}
		if endText := row.Values["endTime"]; endText != "" {
			end, e := timestamp(endText)
			if e != nil {
				return e
			}
			if end.Before(start) {
				return errors.New("endTime precedes startTime; review the export")
			}
			if end.After(opts.Now) {
				return errors.New("export contains unfinished or future records; export completed requests")
			}
		}
		if operation(row.Values["call_type"]) == "" {
			r.ExcludedRows++
			return nil
		}
		side := -1
		if before.contains(start) {
			side = 0
		} else if after.contains(start) {
			side = 1
		}
		if side < 0 {
			r.OutsidePeriods++
			return nil
		}
		a, e := accounting.Tokens(attributes(row))
		if e != nil {
			return e
		}
		q := classify(row, a)
		if row.Values["endTime"] == "" {
			q.Unclear = 1
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
			// Match collector token-weight ranking: both primary fields must be present.
			if a.Metrics[accounting.Prefix+"missing_token_usage_total"] == 0 {
				weight := a.Metrics[accounting.Prefix+"total_tokens_total"]
				if e = w.top.AddHash(h, int64(weight)); e != nil {
					return errors.New("ranking weight exceeds supported range")
				}
				w.window.AttributedTokens += weight
			}
		}
		model, e := identity(key, "model", row.Values["model"])
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
		}
		return pair[side].add(row, a, q)
	})
	if err != nil {
		return Report{}, err
	}
	if automatic && !bytes.Equal(firstDigest.Sum(nil), secondDigest.Sum(nil)) {
		return Report{}, errors.New("export changed while reading; use a completed immutable export")
	}
	r.Before, r.After = windows[0].result(), windows[1].result()
	if r.Before.Requests == 0 || r.After.Requests == 0 {
		return Report{}, errors.New("each selected period needs a supported logged model request; choose explicit periods or review call_type")
	}
	r.Volume = volume(r.Before, r.After)
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
		return bytes.Compare(a.hash[:], b.hash[:])
	})
	for i, v := range ordered {
		m := Model{Item: fmt.Sprintf("model-%d", i+1), Before: v.pair[0].result(), After: v.pair[1].result()}
		m.Volume = volume(m.Before, m.After)
		if opts.ShowHashes {
			m.Hash = fmt.Sprintf("%x", v.hash)
		}
		r.Models = append(r.Models, m)
	}
	r.Rankings, err = compare.RankChanges("top_"+opts.GroupBy, windows[0].top, windows[1].top, compare.Options{Top: opts.Top, ShowHashes: opts.ShowHashes})
	if err != nil {
		return Report{}, err
	}
	r.Rankings.WeightUnit = "attributed-recorded-tokens"
	for i := range r.Rankings.Movers {
		r.Rankings.Movers[i].Item = fmt.Sprintf("%s-%d", opts.GroupBy, i+1)
	}
	r.Increase = increase(r.Rankings, r.Before.Tokens, r.After.Tokens)
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
