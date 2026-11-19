// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/boundedjson"
	"github.com/llm-measurement/fleetdiff/internal/localfile"
)

var Workloads = []string{"qa", "extraction", "coding"}

type Price struct {
	ID            string      `json:"id"`
	Provider      string      `json:"provider"`
	Input         string      `json:"input"`
	Output        string      `json:"output"`
	CacheRead     string      `json:"cache_read"`
	CacheWrite    string      `json:"cache_write"`
	SourceURL     string      `json:"source_url,omitempty"`
	CacheWriteTTL string      `json:"cache_write_ttl,omitempty"`
	InputTierMax  uint64      `json:"input_tier_max,omitempty"`
	Tiers         []PriceTier `json:"tiers,omitempty"`
}

// A zero maximum is unbounded and is allowed only on the final tier.
type PriceTier struct {
	MaxPromptTokens uint64            `json:"max_prompt_tokens"`
	Input           string            `json:"input"`
	Output          string            `json:"output"`
	CacheRead       string            `json:"cache_read"`
	CacheWrite      map[string]string `json:"cache_write"`
}

type Prices struct {
	Version  int     `json:"version"`
	Date     string  `json:"date"`
	Models   []Price `json:"models"`
	Source   string  `json:"source_record,omitempty"`
	Currency string  `json:"currency,omitempty"`
	Unit     string  `json:"unit,omitempty"`
	Tier     string  `json:"tier,omitempty"`
}

type InputFactor struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Factor string `json:"factor"`
	N      int    `json:"n"`
	Source string `json:"source_record"`
}

type Factor struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Workload string `json:"workload"`
	Output   string `json:"output"`
	Low      string `json:"output_low"`
	High     string `json:"output_high"`
	N        int    `json:"n"`
	Source   string `json:"source_record"`
}

type Factors struct {
	Version    int           `json:"version"`
	Date       string        `json:"date"`
	Source     string        `json:"source_record"`
	InputPairs []InputFactor `json:"input_pairs"`
	Pairs      []Factor      `json:"pairs"`
}

// ReadConfig bounds bytes and structure, rejects duplicate/unknown members, and
// deliberately suppresses parser errors that could reproduce file contents.
func ReadConfig(path string, target any) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errors.New("cannot open what-if configuration")
	}
	defer root.Close()
	data, err := localfile.Read(root, filepath.Base(path), 1<<20)
	if err != nil {
		return errors.New("what-if configuration must be a regular JSON file of at most 1 MiB")
	}
	if _, err = boundedjson.Decode(data, 8, 20000); err != nil {
		return errors.New("invalid or over-limit what-if JSON configuration")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(target); err != nil {
		return errors.New("invalid what-if configuration fields; see the versioned example")
	}
	return nil
}

func decimal(s string) (*big.Rat, bool) {
	r, ok := accounting.Cost(s)
	return r, ok
}

func fraction(s string) (*big.Rat, bool) {
	r, ok := decimal(s)
	return r, ok && r.Sign() > 0 && r.Cmp(big.NewRat(1, 1)) <= 0
}

func validDate(s string) bool {
	t, err := time.Parse("2006-01-02", s)
	return err == nil && t.Year() >= 2020 && t.Year() <= 2200
}

func validatePrices(p Prices) error {
	if (p.Version != 1 && p.Version != 2) || !validDate(p.Date) || len(p.Models) == 0 || len(p.Models) > 128 {
		return errors.New("prices need version 1 or 2, a date, and 1-128 model prices")
	}
	if p.Currency != "" && p.Currency != "USD" || p.Unit != "" && p.Unit != "per_million_tokens" {
		return errors.New("prices must use USD per million tokens")
	}
	seen := map[string]bool{}
	for _, m := range p.Models {
		if len(m.ID) == 0 || len(m.ID) > 128 || len(m.Provider) == 0 || len(m.Provider) > 64 || seen[m.ID] {
			return errors.New("invalid or duplicate model in prices")
		}
		seen[m.ID] = true
		if err := validatePriceModel(p.Version, m); err != nil {
			return err
		}
	}
	return nil
}

func validatePriceModel(version int, m Price) error {
	if version == 1 || len(m.Tiers) == 0 {
		if len(m.Tiers) != 0 || !validRates(m.Input, m.Output, m.CacheRead, m.CacheWrite) {
			return errors.New("each model needs nonnegative decimal input, output, cache-read and cache-write USD prices per million tokens")
		}
		return nil
	}
	return validateTiers(m)
}

