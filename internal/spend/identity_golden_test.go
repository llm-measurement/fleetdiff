// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Freeze the legacy private helper's bytes, including canonicalization edges.
func TestLegacyIdentityGoldens(t *testing.T) {
	type vector struct {
		Kind  string   `json:"kind"`
		Parts []string `json:"parts"`
		Hash  string   `json:"hash"`
	}
	cases := []vector{
		{Kind: "session", Parts: []string{" Key A ", "S\r\nession"}},
		{Kind: "user", Parts: []string{"cafe\u0301"}},
		{Kind: "key", Parts: []string{"\u001c\u001d\u001e\u001f"}},
		{Kind: "team", Parts: []string{""}},
		{Kind: "session", Parts: []string{"a" + strings.Repeat("\u0301", 31), "\U00011f41\U00011f3e"}},
	}
	key := []byte("synthetic-refactor-regression-key-32-bytes")
	for i := range cases {
		h, err := identity(key, cases[i].Kind, cases[i].Parts...)
		if err != nil {
			t.Fatal(err)
		}
		cases[i].Hash = hex.EncodeToString(h[:])
	}
	got, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/legacy-identity.json"
	if os.Getenv("FLEETDIFF_UPDATE_REFACTOR_GOLDENS") == "1" {
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("identity bytes differ from v0.8.0")
	}
}
