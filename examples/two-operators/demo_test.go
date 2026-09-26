// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/examples/internal/scenario"
)

func TestFixturesMatchResearchScenario(t *testing.T) {
	recipe, err := scenario.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i, window := range recipe.Windows {
		for _, producer := range []string{"owned", "partner"} {
			actual, err := assets.ReadFile("fixtures/" + window.Name + "-" + producer + ".json")
			if err != nil {
				t.Fatal(err)
			}
			expected, err := json.MarshalIndent(recipe.OTLP(i)[producer], "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual, append(expected, '\n')) {
				t.Fatal("checked-in OTLP fixture differs from the tested research recipe")
			}
		}
	}
}

func TestOTLPSendRejectsPartialSuccessAndNeverRetries(t *testing.T) {
	for _, response := range []string{`{}`, `{"partialSuccess":{"rejectedSpans":"1"}}`, `{"partialSuccess":{"rejectedSpans":"0","errorMessage":"warning"}}`, `not-json`} {
		t.Run(response, func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("invalid OTLP request")
				}
				_, _ = w.Write([]byte(response))
			}))
			defer s.Close()
			err := send(t.Context(), s.URL, []byte(`{}`))
			if (err == nil) != (response == `{}`) || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestOTLPSendDoesNotFollowRedirect(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	if err := send(t.Context(), source.URL, []byte(`{}`)); err == nil || called {
		t.Fatal("redirect was accepted")
	}
}

func TestBoundedOutputAndWindowSelection(t *testing.T) {
	w := &limitedBuffer{limit: 4}
	if _, err := w.Write([]byte("12345")); err == nil || w.Len() > 4 {
		t.Fatal("output limit not enforced")
	}
	if err := noPrivateContent([]byte("FC_PRIVATE_MCP_URI")); err == nil {
		t.Fatal("missed sentinel")
	}
	if err := noPrivateContent([]byte("safe aggregate")); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	start := nextWindow(now)
	if !start.After(now) || start.UnixNano()%windowDuration.Nanoseconds() != 0 {
		t.Fatal("not a future aligned window")
	}
	var output bytes.Buffer
	if err := writeJSON(&output, struct{ A string }{"ok"}); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(output.Bytes()) {
		t.Fatal("invalid JSON")
	}
	if err := writeJSON(&limitedBuffer{limit: 1}, "too long"); !errors.Is(err, errOutputLimit) {
		t.Fatal("write failure lost")
	}
}
