// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package clauderecords

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/llm-measurement/fleetdiff/internal/attribution"
	"github.com/llm-measurement/fleetdiff/internal/compare"
)

type label struct {
	alias  string
	order  int
	digest [32]byte
}
type catalog struct {
	key     []byte
	scope   string
	entries map[uint64]label
}

func owner(r record) (kind, id string) {
	if _, present := r.resolve("identity.source"); present {
		return "unattributed", ""
	}
	for _, f := range []struct{ name, kind string }{{"user.account_uuid", "account"}, {"user.email", "email"}, {"user.id", "installation"}} {
		if _, present := r.resolve(f.name); present {
			v, _ := r.resolve(f.name)
			if !v.invalid && v.kind == "stringValue" && strings.TrimSpace(v.text) == "" {
				continue
			}
			if id, ok := r.text(f.name); ok {
				return f.kind, id
			}
			// Conflicting/malformed higher-priority attribution never falls back.
			return "unattributed", ""
		}
	}
	return "unattributed", ""
}
func (c *catalog) digest(kind string, parts ...string) ([32]byte, error) {
	return attribution.Identity(c.key, "claude-code-logs/v1", kind, append([]string{c.scope}, parts...)...)
}
func (c *catalog) group(r record, group string) (uint64, string, bool, error) {
	kind, id := owner(r)
	if id == "" {
		return 0, kind, false, nil
	}
	parts := []string{kind, id}
	if group == "session" {
		session, ok := r.text("session.id")
		if !ok {
			return 0, kind, false, nil
		}
		parts = append(parts, session)
	}
	d, err := c.digest(group, parts...)
	if err != nil {
		return 0, kind, false, err
	}
	h := binary.BigEndian.Uint64(d[:8])
	if old, exists := c.entries[h]; exists {
		if old.digest != d {
			return 0, kind, false, errors.New("identity hash collision; choose a different hashing secret")
		}
	} else {
		if len(c.entries) >= MaxIdentities {
			return 0, kind, false, errors.New("capture exceeds 250000 attributed identities; select a smaller capture")
		}
		prefix := kind
		if group == "session" {
			prefix = "session"
		}
		c.entries[h] = label{fmt.Sprintf("%s-%d", prefix, len(c.entries)+1), len(c.entries), d}
	}
	return h, kind, true, nil
}
func (c *catalog) prompt(r record) ([32]byte, bool, error) {
	kind, id := owner(r)
	session, hasSession := r.text("session.id")
	prompt, hasPrompt := r.text("prompt.id")
	if id == "" || !hasSession || !hasPrompt {
		return [32]byte{}, false, nil
	}
	d, err := c.digest("prompt", kind, id, session, prompt)
	return d, err == nil, err
}
func (c *catalog) rank(name string, a, b *aggregate, cache bool, opts Options) (compare.Concentration, error) {
	x, y := a.top, b.top
	if cache {
		x, y = a.cacheTop, b.cacheTop
	}
	r, err := compare.RankCandidates(name, x, y)
	if err != nil {
		return r, err
	}
	r.WeightUnit = "reported_tokens_finality_unknown"
	key := func(m compare.Mover) uint64 { n, _ := strconv.ParseUint(m.Hash, 16, 64); return n }
	abs := func(n int64) int64 {
		if n < 0 {
			return -n
		}
		return n
	}
	slices.SortFunc(r.Movers, func(a, b compare.Mover) int {
		x, y := max(abs(a.Delta.Lower), abs(a.Delta.Upper)), max(abs(b.Delta.Lower), abs(b.Delta.Upper))
		if x > y {
			return -1
		}
		if x < y {
			return 1
		}
		return c.entries[key(a)].order - c.entries[key(b)].order
	})
	r.Movers = r.Movers[:min(opts.Top, len(r.Movers))]
	for i := range r.Movers {
		m := &r.Movers[i]
		m.Item = c.entries[key(*m)].alias
		if !opts.ShowHashes {
			m.Hash = ""
		}
	}
	return r, nil
}
