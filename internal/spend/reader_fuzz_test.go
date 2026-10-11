// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzReaderBoundedFormats(f *testing.F) {
	f.Add(uint8(0), readerHeader+"\nreq,completion,now,1,2,3\n")
	f.Add(uint8(0), readerHeader+"\n\"multi\nline\",completion,now,1,2,3\n")
	f.Add(uint8(1), "["+readerObject+"]")
	f.Add(uint8(1), `[{"metadata":{"nested":[1,true,null]}}]`)
	f.Add(uint8(2), readerObject+"\n")
	f.Add(uint8(2), readerExtra(`"metadata":{"x":1,"\u0078":2}`))
	f.Add(uint8(2), readerExtra(`"model":"\ud83d\ude00"`))
	f.Add(uint8(2), readerExtra("\"model\":\"\xff\""))
	f.Fuzz(func(t *testing.T, format uint8, input string) {
		if len(input) > 2*MaxRowBytes {
			t.Skip()
		}
		ext := []string{".csv", ".json", ".jsonl"}[format%3]
		visits := 0
		_ = readSpendStream(strings.NewReader(input), ext, MaxFileBytes, func(row Row) error {
			visits++
			if row.Number != visits || visits > MaxRows || len(row.Values) > MaxFields {
				t.Fatal("unbounded or misnumbered row")
			}
			for key, value := range row.Values {
				if !allowedColumn(key) || value == "" || len(value) > MaxFieldBytes || !utf8.ValidString(value) {
					t.Fatal("invalid retained field")
				}
			}
			return nil
		})
	})
}
