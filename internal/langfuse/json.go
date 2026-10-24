// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package langfuse

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Only bounded approved spans are retained. Discarded values are syntax-scanned
// without constructing strings, RawMessages, token trees, or decoded values.
type lexer struct {
	r     *bufio.Reader
	bytes int
	row   bool
	line  bool
}

func (l *lexer) peek() byte {
	b, err := l.r.Peek(1)
	if err != nil {
		return 0
	}
	return b[0]
}

func (l *lexer) take(out *[]byte) (byte, error) {
	b, err := l.r.ReadByte()
	if err != nil {
		return 0, ErrInvalid
	}
	if l.row {
		l.bytes++
		if l.bytes > MaxRowBytes || (l.line && (b == '\n' || b == '\r')) {
			return 0, ErrInvalid
		}
	}
	if out != nil {
		if len(*out) >= MaxFieldBytes {
			return 0, ErrInvalid
		}
		*out = append(*out, b)
	}
	return b, nil
}

func (l *lexer) space() error {
	for l.peek() == ' ' || l.peek() == '\t' || l.peek() == '\n' || l.peek() == '\r' {
		if _, err := l.take(nil); err != nil {
			return err
		}
	}
	return nil
}

func (l *lexer) expect(want byte, out *[]byte) error {
	b, err := l.take(out)
	if err != nil || b != want {
		return ErrInvalid
	}
	return nil
}

func (l *lexer) hex(out *[]byte) (uint16, error) {
	var value uint16
	for range 4 {
		b, err := l.take(out)
		if err != nil {
			return 0, err
		}
		var n byte
		switch {
		case b >= '0' && b <= '9':
			n = b - '0'
		case b >= 'a' && b <= 'f':
			n = b - 'a' + 10
		case b >= 'A' && b <= 'F':
			n = b - 'A' + 10
		default:
			return 0, ErrInvalid
		}
		value = value*16 + uint16(n)
	}
	return value, nil
}

func (l *lexer) quoted(out *[]byte) error {
	if err := l.expect('"', out); err != nil {
		return err
	}
	for {
		b, err := l.take(out)
		if err != nil || b < 0x20 {
			return ErrInvalid
		}
		if b == '"' {
			return nil
		}
		if b == '\\' {
			e, err := l.take(out)
			if err != nil {
				return err
			}
			if e == 'u' {
				u, err := l.hex(out)
				if err != nil {
					return err
				}
				if u >= 0xdc00 && u <= 0xdfff {
					return ErrInvalid
				}
				if u >= 0xd800 && u <= 0xdbff {
					if l.expect('\\', out) != nil || l.expect('u', out) != nil {
						return ErrInvalid
					}
					low, err := l.hex(out)
					if err != nil || low < 0xdc00 || low > 0xdfff {
						return ErrInvalid
					}
				}
			} else if e != '"' && e != '\\' && e != '/' && e != 'b' && e != 'f' && e != 'n' && e != 'r' && e != 't' {
				return ErrInvalid
			}
		} else if b >= 0x80 {
			// Validate an encoded scalar using at most four transport bytes; do
			// not decode or retain the discarded field's text.
			var encoded [4]byte
			encoded[0] = b
			n := 0
			switch {
			case b >= 0xc2 && b <= 0xdf:
				n = 2
			case b >= 0xe0 && b <= 0xef:
				n = 3
			case b >= 0xf0 && b <= 0xf4:
				n = 4
			default:
				return ErrInvalid
			}
			for i := 1; i < n; i++ {
				encoded[i], err = l.take(out)
				if err != nil {
					return err
				}
			}
			if !utf8.Valid(encoded[:n]) {
				return ErrInvalid
			}
		}
	}
}

func (l *lexer) key() (string, error) {
	var raw []byte
	if err := l.quoted(&raw); err != nil {
		return "", err
	}
	var key string
	if json.Unmarshal(raw, &key) != nil {
		return "", ErrInvalid
	}
	return key, nil
}

func digit(b byte) bool { return b >= '0' && b <= '9' }

