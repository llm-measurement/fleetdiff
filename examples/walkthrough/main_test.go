// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
)

func TestWalkthrough(t *testing.T) {
	t.Chdir("../..")
	parent := t.TempDir()
	var first []byte
	for _, name := range []string{"first", "second"} {
		out := filepath.Join(parent, name)
		var text bytes.Buffer
		if err := run(out, "", &text); err != nil {
			t.Fatal(err)
		}
		published, err := os.ReadFile("docs/media/transcript.txt")
		if err != nil || !bytes.Equal(published, text.Bytes()) {
			t.Fatal("refresh the published walkthrough from actual demo output")
		}
		for _, want := range []string{"800 -> 360 (down 440)", "1200 -> 1800 (up 600)", "Model requests: 6 -> 10", "Observed root-agent runs: 2 -> 2", "Requests missing token usage: 2 -> 2", "Distinct users, estimated: 2 -> 2", "Distinct MCP resources, estimated: 1 -> 4", "Distinct MCP sessions, estimated: 4 -> 8", "[1, 1] -> [4, 4] occurrences; increased", "[0, 0] -> [1, 1] occurrences; increased", "[2, 2] -> [2, 2] occurrences; unchanged", "refused", "marked incomplete", "Reported work moved and grew; answer quality is outside what fleetdiff measures."} {
			if !strings.Contains(text.String(), want) {
				t.Fatalf("missing %q in walkthrough", want)
			}
		}
		for _, file := range []string{"comparison.json", "comparison.txt", "owned-only.json", "missing-operator.json", "walkthrough.txt"} {
			path := filepath.Join(out, file)
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 {
				t.Fatalf("missing %s: %v", file, err)
			}
			if bytes.Contains(data, []byte(`"hash":`)) || bytes.Contains(data, []byte("FC_PRIVATE_")) {
				t.Fatal("example report disclosed private content")
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("report permissions")
			}
			if file == "missing-operator.json" {
				var r compare.Report
				if json.Unmarshal(data, &r) != nil || r.Complete || len(r.After.MissingProducers) != 1 {
					t.Fatal("missing export was hidden")
				}
			}
			if file == "walkthrough.txt" && !bytes.Equal(data, text.Bytes()) {
				t.Fatal("saved transcript differs from terminal output")
			}
		}
		if first == nil {
			first = bytes.Clone(text.Bytes())
		} else if !bytes.Equal(first, text.Bytes()) {
			t.Fatal("repeated example changed the answer")
		}
		if err := run(out, "", io.Discard); err == nil {
			t.Fatal("existing output directory was overwritten")
		}
	}
}

func TestInvalidInputsProduceNoReports(t *testing.T) {
	out := filepath.Join(t.TempDir(), "reports")
	var text bytes.Buffer
	if err := run(out, filepath.Join(t.TempDir(), "missing"), &text); err == nil || text.Len() != 0 {
		t.Fatal("invalid inputs produced a walkthrough")
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid inputs created output")
	}
}

func TestLauncherHelpAndMissingGo(t *testing.T) {
	script, err := filepath.Abs("../demo.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"--help"}, 0, "Usage:"},
		{[]string{"--unknown"}, 2, "Usage:"},
		{[]string{"--live", "extra"}, 2, "Usage:"},
		{nil, 1, "Install Go"},
	} {
		cmd := exec.Command("/bin/sh", append([]string{script}, tt.args...)...)
		cmd.Env = []string{"PATH=" + t.TempDir()}
		data, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		if code != tt.code || !strings.Contains(string(data), tt.want) {
			t.Fatalf("args=%v code=%d output=%s", tt.args, code, data)
		}
	}
}
