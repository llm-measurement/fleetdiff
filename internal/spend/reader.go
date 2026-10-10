// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	MaxRows       = 10_000_000
	MaxFileBytes  = 16 << 30
	MaxRowBytes   = 64 << 10
	MaxFieldBytes = 8 << 10
	MaxFields     = 64
	MaxDepth      = 8
)

// Row is a request-level SQL projection. Number is the one-based record number,
// excluding CSV headers and blank lines. Values contains only nonempty, non-null
// allowlisted fields; numeric JSON values retain their original spelling.
type Row struct {
	Values map[string]string
	Number int
}

var requiredColumns = [...]string{
	"request_id", "call_type", "startTime", "prompt_tokens", "completion_tokens", "total_tokens",
}

var (
	errSpendFile      = errors.New("spend input must be a readable regular file, not a directory, symlink, or special file; export one SQL-projection file")
	errSpendRead      = errors.New("cannot read spend input; check access and export a new SQL-projection file")
	errSpendType      = errors.New("unsupported spend file extension; use .csv, .json, or .jsonl from the SQL projection")
	errSpendSize      = errors.New("spend input exceeds 16 GiB; export a smaller SQL-projection file")
	errSpendRows      = errors.New("spend input exceeds 10,000,000 rows; export a smaller SQL-projection file")
	errSpendRow       = errors.New("spend record exceeds 64 KiB; exclude content columns using the SQL projection")
	errSpendField     = errors.New("spend field exceeds 8 KiB; shorten the field or exclude content columns using the SQL projection")
	errSpendFields    = errors.New("spend record exceeds 64 fields; select only SQL-projection columns")
	errSpendDepth     = errors.New("spend JSON exceeds nesting depth 8; exclude nested content using the SQL projection")
	errSpendCSV       = errors.New("invalid spend CSV; export headered CSV using the SQL projection")
	errSpendJSON      = errors.New("invalid spend JSON; export a JSON array or one JSON object per line using the SQL projection")
	errSpendUTF8      = errors.New("spend input contains invalid UTF-8 or Unicode escapes; export UTF-8 SQL-projection rows")
	errSpendDuplicate = errors.New("duplicate spend column or JSON object key; export unique SQL-projection column names")
	errSpendColumns   = errors.New("spend export is missing required columns; include request_id, call_type, startTime, prompt_tokens, completion_tokens, and total_tokens in the SQL projection")
	errSpendScalar    = errors.New("spend SQL-projection fields must be strings, numbers, or null; export scalar columns")
	errSpendAggregate = errors.New("daily UI aggregates and API pages are unsupported; export request-level SQL-projection rows as headered CSV, a JSON array, or JSONL")
)

func allowedColumn(name string) bool {
	switch name {
	case "request_id", "call_type", "api_key", "user", "team_id", "organization_id", "end_user",
		"model", "model_id", "model_group", "custom_llm_provider", "prompt_tokens", "completion_tokens",
		"total_tokens", "spend", "status", "session_id", "startTime", "endTime", "cache_read_input_tokens",
		"cache_write_input_tokens", "reasoning_output_tokens":
		return true
	}
	return false
}

// Read streams a regular file without modifying it. The caller must discard
// results if Read fails: validation can fail after earlier visits. Visitor errors
// are returned unchanged; the visitor is responsible for keeping its errors safe.
// CSV record bytes include line endings; JSON array record bytes exclude the
// array delimiters. Field limits apply to decoded text, including ignored fields.
func Read(path string, visit func(Row) error) error {
	if visit == nil {
		return errors.New("spend row visitor is required; configure an importer before reading")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errSpendFile
	}
	if info.Size() > MaxFileBytes {
		return errSpendSize
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".csv" && ext != ".json" && ext != ".jsonl" {
		return errSpendType
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errSpendFile
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return errSpendFile
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return errSpendFile
	}
	if current.Size() > MaxFileBytes {
		return errSpendSize
	}
	return readSpendStream(f, ext, MaxFileBytes, visit)
}

// Keep a byte budget on the open descriptor as well as checking its initial
// size. A single-byte probe detects growth at the limit without returning it.
type spendByteBudget struct {
	r    io.Reader
	left int64
}

func (r *spendByteBudget) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.left == 0 {
		var probe [1]byte
		n, err := r.r.Read(probe[:])
		if n != 0 {
			return 0, errSpendSize
		}
		if err != nil && err != io.EOF {
			return 0, errSpendRead
		}
		return 0, err
	}
	if int64(len(p)) > r.left {
		p = p[:int(r.left)]
	}
	n, err := r.r.Read(p)
	r.left -= int64(n)
	if err != nil && err != io.EOF {
		err = errSpendRead
	}
	return n, err
}

