// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
)

// Exact session review has separate identity-state caps from the streaming reader.
const MaxSessions = 250_000
const MaxSessionMemberships = 1_000_000

type SessionOptions struct {
	Period, BudgetUSD, SecretEnv string
	CounterWindow                time.Duration
	Top                          int
	ShowHashes                   bool
	Now                          time.Time
}

type Distribution struct {
	N   uint64  `json:"n"`
	P50 *string `json:"p50"`
	P95 *string `json:"p95"`
	Max *string `json:"max"`
}

type Session struct {
	Alias             string            `json:"alias"`
	Hash              string            `json:"hash,omitempty"`
	Calls             uint64            `json:"logged_model_calls"`
	Tokens            uint64            `json:"recorded_tokens"`
	RecordedSpend     string            `json:"recorded_spend_usd"`
	SpendComplete     bool              `json:"spend_complete"`
	MissingSpend      uint64            `json:"missing_spend_rows"`
	InvalidSpend      uint64            `json:"invalid_spend_rows"`
	UnanalyzedRows    uint64            `json:"unanalyzed_rows"`
	Rejections        map[string]uint64 `json:"recorded_rejections"`
	RejectionCoverage bool              `json:"rejection_classification_complete"`
	Censored          bool              `json:"recorded_budget_or_iteration_rejection"`
	PossiblyClipped   bool              `json:"possibly_clipped"`
	SpendSpanSeconds  *float64          `json:"positive_spend_span_seconds"`
	Keys              uint64            `json:"keys"`
	Teams             uint64            `json:"teams"`
	Agents            uint64            `json:"agents"`
}

type SessionReport struct {
	Schema                     string            `json:"schema"`
	Grouping                   string            `json:"grouping"`
	Period                     Period            `json:"period"`
	AutomaticPeriod            bool              `json:"automatic_period"`
	Hashing                    string            `json:"hashing"`
	BudgetUSD                  *string           `json:"supplied_budget_usd"`
	CounterWindowSeconds       float64           `json:"counter_window_seconds"`
	Rows                       uint64            `json:"rows"`
	SelectedRows               uint64            `json:"selected_rows"`
	UnsupportedRows            uint64            `json:"unsupported_rows"`
	UnattributedRows           uint64            `json:"unattributed_rows"`
	UnattributedSpend          string            `json:"unattributed_recorded_spend_usd"`
	UnattributedSpendRows      uint64            `json:"unattributed_spend_rows"`
	UnattributedMissingSpend   uint64            `json:"unattributed_missing_spend_rows"`
	UnattributedInvalidSpend   uint64            `json:"unattributed_invalid_spend_rows"`
	ClassifiedRows             uint64            `json:"rejection_classified_rows"`
	FailureLoggingCompleteness string            `json:"failure_logging_completeness"`
	UnclassifiedFailures       uint64            `json:"unclassified_failure_rows"`
	Sessions                   uint64            `json:"sessions_with_model_calls"`
	ModelCalls                 uint64            `json:"logged_model_calls_total"`
	RejectionOnlySessions      uint64            `json:"rejection_only_sessions"`
	UnanalyzedOnlySessions     uint64            `json:"sessions_without_analyzed_calls"`
	Singletons                 uint64            `json:"singleton_sessions"`
	PossiblyClipped            uint64            `json:"possibly_clipped_sessions"`
	Censored                   uint64            `json:"sessions_with_recorded_budget_or_iteration_rejection"`
	CensoredSpendN             uint64            `json:"rejection_affected_spend_distribution_n"`
	CensoredCallsN             uint64            `json:"rejection_affected_call_distribution_n"`
	Spend                      Distribution      `json:"complete_recorded_spend_usd"`
	Calls                      Distribution      `json:"logged_model_calls"`
	Quality                    Quality           `json:"usage_quality"`
	RecordedTokens             uint64            `json:"recorded_tokens"`
	Rejections                 map[string]uint64 `json:"recorded_rejections"`
	Largest                    []Session         `json:"largest_sessions"`
	Exceeding                  uint64            `json:"complete_sessions_exceeding_cap"`
	PartialExceeding           uint64            `json:"partial_subtotals_exceeding_cap"`
	NoRecordedRejection        *uint64           `json:"exceeding_without_recorded_session_budget_rejection"`
	CapReview                  []Session         `json:"cap_review"`
	LongSpans                  uint64            `json:"sessions_spanning_counter_window"`
	LongSpanReview             []Session         `json:"counter_window_review"`
	SharedCounters             uint64            `json:"sessions_under_multiple_identities"`
	SharedCounterReview        []Session         `json:"shared_counter_review"`
}

