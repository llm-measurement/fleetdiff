// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestHostileYAML(t *testing.T) {
	inputs := map[string]string{
		"empty": "", "scalar": "SECRET", "sequence": "[SECRET]",
		"syntax": "secret: [SECRET", "duplicate": "secret: one\nsecret: two\n",
		"decoded-duplicate": "secret: one\n\"\\x73ecret\": two\n",
		"nested-duplicate":  "x: {secret: one, secret: two}",
		"documents":         "x: one\n---\nx: two\n", "trailing-document": "x: one\n---\n",
		"complex-key": "? [one, two]\n: secret", "non-string-key": "123: secret",
		"anchor": "a: &x secret", "alias": "a: &x secret\nb: *x",
		"recursive-alias": "a: &x [*x]", "merge": "a: &x {b: secret}\nc: {<<: *x}",
		"merge-without-alias": "a: {<<: {secret: value}}",
		"depth":               "a: " + strings.Repeat("[", MaxDepth+2) + "secret" + strings.Repeat("]", MaxDepth+2),
		"parser-depth":        "a: " + strings.Repeat("[", 11000) + "secret" + strings.Repeat("]", 11000),
		"nodes":               "a: [" + strings.Repeat("x,", MaxNodes) + "x]",
		"alias-expansion":     "a: &a [secret, secret, secret]\nb: &b [*a, *a, *a]\nc: &c [*b, *b, *b]\nd: [*c, *c, *c]",
		"nul":                 "a: \x00secret", "invalid-utf8": "a: \xff",
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			r := check(t, input)
			if r.ExitCode() != 4 || !hasFinding(r, "unsupported_yaml") || r.ConfigurationParsed {
				t.Fatalf("expected rejected YAML: %+v", r)
			}
		})
	}
}

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, errors.New("PRIVATE_IO_VALUE") }

func TestInputFailures(t *testing.T) {
	for _, in := range []string{strings.Repeat(" ", MaxInputBytes+1)} {
		if _, err := Diagnose("-", strings.NewReader(in)); err == nil {
			t.Fatal("byte limit not enforced")
		}
	}
	if _, err := Diagnose("-", nil); err == nil {
		t.Fatal("nil stdin accepted")
	}
	if _, err := Diagnose("-", brokenReader{}); err == nil || strings.Contains(err.Error(), "PRIVATE_IO_VALUE") {
		t.Fatal("reader error leaked")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(base, "private-config.yaml")
	if err := os.WriteFile(regular, []byte(fixture(t, "safe")), 0600); err != nil {
		t.Fatal(err)
	}
	if r, err := Diagnose(regular, nil); err != nil || r.ExitCode() != 0 {
		t.Fatalf("regular file rejected: %v", err)
	}
	link := filepath.Join(base, "private-link.yaml")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(base, "private-directory")
	if err := os.Symlink(base, dirLink); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(base, "private-fifo.yaml")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(dirLink, "private-config.yaml"), base, fifo, filepath.Join(base, "absent-private")} {
		if _, err := Diagnose(path, nil); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid file accepted or path leaked")
		}
	}
	if err := os.WriteFile(regular, []byte(strings.Repeat(" ", MaxInputBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Diagnose(regular, nil); err == nil {
		t.Fatal("large file accepted")
	}
}

func TestBoundedDeterministicFindings(t *testing.T) {
	input := fixture(t, "safe")
	for i := 0; i < MaxFindings*2; i++ {
		input += fmt.Sprintf("private_%d: ${env:PRIVATE}\n", i)
	}
	r := check(t, input)
	if r.ExitCode() != 4 || len(r.Findings) != MaxFindings+1 || !hasFinding(r, "findings_truncated") {
		t.Fatal("findings not bounded")
	}
	want, _ := json.Marshal(r)
	for i := 0; i < 20; i++ {
		got, _ := json.Marshal(check(t, input))
		if string(got) != string(want) {
			t.Fatal("unstable report")
		}
	}
	for _, f := range r.Findings {
		if Description(f.ID) == "" || (f.Severity != "blocking" && f.Severity != "unsupported") {
			t.Fatal("finding outside allowlist")
		}
	}
}

func FuzzDiagnose(f *testing.F) {
	for _, input := range []string{"{}", "x: &x [*x]", ShadowConfig(), "? [a,b]\n: c"} {
		f.Add(input)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > MaxInputBytes {
			return
		}
		r, err := Diagnose("-", strings.NewReader(input))
		if err != nil {
			t.Fatal("bounded in-memory input must report unsupported syntax")
		}
		if len(r.Findings) > MaxFindings+1 {
			t.Fatal("unbounded findings")
		}
		for _, finding := range r.Findings {
			if Description(finding.ID) == "" {
				t.Fatal("unallowlisted finding")
			}
		}
	})
}
