//go:build integration

// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTwoOperatorsThroughReleasedCollectors(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	out := filepath.Join(t.TempDir(), "run")
	var progress bytes.Buffer
	if err := runDemo(ctx, out, &progress); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"comparison.json", "comparison.txt", "owned-only.json", "checks.json", "missing-operator.json", "partial-interval.json"} {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil || len(data) == 0 {
			t.Fatalf("missing result %s", name)
		}
		if err := noPrivateContent(data); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(out, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("report permissions")
		}
	}
	if err := runDemo(ctx, out, &progress); err == nil {
		t.Fatal("existing output accepted")
	}
}