type sessionState struct {
	Session
	id          [32]byte
	order       int
	cost        big.Rat
	first, last time.Time
}

type sessionMember struct {
	session  [32]byte
	identity [32]byte
	kind     uint8
}

func sessionPeriod(opts SessionOptions, first, last time.Time) (Period, error) {
	if opts.Period != "" {
		p, err := ParsePeriod(opts.Period)
		if err != nil {
			return p, err
		}
		if p.End.After(opts.Now) {
			return p, errors.New("select a completed period ending before now")
		}
		return p, nil
	}
	start := first.Truncate(24 * time.Hour)
	if !start.Equal(first) {
		start = start.AddDate(0, 0, 1)
	}
	end := last.Truncate(24 * time.Hour)
	if today := opts.Now.UTC().Truncate(24 * time.Hour); end.After(today) {
		end = today
	}
	if first.IsZero() || !start.Before(end) {
		return Period{}, errors.New("no complete UTC days in the file; supply --period (a date or RFC3339 start/end interval)")
	}
	return Period{start, end}, nil
}

// ReviewSessions follows the pinned limiter's literal session-ID counter unit.
// It keeps no raw identifiers in the result and does not infer task boundaries.
func ReviewSessions(path string, opts SessionOptions) (SessionReport, error) {
	if opts.Top < 1 || opts.Top > 100 {
		return SessionReport{}, errors.New("top must be 1-100")
	}
	if opts.CounterWindow == 0 {
		opts.CounterWindow = time.Hour
	}
	if opts.CounterWindow < time.Second || opts.CounterWindow > 365*24*time.Hour {
		return SessionReport{}, errors.New("session-counter-window must be between 1s and 8760h")
	}
	var cap *big.Rat
	if opts.BudgetUSD != "" {
		var ok bool
		cap, ok = costValue(opts.BudgetUSD)
		if !ok || cap.Sign() <= 0 {
			return SessionReport{}, errors.New("session-budget-usd must be a positive decimal no greater than 1e18")
		}
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	}
	key, mode, err := secret(opts.SecretEnv)
	if err != nil {
		return SessionReport{}, err
	}
	defer clear(key)
	var first, last time.Time
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	if err := Read(path, func(row Row) error {
		t, err := timestamp(row.Values["startTime"])
		if err != nil {
			return err
		}
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if last.IsZero() || t.After(last) {
			last = t
		}
		return encoder.Encode(row.Values)
	}); err != nil {
		return SessionReport{}, err
	}
	p, err := sessionPeriod(opts, first, last)
	if err != nil {
		return SessionReport{}, err
	}
	r := SessionReport{Schema: "fleetdiff-litellm-session-budget/v1", Grouping: "session_id_only", Period: p, AutomaticPeriod: opts.Period == "", Hashing: mode, CounterWindowSeconds: opts.CounterWindow.Seconds(), Rejections: map[string]uint64{}, Largest: []Session{}, CapReview: []Session{}, LongSpanReview: []Session{}, SharedCounterReview: []Session{}}
	r.FailureLoggingCompleteness = "unknown"
	if cap != nil {
		value := costText(cap)
		r.BudgetUSD = &value
	}
	states := map[[32]byte]*sessionState{}
	members := map[sessionMember]struct{}{}
	var seen requestDigests
	var unattributedSpend big.Rat
	second := sha256.New()
	encoder = json.NewEncoder(second)
	err = Read(path, func(row Row) error {
		r.Rows++
		if err := encoder.Encode(row.Values); err != nil {
			return err
		}
		v := row.Values
		if v["request_id"] == "" {
			return errors.New("each request-level row needs a nonempty request_id")
		}
		seen.add(requestDigest(key, v["request_id"]))
		t, err := timestamp(v["startTime"])
		if err != nil {
			return err
		}
		if !p.contains(t) {
			return nil
		}
		r.SelectedRows++
		_, classified := v["rejection_kind"]
		if classified {
			r.ClassifiedRows++
		}
		rejection := v["rejection_kind"]
		if rejection == "" && v["status"] == "failure" {
			r.UnclassifiedFailures++
		}
		switch rejection {
		case "":
		case "session_budget", "session_iterations", "other_budget":
			if v["status"] != "failure" {
				return errors.New("rejection_kind requires failure status; rerun the pinned SQL export")
			}
			r.Rejections[rejection]++
		default:
			return errors.New("unsupported rejection_kind; use the pinned SQL export")
		}
		unsupported := rejection == "" && operation(v["call_type"]) == ""
		if unsupported {
			r.UnsupportedRows++
		}
		if rejection == "" && !unsupported {
			r.ModelCalls++
		}
		if v["endTime"] != "" && !unsupported {
			end, e := timestamp(v["endTime"])
			if e != nil {
				return e
			}
			if end.Before(t) || end.After(opts.Now) {
				return errors.New("selected records have unfinished or invalid endTime; export completed requests")
			}
		}
		var obs accounting.Observation
		var q Quality
		amount, validSpend := costValue(v["spend"])
		if rejection == "" && !unsupported {
			obs, err = accounting.Tokens(attributes(row))
			if err != nil {
				return err
			}
			q = classify(row, obs)
			if v["spend"] == "" {
				q.SpendMissing = 1
			} else if !validSpend {
				q.SpendInvalid = 1
			} else if amount.Sign() == 0 {
				q.SpendZeroUnknown = 1
			}
			if v["session_id"] == "" {
				q.MissingIdentity = 1
			}
			if v["endTime"] == "" {
				q.Unclear = 1
			}
			r.Quality.add(q)
			r.RecordedTokens, err = accounting.AddBounded(r.RecordedTokens, obs.Metrics[accounting.Prefix+"total_tokens_total"])
			if err != nil {
				return err
			}
		}
		if v["session_id"] == "" {
			r.UnattributedRows++
			if rejection == "" && !unsupported {
				if q.SpendMissing != 0 {
					r.UnattributedMissingSpend++
				} else if q.SpendInvalid != 0 {
					r.UnattributedInvalidSpend++
				} else {
					r.UnattributedSpendRows++
					unattributedSpend.Add(&unattributedSpend, amount)
				}
			}
			return nil
		}
		// Domain-separate literal identifiers; do not normalize the limiter's key.
		id := requestDigest(key, "session-budget/session\x00"+v["session_id"])
		s := states[id]
		if s == nil {
			if len(states) == MaxSessions {
				return errors.New("session review exceeds 250,000 sessions; select a shorter period")
			}
			s = &sessionState{Session: Session{SpendComplete: true, RejectionCoverage: true, Rejections: map[string]uint64{}}, id: id, order: row.Number}
			states[id] = s
		}
		s.RejectionCoverage = s.RejectionCoverage && classified
		for i, field := range []string{"api_key", "team_id", "agent_id"} {
			if v[field] == "" {
				continue
			}
			m := sessionMember{id, requestDigest(key, "session-budget/"+field+"\x00"+v[field]), uint8(i)}
			if _, ok := members[m]; !ok {
				if len(members) == MaxSessionMemberships {
					return errors.New("session review exceeds 1,000,000 identity memberships; select a shorter period")
				}
				members[m] = struct{}{}
				switch i {
				case 0:
					s.Keys++
				case 1:
					s.Teams++
				case 2:
					s.Agents++
				}
			}
		}
		if rejection != "" {
			s.Rejections[rejection]++
			s.Censored = true
			return nil
		}
		if unsupported {
			s.UnanalyzedRows++
			s.SpendComplete = false
			return nil
		}
		s.Calls++
		s.Tokens, err = accounting.AddBounded(s.Tokens, obs.Metrics[accounting.Prefix+"total_tokens_total"])
		if err != nil {
			return err
		}
		if v["spend"] == "" {
			s.MissingSpend++
		} else if !validSpend {
			s.InvalidSpend++
		} else {
			s.cost.Add(&s.cost, amount)
			if amount.Sign() > 0 {
				if s.first.IsZero() || t.Before(s.first) {
					s.first = t
				}
				if s.last.IsZero() || t.After(s.last) {
					s.last = t
				}
			}
			if t.Truncate(24*time.Hour).Equal(p.Start.Truncate(24*time.Hour)) || t.Truncate(24*time.Hour).Equal(p.End.Add(-time.Nanosecond).Truncate(24*time.Hour)) {
				s.PossiblyClipped = true
			}
		}
		s.SpendComplete = s.MissingSpend+s.InvalidSpend+s.UnanalyzedRows == 0
		return nil
	})
	if err != nil {
		return SessionReport{}, err
	}
	if !bytes.Equal(digest.Sum(nil), second.Sum(nil)) {
		return SessionReport{}, errors.New("spend file changed while reading; retry with a stable export")
	}
	if seen.duplicated() {
		return SessionReport{}, errors.New("duplicate request_id; export unique request-level rows")
	}
	if r.SelectedRows == 0 {
		return SessionReport{}, errors.New("no records in the selected period")
	}
	r.UnattributedSpend = costText(&unattributedSpend)
	return finishSessions(r, states, opts, cap), nil
}

