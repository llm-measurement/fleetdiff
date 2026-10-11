// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/llm-measurement/fleetdiff/internal/accounting"
	"github.com/llm-measurement/fleetdiff/internal/compare"
	commonpb "go.opentelemetry.io/proto/slim/otlp/common/v1"
)

func recordCallType(r *Report, index map[string]int, row Row, side int) error {
	v := row.Values["call_type"]
	// Call types are schema values, matched exactly like operation(). Retain at
	// most 64 locally; unfamiliar values are never copied into the report.
	i, ok := index[v]
	if !ok {
		if len(index) == 64 {
			return errors.New("export exceeds 64 call types; select a narrower export")
		}
		name := v
		if operation(v) == "" {
			switch v {
			case "embedding", "aembedding", "image_generation", "aimage_generation", "transcription", "atranscription", "speech", "aspeech", "moderation", "amoderation", "rerank", "arerank", "agent", "tool", "generate_content", "agenerate_content", "send_message", "asend_message":
			default:
				name = fmt.Sprintf("unknown-%d", len(index)+1)
			}
		}
		i = len(r.CallTypes)
		index[v] = i
		r.CallTypes = append(r.CallTypes, CallType{Name: name, Analyzed: operation(v) != ""})
	}
	c := &r.CallTypes[i].Before
	if side == 1 {
		c = &r.CallTypes[i].After
	}
	c.Requests++
	invalid, missing := false, false
	for _, f := range []struct {
		name     string
		sum      *uint64
		overflow *bool
	}{
		{"prompt_tokens", &c.PromptTokens, &c.PromptTokensOverflow},
		{"completion_tokens", &c.CompletionTokens, &c.CompletionTokensOverflow},
		{"total_tokens", &c.TotalTokens, &c.TotalTokensOverflow},
	} {
		v := row.Values[f.name]
		if v == "" {
			missing = true
			continue
		}
		n, valid, err := accounting.Number(&commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}})
		if !valid || err != nil {
			invalid = true
			continue
		}
		if *f.overflow {
			continue
		}
		sum, err := accounting.AddBounded(*f.sum, n)
		if err != nil {
			// Raw coverage is optional evidence, not the analyzed token total.
			*f.sum, *f.overflow = 0, true
		} else {
			*f.sum = sum
		}
	}
	if invalid {
		c.InvalidUsage++
	}
	if missing {
		c.MissingUsage++
	}
	return nil
}

// Retain ordinals only for the bounded candidate set. A second streaming pass
// avoids an exact identity map growing with every user; its digest checks that
// aliases and totals describe the same file contents.
func orderCandidates(path string, r *Report, key, expected []byte) error {
	if len(r.Rankings.Movers) == 0 {
		return nil
	}
	order := map[uint64]int{}
	for _, m := range r.Rankings.Movers {
		h, err := strconv.ParseUint(m.Hash, 16, 64)
		if err != nil {
			return errors.New("invalid internal candidate hash")
		}
		order[h] = 0
	}
	digest := sha256.New()
	enc := json.NewEncoder(digest)
	err := Read(path, func(row Row) error {
		if err := enc.Encode(row.Values); err != nil {
			return err
		}
		start, err := timestamp(row.Values["startTime"])
		if err != nil {
			return err
		}
		if operation(row.Values["call_type"]) == "" || (!r.Before.Period.contains(start) && !r.After.Period.contains(start)) {
			return nil
		}
		h, present, err := groupIdentity(row, r.GroupBy, key)
		if err != nil {
			return err
		}
		id := binary.BigEndian.Uint64(h[:8])
		if n, tracked := order[id]; present && tracked && n == 0 {
			order[id] = row.Number
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(expected, digest.Sum(nil)) {
		return errors.New("export changed while reading; use a completed immutable export")
	}
	strength := func(v compare.Interval) int64 { return max(v.Upper, -v.Lower) }
	slices.SortFunc(r.Rankings.Movers, func(a, b compare.Mover) int {
		x, y := strength(a.Delta), strength(b.Delta)
		if x > y {
			return -1
		}
		if x < y {
			return 1
		}
		ha, _ := strconv.ParseUint(a.Hash, 16, 64)
		hb, _ := strconv.ParseUint(b.Hash, 16, 64)
		return order[ha] - order[hb]
	})
	return nil
}
