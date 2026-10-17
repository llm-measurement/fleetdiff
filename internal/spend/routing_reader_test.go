// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

var routingPresenceColumns = []string{
	"original_model_group", "attempted_fallbacks", "internal_call_origin", "agent_id", "model", "model_id",
}

func collectRouting(t *testing.T, ext, body string) ([]Row, error) {
	t.Helper()
	path := readerWrite(t, ext, body)
	var rows []Row
	err := ReadRouting(path, func(row Row) error { rows = append(rows, row); return nil })
	after, readErr := os.ReadFile(path)
	if readErr != nil || string(after) != body {
		t.Fatal("routing read changed source")
	}
	return rows, err
}

func TestRoutingReaderPresenceAndLegacyRetention(t *testing.T) {
	for _, field := range routingPresenceColumns {
		for _, value := range []string{"null", `""`, `" "`, `"SENTINEL_ROUTING_VALUE"`, "0"} {
			t.Run(field+"/"+strconv.Itoa(len(value))+"/"+strconv.FormatBool(value == "null"), func(t *testing.T) {
				var scalar any
				if err := json.Unmarshal([]byte(value), &scalar); err != nil {
					t.Fatal("bad test scalar")
				}
				want := ""
				if scalar != nil {
					if s, ok := scalar.(string); ok {
						want = s
					} else {
						want = value
					}
				}
				object := readerExtra(strconv.Quote(field) + ":" + value)
				for ext, body := range map[string]string{
					".json": "[" + object + "]", ".jsonl": object + "\n",
					".csv": readerHeader + "," + field + "\nreq,completion,2026-10-08T00:00:00Z,10,2,12," + want + "\n",
				} {
					got, err := collectRouting(t, ext, body)
					if err != nil || len(got) != 1 {
						t.Fatal("routing scalar read failed")
					}
					actual, present := got[0].Values[field]
					if !present || actual != want || got[0].Number != 1 {
						t.Fatal("routing scalar presence/value changed")
					}
					legacy, err := readerCollect(t, ext, body)
					if err != nil || len(legacy) != 1 {
						t.Fatal("legacy read changed")
					}
					v, ok := legacy[0].Values[field]
					if ok != (!routingReaderColumn(field) && want != "") || ok && v != want {
						t.Fatal("legacy field retention changed")
					}
					delete(got[0].Values, field)
					delete(legacy[0].Values, field)
					if !reflect.DeepEqual(got, legacy) {
						t.Fatal("routing read changed base values or row count")
					}
				}
			})
		}
	}
	for ext, body := range map[string]string{".json": "[" + readerObject + "]", ".jsonl": readerObject,
		".csv": readerHeader + "\nreq,completion,2026-10-08T00:00:00Z,10,2,12\n"} {
		rows, err := collectRouting(t, ext, body)
		if err != nil || len(rows) != 1 {
			t.Fatal("old-column routing input failed")
		}
		for _, field := range routingPresenceColumns {
			if _, ok := rows[0].Values[field]; ok {
				t.Fatal("routing read invented an absent column")
			}
		}
	}
}

func TestRoutingReaderPartialRowsAndLexicalCounts(t *testing.T) {
	first := readerExtra(`"original_model_group":"SENTINEL_ROUTE","agent_id":null,"model":"SENTINEL_MODEL","model_id":"SENTINEL_DEPLOYMENT","attempted_fallbacks":0,"internal_call_origin":null,"rejection_kind":null,"metadata":{"original_model_group":"SENTINEL_IGNORED"}`)
	second := readerExtra(`"original_model_group":null,"agent_id":"SENTINEL_AGENT","attempted_fallbacks":"1","model_group":"SENTINEL_RECOVERY"`)
	for ext, body := range map[string]string{".json": "[" + first + "," + second + "]", ".jsonl": first + "\n" + second} {
		rows, err := collectRouting(t, ext, body)
		if err != nil || len(rows) != 2 || rows[1].Number != 2 {
			t.Fatal("partial routing records failed")
		}
		if rows[0].Values["original_model_group"] != "SENTINEL_ROUTE" || rows[1].Values["original_model_group"] != "" || rows[0].Values["agent_id"] != "" {
			t.Fatal("routing reader inferred identity or overwrote a row")
		}
		if _, ok := rows[1].Values["model_id"]; ok {
			t.Fatal("missing deployment became present")
		}
		if _, ok := rows[0].Values["rejection_kind"]; !ok {
			t.Fatal("existing rejection coverage lost")
		}
		if _, ok := rows[0].Values["metadata"]; ok {
			t.Fatal("nested private metadata retained")
		}
	}
	for _, value := range []string{"0", "1", "-1", "1.25", "1e3", "9223372036854775808", `"SENTINEL_INVALID_COUNT"`} {
		rows, err := collectRouting(t, ".jsonl", readerExtra(`"attempted_fallbacks":`+value))
		if err != nil || len(rows) != 1 || rows[0].Values["attempted_fallbacks"] != strings.Trim(value, `"`) {
			t.Fatal("reader parsed or normalized a fallback count")
		}
	}
}