func finishSessions(r SessionReport, states map[[32]byte]*sessionState, opts SessionOptions, cap *big.Rat) SessionReport {
	ordered := make([]*sessionState, 0, len(states))
	for _, s := range states {
		ordered = append(ordered, s)
	}
	slices.SortFunc(ordered, func(a, b *sessionState) int {
		if c := b.cost.Cmp(&a.cost); c != 0 {
			return c
		}
		return a.order - b.order
	})
	var spends, calls []*big.Rat
	if cap != nil && r.ClassifiedRows == r.SelectedRows {
		r.NoRecordedRejection = new(uint64)
	}
	for i, s := range ordered {
		s.Alias = fmt.Sprintf("session-%d", i+1)
		if opts.ShowHashes {
			s.Hash = fmt.Sprintf("%x", s.id)
		}
		s.RecordedSpend = costText(&s.cost)
		if !s.first.IsZero() {
			seconds := s.last.Sub(s.first).Seconds()
			s.SpendSpanSeconds = &seconds
		}
		if s.Calls == 0 {
			if s.UnanalyzedRows != 0 {
				r.UnanalyzedOnlySessions++
			} else {
				r.RejectionOnlySessions++
			}
		} else {
			r.Sessions++
			calls = append(calls, new(big.Rat).SetInt(new(big.Int).SetUint64(s.Calls)))
			if s.SpendComplete {
				spends = append(spends, &s.cost)
			}
			if s.Calls == 1 {
				r.Singletons++
			}
			if len(r.Largest) < opts.Top {
				r.Largest = append(r.Largest, s.Session)
			}
		}
		if s.PossiblyClipped {
			r.PossiblyClipped++
		}
		if s.Censored {
			r.Censored++
			if s.Calls > 0 {
				r.CensoredCallsN++
				if s.SpendComplete {
					r.CensoredSpendN++
				}
			}
		}
		if cap != nil && s.cost.Cmp(cap) > 0 {
			if s.SpendComplete {
				r.Exceeding++
				if r.NoRecordedRejection != nil && s.Rejections["session_budget"] == 0 {
					(*r.NoRecordedRejection)++
				}
			} else {
				r.PartialExceeding++
			}
			if len(r.CapReview) < opts.Top {
				r.CapReview = append(r.CapReview, s.Session)
			}
		}
		if s.SpendSpanSeconds != nil && *s.SpendSpanSeconds > r.CounterWindowSeconds {
			r.LongSpans++
			if len(r.LongSpanReview) < opts.Top {
				r.LongSpanReview = append(r.LongSpanReview, s.Session)
			}
		}
		if s.Keys > 1 || s.Teams > 1 || s.Agents > 1 {
			r.SharedCounters++
			if len(r.SharedCounterReview) < opts.Top {
				r.SharedCounterReview = append(r.SharedCounterReview, s.Session)
			}
		}
	}
	r.Spend = distribution(spends)
	r.Calls = distribution(calls)
	return r
}

func distribution(values []*big.Rat) Distribution {
	d := Distribution{N: uint64(len(values))}
	if len(values) == 0 {
		return d
	}
	slices.SortFunc(values, func(a, b *big.Rat) int { return a.Cmp(b) })
	p50, p95, max := costText(values[(len(values)*50+99)/100-1]), costText(values[(len(values)*95+99)/100-1]), costText(values[len(values)-1])
	d.P50, d.P95, d.Max = &p50, &p95, &max
	return d
}