func readSpendStream(r io.Reader, ext string, limit int64, visit func(Row) error) error {
	b := bufio.NewReaderSize(&spendByteBudget{r: r, left: limit}, 4096)
	// A fixed record buffer bounds framing before csv/json can allocate fields.
	frame := make([]byte, 0, MaxRowBytes)
	switch ext {
	case ".csv":
		return readSpendCSV(b, frame, visit)
	case ".json":
		return readSpendArray(b, frame, visit)
	case ".jsonl":
		return readSpendLines(b, frame, visit)
	}
	return errSpendType
}

func readSpendCSV(b *bufio.Reader, frame []byte, visit func(Row) error) error {
	var header []string
	number := 0
	var source bytes.Reader
	buffered := bufio.NewReader(&source)
	reader := csv.NewReader(buffered)
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	for {
		frame = frame[:0]
		quoted := false
		for {
			c, err := b.ReadByte()
			if err != nil {
				if err != io.EOF {
					return err
				}
				break
			}
			if len(frame) == MaxRowBytes {
				return errSpendRow
			}
			frame = append(frame, c)
			if c == '"' {
				quoted = !quoted
			}
			if c == '\n' && !quoted {
				break
			}
		}
		if len(frame) == 0 {
			if header == nil {
				return errSpendColumns
			}
			return nil
		}
		body := bytes.TrimSuffix(bytes.TrimSuffix(frame, []byte{'\n'}), []byte{'\r'})
		if len(body) == 0 {
			continue
		}
		if header != nil && number == MaxRows {
			return errSpendRows
		}
		if !utf8.Valid(frame) {
			return errSpendUTF8
		}
		if err := preflightSpendCSV(body); err != nil {
			return err
		}
		source.Reset(frame)
		buffered.Reset(&source)
		record, err := reader.Read()
		if err != nil {
			return errSpendCSV
		}
		if header == nil {
			seen := make(map[string]bool, len(record))
			for _, key := range record {
				if seen[key] {
					return errSpendDuplicate
				}
				seen[key] = true
			}
			if err := checkSpendColumns(seen); err != nil {
				return err
			}
			header = append([]string(nil), record...)
			continue
		}
		if len(record) != len(header) {
			return errSpendCSV
		}
		values := make(map[string]string)
		for i, value := range record {
			if value != "" && allowedColumn(header[i]) {
				// csv fields share a backing string; clone retained values so ignored
				// content does not survive in rows held by the visitor.
				values[header[i]] = strings.Clone(value)
			}
		}
		number++
		if err := visit(Row{Values: values, Number: number}); err != nil {
			return err
		}
	}
}

func preflightSpendCSV(data []byte) error {
	start, fields := 0, 0
	quoted := false
	for i := 0; i <= len(data); i++ {
		if i < len(data) && data[i] == '"' {
			quoted = !quoted
		}
		if i < len(data) && (data[i] != ',' || quoted) {
			continue
		}
		fields++
		if fields > MaxFields {
			return errSpendFields
		}
		field := data[start:i]
		size := len(field)
		if len(field) >= 2 && field[0] == '"' && field[len(field)-1] == '"' {
			size -= 2 + bytes.Count(field[1:len(field)-1], []byte{'"', '"'}) + bytes.Count(field, []byte{'\r', '\n'})
		}
		if size > MaxFieldBytes {
			return errSpendField
		}
		start = i + 1
	}
	return nil
}