func validateTiers(m Price) error {
	if len(m.Tiers) > 8 || m.Input != "" || m.Output != "" || m.CacheRead != "" || m.CacheWrite != "" || m.InputTierMax != 0 || m.CacheWriteTTL == "" {
		return errors.New("tiered model needs 1-8 tiers and one selected cache-write TTL class")
	}
	previous := uint64(0)
	for i, tier := range m.Tiers {
		if !validTier(m, tier, i, previous) {
			return errors.New("tier bounds and selected rates must be ordered and complete")
		}
		for ttl, rate := range tier.CacheWrite {
			if !validTTL(ttl) || !validRate(rate) {
				return errors.New("tier cache-write classes need known TTLs and nonnegative rates")
			}
		}
		previous = tier.MaxPromptTokens
	}
	return nil
}

func validTier(m Price, tier PriceTier, i int, previous uint64) bool {
	return !((tier.MaxPromptTokens == 0 && i != len(m.Tiers)-1) ||
		(tier.MaxPromptTokens != 0 && tier.MaxPromptTokens <= previous) ||
		len(tier.CacheWrite) == 0 || len(tier.CacheWrite) > 4 ||
		!validRates(tier.Input, tier.Output, tier.CacheRead, tier.CacheWrite[m.CacheWriteTTL]))
}

func validRate(s string) bool { _, ok := decimal(s); return ok }

func validRates(input, output, read, write string) bool {
	return validRate(input) && validRate(output) && validRate(read) && validRate(write)
}

func validTTL(s string) bool {
	return slices.Contains([]string{"5m", "1h", "30m", "12h"}, s)
}

// OverridePrices changes rates, not the identity or provider of a known model.
// Unlisted models retain the bundled rates only when no override is supplied.
func OverridePrices(bundled, override Prices) (Prices, error) {
	if err := validatePrices(override); err != nil {
		return Prices{}, err
	}
	override.Models = slices.Clone(override.Models)
	for j, p := range override.Models {
		i := slices.IndexFunc(bundled.Models, func(b Price) bool { return b.ID == p.ID && b.Provider == p.Provider })
		if i < 0 {
			return Prices{}, errors.New("price override names an unknown model or provider; no rates were guessed")
		}
		if len(bundled.Models[i].Tiers) > 1 && len(p.Tiers) == 0 && p.InputTierMax == 0 {
			return Prices{}, errors.New("tiered model override needs explicit tiers or a priced input maximum")
		}
		if p.InputTierMax == 0 {
			override.Models[j].InputTierMax = bundled.Models[i].InputTierMax
		}
		if p.CacheWriteTTL == "" {
			override.Models[j].CacheWriteTTL = bundled.Models[i].CacheWriteTTL
		}
	}
	return override, nil
}

func validateScenario(s Scenario, m WorkloadMap) error {
	if s.Version != 1 || len(s.Moves) == 0 || len(s.Moves) > 32 {
		return errors.New("scenario needs version 1 and 1-32 moves")
	}
	for _, move := range s.Moves {
		if move.From == "" || move.To == "" || move.From == move.To || len(move.From) > 128 || len(move.To) > 128 {
			return errors.New("each move needs distinct known source and target models")
		}
		if _, ok := fraction(move.Fraction); !ok {
			return errors.New("move fractions must be decimals greater than zero and at most one")
		}
		if move.Scope != nil && !validSelector(*move.Scope) {
			return errors.New("move scope needs kind key, team, route or agent and a nonempty value of at most 8192 bytes")
		}
	}
	if m.Version != 0 && m.Version != 1 || len(m.Mappings) > 1024 || len(m.Mappings) > 0 && m.Version != 1 {
		return errors.New("workload map needs version 1 and at most 1024 mappings")
	}
	seen := map[Selector]bool{}
	for _, item := range m.Mappings {
		if !validSelector(item.Selector) || !slices.Contains(Workloads, item.Workload) || seen[item.Selector] {
			return errors.New("invalid or duplicate workload mapping; use key/team/route/agent selectors and qa, extraction or coding")
		}
		seen[item.Selector] = true
	}
	return nil
}

func validSelector(s Selector) bool {
	return (s.Kind == "key" || s.Kind == "team" || s.Kind == "route" || s.Kind == "agent") && len(s.Value) <= 8192 && strings.TrimSpace(s.Value) != ""
}
