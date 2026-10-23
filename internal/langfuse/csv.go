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

// csvField scans RFC4180 quoting even for discarded columns. Only approved
// columns have a value buffer; encoding/csv cannot provide that boundary.
func (l *lexer) csvField(keep bool) ([]byte, byte, error) {
	var out []byte
	add := func(b byte) error {
		if keep {
			if len(out) >= MaxFieldBytes {
				return ErrInvalid
			}
			out = append(out, b)
		}
		return nil
	}
	quoted := l.peek() == '"'
	if quoted && l.expect('"', nil) != nil {
		return nil, 0, ErrInvalid
	}
	for {
		if l.peek() == 0 {
			if quoted {
				return nil, 0, ErrInvalid
			}
			return out, 0, nil
		}
		b, err := l.take(nil)
		if err != nil {
			return nil, 0, err
		}
		if quoted && b == '"' {
			if l.peek() == '"' {
				if l.expect('"', nil) != nil || add('"') != nil {
					return nil, 0, ErrInvalid
				}
				continue
			}
			next := l.peek()
			if next != ',' && next != '\r' && next != '\n' && next != 0 {
				return nil, 0, ErrInvalid
			}
			if next == 0 {
				return out, 0, nil
			}
			b, err = l.take(nil)
			if err != nil {
				return nil, 0, err
			}
			return out, b, nil
		}
		if !quoted && (b == ',' || b == '\r' || b == '\n') {
			return out, b, nil
		}
		if b == 0 || (!quoted && b == '"') {
			return nil, 0, ErrInvalid
		}
		if b >= 0x80 {
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
				return nil, 0, ErrInvalid
			}
			for i := 1; i < n; i++ {
				encoded[i], err = l.take(nil)
				if err != nil {
					return nil, 0, err
				}
			}
			if !utf8.Valid(encoded[:n]) {
				return nil, 0, ErrInvalid
			}
			for _, c := range encoded[:n] {
				if add(c) != nil {
					return nil, 0, ErrInvalid
				}
			}
		} else if add(b) != nil {
			return nil, 0, ErrInvalid
		}
	}
}

func (l *lexer) csvRecord(names []string) ([][]byte, error) {
	l.bytes, l.row = 0, true
	defer func() { l.row = false }()
	var fields [][]byte
	for i := 0; i < MaxFields; i++ {
		if names != nil && i >= len(names) {
			return nil, ErrInvalid
		}
		keep := names == nil || rowKeys[names[i]]
		value, end, err := l.csvField(keep)
		if err != nil {
			return nil, err
		}
		fields = append(fields, value)
		if end == ',' {
			continue
		}
		if end == '\r' {
			if l.expect('\n', nil) != nil {
				return nil, ErrInvalid
			}
		}
		if names != nil && len(fields) != len(names) {
			return nil, ErrInvalid
		}
		return fields, nil
	}
	return nil, ErrInvalid
}

func readCSV(r io.Reader, visit func(projection) error) error {
	l := lexer{r: bufio.NewReader(r)}
	if l.peek() == 0 {
		return ErrInvalid
	}
	header, err := l.csvRecord(nil)
	if err != nil {
		return err
	}
	names := make([]string, len(header))
	seen := map[string]bool{}
	for i, value := range header {
		names[i] = string(value)
		if len(value) == 0 || seen[names[i]] {
			return ErrInvalid
		}
		seen[names[i]] = true
	}
	for l.peek() != 0 {
		values, err := l.csvRecord(names)
		if err != nil {
			return err
		}
		p := projection{fields: map[string][]byte{}}
		for i, name := range names {
			if !rowKeys[name] {
				continue
			}
			value := values[i]
			if name == "usage_details" {
				if len(value) == 0 {
					continue
				}
				field := lexer{r: bufio.NewReader(bytes.NewReader(value))}
				if field.usage(&p) != nil || field.finish() != nil {
					return ErrInvalid
				}
			} else if name == "total_cost" || name == "tags" {
				if len(value) > 0 {
					p.fields[name] = value
				}
			} else {
				p.fields[name], _ = json.Marshal(string(value))
			}
		}
		if visit(p) != nil {
			return ErrInvalid
		}
	}
	return l.finish()
}