func (l *lexer) number(out *[]byte) error {
	if l.peek() == '-' {
		if _, err := l.take(out); err != nil {
			return err
		}
	}
	if l.peek() == '0' {
		if _, err := l.take(out); err != nil {
			return err
		}
		if digit(l.peek()) {
			return ErrInvalid
		}
	} else {
		if l.peek() < '1' || l.peek() > '9' {
			return ErrInvalid
		}
		for digit(l.peek()) {
			if _, err := l.take(out); err != nil {
				return err
			}
		}
	}
	if l.peek() == '.' {
		if _, err := l.take(out); err != nil {
			return err
		}
		if !digit(l.peek()) {
			return ErrInvalid
		}
		for digit(l.peek()) {
			if _, err := l.take(out); err != nil {
				return err
			}
		}
	}
	if l.peek() == 'e' || l.peek() == 'E' {
		if _, err := l.take(out); err != nil {
			return err
		}
		if l.peek() == '+' || l.peek() == '-' {
			if _, err := l.take(out); err != nil {
				return err
			}
		}
		if !digit(l.peek()) {
			return ErrInvalid
		}
		for digit(l.peek()) {
			if _, err := l.take(out); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *lexer) value(out *[]byte, depth int) error {
	if depth > MaxDepth || l.space() != nil {
		return ErrInvalid
	}
	switch l.peek() {
	case '"':
		return l.quoted(out)
	case '{', '[':
		open, err := l.take(out)
		if err != nil {
			return err
		}
		close := byte(']')
		if open == '{' {
			close = '}'
		}
		if l.space() != nil {
			return ErrInvalid
		}
		if l.peek() == close {
			return l.expect(close, out)
		}
		for n := 0; n < MaxFields; n++ {
			if open == '{' {
				if l.space() != nil || l.quoted(out) != nil || l.space() != nil || l.expect(':', out) != nil {
					return ErrInvalid
				}
			}
			if l.value(out, depth+1) != nil || l.space() != nil {
				return ErrInvalid
			}
			if l.peek() == close {
				return l.expect(close, out)
			}
			if l.expect(',', out) != nil {
				return ErrInvalid
			}
		}
		return ErrInvalid
	case 't', 'f', 'n':
		word := "null"
		if l.peek() == 't' {
			word = "true"
		}
		if l.peek() == 'f' {
			word = "false"
		}
		for i := range len(word) {
			if l.expect(word[i], out) != nil {
				return ErrInvalid
			}
		}
		return nil
	default:
		return l.number(out)
	}
}

var usageKeys = map[string]string{"input": "input", "output": "output", "input_cached_tokens": "cache_read", "input_cache_creation": "cache_write", "output_reasoning_tokens": "reasoning", "total": "total"}
var rowKeys = map[string]bool{
	"id": true, "project_id": true, "type": true, "start_time": true, "end_time": true,
	"total_cost": true, "usage_details": true, "provided_model_name": true,
	"user_id": true, "session_id": true, "environment": true, "release": true,
	"trace_name": true, "prompt_name": true, "tags": true,
}

type projection struct {
	fields   map[string][]byte
	level    string
	usage    map[string][]byte
	unknown  uint64
	badUsage bool
}

func observationLevel(s string) string {
	switch s {
	case "DEFAULT", "DEBUG", "WARNING", "ERROR":
		return s
	default:
		return "unknown"
	}
}

// Seven ASCII characters, even when all are JSON-escaped, fit in 44 bytes.
// Decode only that bounded lookahead; scan the full value without retaining it.
func (l *lexer) level() (string, error) {
	if l.space() != nil {
		return "unknown", ErrInvalid
	}
	level := "unknown"
	if l.peek() == '"' {
		raw, _ := l.r.Peek(44)
		var s string
		if json.NewDecoder(bytes.NewReader(raw)).Decode(&s) == nil {
			level = observationLevel(s)
		}
	}
	return level, l.value(nil, 1)
}

func (l *lexer) usage(p *projection) error {
	p.usage = map[string][]byte{}
	if l.space() != nil {
		return ErrInvalid
	}
	if l.peek() != '{' {
		p.badUsage = l.peek() != 'n'
		return l.value(nil, 1)
	}
	if l.expect('{', nil) != nil || l.space() != nil {
		return ErrInvalid
	}
	if l.peek() == '}' {
		return l.expect('}', nil)
	}
	seen := map[string]bool{}
	for n := 0; n < MaxFields; n++ {
		if l.space() != nil {
			return ErrInvalid
		}
		key, err := l.key()
		if err != nil || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
		if l.space() != nil || l.expect(':', nil) != nil || l.space() != nil {
			return ErrInvalid
		}
		name, keep := usageKeys[key]
		if !keep {
			p.unknown++
			if l.value(nil, 2) != nil {
				return ErrInvalid
			}
		} else {
			var raw []byte
			if digit(l.peek()) || l.peek() == '-' || l.peek() == 'n' {
				if l.value(&raw, 2) != nil {
					return ErrInvalid
				}
			} else {
				if l.value(nil, 2) != nil {
					return ErrInvalid
				}
				raw = []byte("false")
			}
			p.usage[name] = raw
		}
		if l.space() != nil {
			return ErrInvalid
		}
		if l.peek() == '}' {
			return l.expect('}', nil)
		}
		if l.expect(',', nil) != nil {
			return ErrInvalid
		}
	}
	return ErrInvalid
}

func (l *lexer) object() (projection, error) {
	p := projection{fields: map[string][]byte{}}
	if l.expect('{', nil) != nil || l.space() != nil {
		return p, ErrInvalid
	}
	if l.peek() == '}' {
		return p, l.expect('}', nil)
	}
	seen := map[string]bool{}
	for n := 0; n < MaxFields; n++ {
		if l.space() != nil {
			return p, ErrInvalid
		}
		key, err := l.key()
		if err != nil || seen[key] {
			return p, ErrInvalid
		}
		seen[key] = true
		if l.space() != nil || l.expect(':', nil) != nil {
			return p, ErrInvalid
		}
		if key == "level" {
			var err error
			p.level, err = l.level()
			if err != nil {
				return p, err
			}
		} else if key == "usage_details" {
			err = l.usage(&p)
		} else if rowKeys[key] {
			var raw []byte
			err = l.value(&raw, 1)
			p.fields[key] = raw
		} else {
			err = l.value(nil, 1)
		}
		if err != nil || l.space() != nil {
			return p, ErrInvalid
		}
		if l.peek() == '}' {
			return p, l.expect('}', nil)
		}
		if l.expect(',', nil) != nil {
			return p, ErrInvalid
		}
	}
	return p, ErrInvalid
}

func readJSON(r io.Reader, array bool, visit func(projection) error) error {
	l := lexer{r: bufio.NewReader(r)}
	if l.space() != nil {
		return ErrInvalid
	}
	if array && l.expect('[', nil) != nil {
		return ErrInvalid
	}
	if l.space() != nil {
		return ErrInvalid
	}
	if array && l.peek() == ']' {
		if l.expect(']', nil) != nil {
			return ErrInvalid
		}
		return l.finish()
	}
	for {
		if !array && l.peek() == 0 {
			return l.finish()
		}
		l.bytes, l.row, l.line = 0, true, !array
		p, err := l.object()
		l.row = false
		if err != nil {
			return err
		}
		if visit(p) != nil {
			return ErrInvalid
		}
		if array {
			if l.space() != nil {
				return ErrInvalid
			}
			if l.peek() == ']' {
				if l.expect(']', nil) != nil {
					return ErrInvalid
				}
				return l.finish()
			}
			if l.expect(',', nil) != nil || l.space() != nil {
				return ErrInvalid
			}
		} else {
			for l.peek() == ' ' || l.peek() == '\t' {
				if _, err = l.take(nil); err != nil {
					return err
				}
			}
			if l.peek() == 0 {
				return l.finish()
			}
			if l.peek() == '\r' {
				if l.expect('\r', nil) != nil || l.expect('\n', nil) != nil {
					return ErrInvalid
				}
			} else if l.expect('\n', nil) != nil {
				return ErrInvalid
			}
			if l.space() != nil {
				return ErrInvalid
			}
		}
	}
}

func (l *lexer) finish() error {
	if l.space() != nil {
		return ErrInvalid
	}
	_, err := l.r.Peek(1)
	if err != io.EOF {
		return ErrInvalid
	}
	return nil
}
