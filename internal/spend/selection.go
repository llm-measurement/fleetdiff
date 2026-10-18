// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

type scopeSelection struct {
	kind string
	hash [32]byte
}

func validateRoutingOptions(opts *Options) error {
	if opts.RoutingBy != "" && !opts.RoutingAudit {
		return errors.New("routing-by requires routing-audit")
	}
	if opts.RoutingAudit && opts.RoutingBy == "" {
		opts.RoutingBy = "route"
	}
	if opts.RoutingAudit && opts.RoutingBy != "route" && opts.RoutingBy != "route-agent" {
		return errors.New("routing-by must be route or route-agent")
	}
	if opts.ScopeRoute != "" && opts.ScopeAgent != "" {
		return errors.New("select only one route or agent scope")
	}
	if opts.ScopeRoute == "" && opts.ScopeAgent == "" {
		return nil
	}
	if opts.BeforePeriod == "" || opts.AfterPeriod == "" {
		return errors.New("route and agent scopes require explicit before-period and after-period")
	}
	if opts.DailySpendPath != "" || opts.GroupBy == "session" {
		return errors.New("route and agent scopes cannot use daily spend or session grouping")
	}
	for _, value := range []string{opts.ScopeRoute, opts.ScopeAgent} {
		if value != "" && (!utf8.ValidString(value) || len(value) > MaxFieldBytes || strings.TrimSpace(value) == "") {
			return errors.New("scope identity must be nonblank UTF-8 text of at most 8192 bytes")
		}
	}
	return nil
}

func newScopeSelection(opts Options, key []byte) (*scopeSelection, error) {
	kind, value := "route", opts.ScopeRoute
	if opts.ScopeAgent != "" {
		kind, value = "agent", opts.ScopeAgent
	}
	if value == "" {
		return nil, nil
	}
	hash, err := identity(key, kind, value)
	if err != nil {
		return nil, err
	}
	return &scopeSelection{kind: kind, hash: hash}, nil
}

func scopeField(kind string) string {
	if kind == "route" {
		return "original_model_group"
	}
	return "agent_id"
}

func (s *scopeSelection) match(row Row, key []byte) (matched, known bool) {
	if s == nil {
		return true, true
	}
	field := scopeField(s.kind)
	if routingTextState(row, field) != routingKnown {
		return false, false
	}
	hash, err := identity(key, s.kind, row.Values[field])
	return err == nil && hash == s.hash, err == nil
}

func validateRoutingEnd(row Row, start, now time.Time) error {
	if text := row.Values["endTime"]; text != "" {
		end, err := timestamp(text)
		if err != nil {
			return err
		}
		if end.Before(start) {
			return errors.New("endTime precedes startTime; review the export")
		}
		if end.After(now) {
			return errors.New("export contains unfinished or future records; export completed requests")
		}
	}
	return nil
}