func TestRoutingReaderErrorsRemainBoundedAndPrivate(t *testing.T) {
	for _, field := range routingPresenceColumns {
		for _, value := range []string{"true", "[]", `{"private":"SENTINEL_CONTENT"}`} {
			body := readerExtra(strconv.Quote(field) + ":" + value)
			for _, ext := range []string{".json", ".jsonl"} {
				input := body
				if ext == ".json" {
					input = "[" + input + "]"
				}
				_, err := collectRouting(t, ext, input)
				if !errors.Is(err, errSpendScalar) || strings.Contains(err.Error(), "SENTINEL") {
					t.Fatal("routing nonscalar rejection changed or leaked")
				}
				if routingReaderColumn(field) {
					if _, err := readerCollect(t, ext, input); err != nil {
						t.Fatal("legacy reader stopped ignoring routing fields")
					}
				}
			}
		}
		for _, n := range []int{MaxFieldBytes, MaxFieldBytes + 1} {
			for ext, body := range map[string]string{
				".jsonl": readerExtra(strconv.Quote(field) + ":" + strconv.Quote(strings.Repeat("x", n))),
				".csv":   readerHeader + "," + field + "\nreq,completion,now,1,2,3," + strings.Repeat("x", n) + "\n",
			} {
				_, err := collectRouting(t, ext, body)
				if n == MaxFieldBytes && err != nil || n > MaxFieldBytes && !errors.Is(err, errSpendField) {
					t.Fatal("routing field limit changed")
				}
			}
		}
	}
	for _, tc := range []struct {
		body string
		want error
	}{
		{readerExtra(`"original_model_group":null,"original_model_group":"SENTINEL_ROUTE"`), errSpendDuplicate},
		{readerExtra(`"agent_id":"\ud800"`), errSpendUTF8},
		{readerExtra(`"metadata":` + strings.Repeat("[", MaxDepth) + "0" + strings.Repeat("]", MaxDepth)), errSpendDepth},
		{readerObject + strings.Repeat(" ", MaxRowBytes), errSpendRow},
	} {
		_, err := collectRouting(t, ".jsonl", tc.body)
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "SENTINEL") {
			t.Fatal("routing structural bound or privacy changed")
		}
	}
	stop := errors.New("visitor stopped")
	path := readerWrite(t, ".jsonl", readerObject+"\n"+readerObject)
	visits := 0
	if err := ReadRouting(path, func(Row) error { visits++; return stop }); err != stop || visits != 1 {
		t.Fatal("routing reader did not preserve visitor stop")
	}
	link := filepath.Join(t.TempDir(), "SENTINEL_PATH.jsonl")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal("test symlink creation failed")
	}
	if err := ReadRouting(link, func(Row) error { return nil }); err != errSpendFile {
		t.Fatal("routing reader accepted a symlink")
	}
	if err := ReadRouting(path, nil); err == nil {
		t.Fatal("routing reader accepted a nil visitor")
	}
}

func FuzzRoutingReaderBoundedFormats(f *testing.F) {
	f.Add(uint8(0), readerHeader+"\nreq,completion,now,1,2,3\n")
	f.Add(uint8(0), readerHeader+",original_model_group,attempted_fallbacks,internal_call_origin,agent_id,model,model_id\nreq,completion,now,1,2,3,,0,,,,\n")
	f.Add(uint8(0), readerHeader+",original_model_group\nreq,completion,now,1,2,3,\"multi\nline\"\n")
	f.Add(uint8(1), "["+readerObject+"]")
	f.Add(uint8(1), "["+readerExtra(`"original_model_group":null,"attempted_fallbacks":0,"internal_call_origin":"","agent_id":null,"model":"","model_id":null`)+"]")
	f.Add(uint8(2), readerExtra(`"original_model_group":"sentinel-route","attempted_fallbacks":"1e99","internal_call_origin":null,"agent_id":"sentinel-agent","rejection_kind":null`))
	f.Add(uint8(2), readerExtra(`"metadata":{"original_model_group":"ignored","nested":[1,true,null]}`))
	f.Add(uint8(2), readerExtra(`"original_model_group":null,"original_model_\u0067roup":"duplicate"`))
	f.Add(uint8(2), readerExtra(`"original_model_group":[],"attempted_fallbacks":true`))
	f.Add(uint8(2), readerExtra(`"agent_id":"\ud83d\ude00"`))
	f.Add(uint8(2), readerExtra("\"original_model_group\":\"\xff\""))
	f.Fuzz(func(t *testing.T, format uint8, input string) {
		if len(input) > 2*MaxRowBytes {
			t.Skip()
		}
		ext := []string{".csv", ".json", ".jsonl"}[format%3]
		visits := 0
		_ = readSpendStreamMode(strings.NewReader(input), ext, MaxFileBytes, func(row Row) error {
			visits++
			if row.Number != visits || visits > MaxRows || len(row.Values) > MaxFields {
				t.Fatal("unbounded or misnumbered routing row")
			}
			for key, value := range row.Values {
				allowed, emptyAllowed := allowedColumn(key), key == "rejection_kind"
				switch key {
				case "original_model_group", "attempted_fallbacks", "internal_call_origin":
					allowed, emptyAllowed = true, true
				case "agent_id", "model", "model_id":
					emptyAllowed = true
				}
				if !allowed || value == "" && !emptyAllowed || len(value) > MaxFieldBytes || !utf8.ValidString(value) {
					t.Fatal("invalid retained routing field")
				}
			}
			return nil
		}, true)
	})
}
