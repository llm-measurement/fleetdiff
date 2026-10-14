// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package attribution

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/canon"
)

func TestExistingSpendIdentityBytesPreserved(t *testing.T) {
	key := []byte("synthetic-regression-key-32-bytes-long")
	for _, parts := range [][]string{{" Key A ", "S\r\nession"}, {"caf\u0065\u0301"}, {"\u001c"}, {""}} {
		legacy := []string{"litellm-spend/v1", "session"}
		for _, p := range parts {
			b, e := canon.CanonicalizeString(canon.TextV1, p)
			if e != nil {
				t.Fatal(e)
			}
			legacy = append(legacy, string(b))
		}
		b, _ := json.Marshal(legacy)
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte("user:v1\x00"))
		mac.Write(b)
		got, e := Identity(key, "litellm-spend/v1", "session", parts...)
		if e != nil {
			t.Fatal(e)
		}
		if got != [32]byte(mac.Sum(nil)) {
			t.Fatalf("existing spend identity changed: %x != %x", got, mac.Sum(nil))
		}
	}
}