func checkSpendColumns(seen map[string]bool) error {
	for _, key := range requiredColumns {
		if !seen[key] {
			if seen["data"] || seen["Date"] || seen["Spend ($)"] {
				return errSpendAggregate
			}
			return errSpendColumns
		}
	}
	return nil
}

func jsonSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func nextSpendJSONByte(b *bufio.Reader) (byte, error) {
	for {
		c, err := b.ReadByte()
		if err != nil || !jsonSpace(c) {
			return c, err
		}
	}
}

func spendJSONReadError(err error) error {
	if err == nil || err == io.EOF {
		return errSpendJSON
	}
	return err
}

func readSpendArray(b *bufio.Reader, frame []byte, visit func(Row) error) error {
	c, err := nextSpendJSONByte(b)
	if err == nil && c == '{' {
		return errSpendAggregate
	}
	if err != nil || c != '[' {
		return spendJSONReadError(err)
	}
	c, err = nextSpendJSONByte(b)
	for number := 1; err == nil && c != ']'; number++ {
		if number > MaxRows {
			return errSpendRows
		}
		if c != '{' {
			return errSpendJSON
		}
		frame = append(frame[:0], c)
		depth, quoted, escaped := 1, false, false
		for depth != 0 {
			c, err = b.ReadByte()
			if err != nil {
				return spendJSONReadError(err)
			}
			if len(frame) == MaxRowBytes {
				return errSpendRow
			}
			if quoted {
				if escaped {
					escaped = false
				} else if c == '\\' {
					escaped = true
				} else if c == '"' {
					quoted = false
				}
			} else {
				switch c {
				case '"':
					quoted = true
				case '{', '[':
					depth++
					if depth+1 > MaxDepth { // Include the outer export array.
						return errSpendDepth
					}
				case '}', ']':
					depth--
				}
			}
			frame = append(frame, c)
		}
		values, decodeErr := decodeSpendJSON(frame, 1)
		if decodeErr != nil {
			return decodeErr
		}
		if err := visit(Row{Values: values, Number: number}); err != nil {
			return err
		}
		c, err = nextSpendJSONByte(b)
		if err != nil || c == ']' {
			break
		}
		if c != ',' {
			return errSpendJSON
		}
		c, err = nextSpendJSONByte(b)
		if err == nil && c == ']' {
			return errSpendJSON
		}
	}
	if err != nil {
		return spendJSONReadError(err)
	}
	_, err = nextSpendJSONByte(b)
	if err != io.EOF {
		return spendJSONReadError(err)
	}
	return nil
}

