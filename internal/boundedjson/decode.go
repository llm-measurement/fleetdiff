// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

// Package boundedjson decodes bounded JSON without accepting duplicate members.
package boundedjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var errEncoding = errors.New("invalid or over-limit JSON structure")

// Decode composes encoding/json with the inspect decoder's structural limits.
// Callers must bound bytes before calling; Number preserves decimal lexemes.
func Decode(data []byte, maxDepth, maxNodes int) (any, error) {
	if !utf8.Valid(data) {
		return nil, errEncoding
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	value, err := read(d, 0, maxDepth, &maxNodes)
	if err != nil {
		return nil, errEncoding
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errEncoding
	}
	return value, nil
}

func read(d *json.Decoder, depth, maxDepth int, left *int) (any, error) {
	*left--
	if depth > maxDepth || *left < 0 {
		return nil, errEncoding
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			t, err := d.Token()
			if err != nil {
				return nil, err
			}
			key, ok := t.(string)
			if !ok {
				return nil, errEncoding
			}
			if _, duplicate := m[key]; duplicate {
				return nil, errEncoding
			}
			v, err := read(d, depth+1, maxDepth, left)
			if err != nil {
				return nil, err
			}
			m[key] = v
		}
		if t, err := d.Token(); err != nil || t != json.Delim('}') {
			return nil, errEncoding
		}
		return m, nil
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := read(d, depth+1, maxDepth, left)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		if t, err := d.Token(); err != nil || t != json.Delim(']') {
			return nil, errEncoding
		}
		return a, nil
	default:
		if _, bad := t.(json.Delim); bad {
			return nil, errEncoding
		}
		return t, nil
	}
}