func readSpendLines(b *bufio.Reader, frame []byte, visit func(Row) error) error {
	number := 0
	for {
		frame = frame[:0]
		for {
			part, err := b.ReadSlice('\n')
			if len(part) > MaxRowBytes-len(frame) {
				return errSpendRow
			}
			frame = append(frame, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil && err != io.EOF {
				return err
			}
			if len(frame) == 0 && err == io.EOF {
				return nil
			}
			break
		}
		data := bytes.Trim(frame, " \t\r\n")
		if len(data) == 0 {
			continue
		}
		if number == MaxRows {
			return errSpendRows
		}
		values, err := decodeSpendJSON(data, 0)
		if err != nil {
			return err
		}
		number++
		if err := visit(Row{Values: values, Number: number}); err != nil {
			return err
		}
	}
}

// Preflight lexical sizes and structure before json.Decoder allocates tokens.
// Only a fixed stack and slices of the already bounded record are needed.
func preflightSpendJSON(data []byte, outerDepth int) error {
	if !utf8.Valid(data) {
		return errSpendUTF8
	}
	var fields [MaxDepth]int
	depth := outerDepth
	for i := 0; i < len(data); {
		switch data[i] {
		case '{', '[':
			if depth == MaxDepth {
				return errSpendDepth
			}
			fields[depth] = 0
			depth++
			i++
		case '}', ']':
			depth--
			if depth < outerDepth {
				return errSpendJSON
			}
			i++
		case ':':
			if depth == outerDepth {
				return errSpendJSON
			}
			fields[depth-1]++
			if fields[depth-1] > MaxFields {
				return errSpendFields
			}
			i++
		case '"':
			end, err := spendJSONStringEnd(data, i+1)
			if err != nil {
				return err
			}
			i = end
		default:
			if jsonSpace(data[i]) || data[i] == ',' {
				i++
				continue
			}
			start := i
			for i < len(data) && !jsonSpace(data[i]) && !bytes.ContainsRune([]byte(",:{}[]\""), rune(data[i])) {
				i++
				if i-start > MaxFieldBytes {
					return errSpendField
				}
			}
		}
	}
	return nil
}

func spendJSONStringEnd(data []byte, i int) (int, error) {
	size := 0
	for i < len(data) {
		if data[i] == '"' {
			return i + 1, nil
		}
		r, n := utf8.DecodeRune(data[i:])
		if data[i] == '\\' {
			i++
			if i == len(data) {
				return 0, errSpendJSON
			}
			r, n = rune(data[i]), 1
			if data[i] == 'u' {
				if len(data)-i < 5 {
					return 0, errSpendJSON
				}
				u, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
				if err != nil {
					return 0, errSpendJSON
				}
				r, n = rune(u), 5
				if utf16.IsSurrogate(r) {
					paired := false
					if u >= 0xD800 && u <= 0xDBFF && len(data)-i >= 11 && data[i+5] == '\\' && data[i+6] == 'u' {
						v, err := strconv.ParseUint(string(data[i+7:i+11]), 16, 16)
						if err == nil && v >= 0xDC00 && v <= 0xDFFF {
							r, n = utf16.DecodeRune(rune(u), rune(v)), 11
							paired = true
						}
					}
					if !paired {
						return 0, errSpendUTF8
					}
				}
			}
		}
		size += utf8.RuneLen(r)
		if size > MaxFieldBytes {
			return 0, errSpendField
		}
		i += n
	}
	return 0, errSpendJSON
}

func decodeSpendJSON(data []byte, outerDepth int) (map[string]string, error) {
	if err := preflightSpendJSON(data, outerDepth); err != nil {
		return nil, err
	}
	if !json.Valid(data) || len(data) == 0 || data[0] != '{' {
		return nil, errSpendJSON
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	_, _ = dec.Token() // json.Valid has checked the complete bounded record.
	values := make(map[string]string)
	if err := spendJSONObject(dec, values); err != nil {
		return nil, err
	}
	return values, nil
}

func spendJSONObject(dec *json.Decoder, values map[string]string) error {
	seen := make(map[string]bool)
	for dec.More() {
		token, _ := dec.Token()
		key := token.(string)
		if seen[key] {
			return errSpendDuplicate
		}
		seen[key] = true
		token, _ = dec.Token()
		if values != nil && allowedColumn(key) {
			switch value := token.(type) {
			case nil:
			case string:
				if value != "" {
					values[key] = value
				}
			case json.Number:
				values[key] = value.String()
			default:
				return errSpendScalar
			}
		} else if err := skipSpendJSONValue(dec, token); err != nil {
			return err
		}
	}
	_, _ = dec.Token()
	if values != nil {
		return checkSpendColumns(seen)
	}
	return nil
}

func skipSpendJSONValue(dec *json.Decoder, token json.Token) error {
	switch token {
	case json.Delim('{'):
		return spendJSONObject(dec, nil)
	case json.Delim('['):
		for dec.More() {
			token, _ := dec.Token()
			if err := skipSpendJSONValue(dec, token); err != nil {
				return err
			}
		}
		_, _ = dec.Token()
	}
	return nil
}
