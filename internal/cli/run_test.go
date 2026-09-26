// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"slices"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/compare"
	"github.com/llm-measurement/llm-sketchkit/go/sketchkit/summary"
)

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("SENTINEL_WRITE_ERROR") }

func TestBuildVersion(t *testing.T) {
	original := Version
	t.Cleanup(func() { Version = original })
	for _, tc := range []struct {
		name, stamp string
		info        *debug.BuildInfo
		want        string
	}{
		{"no build info", "dev", nil, "dev"},
		{"empty module version", "dev", &debug.BuildInfo{}, "dev"},
		{"checkout", "dev", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "dev"},
		{"tagged install", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.1.0"},
		{"pseudo version", "dev", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.1-0.20260925120000-abcdef123456"}}, "v0.1.1-0.20260925120000-abcdef123456"},
		{"release stamp", "v0.2.0", &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0"}}, "v0.2.0"},
		{"stamp without build info", "v0.2.0", nil, "v0.2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			Version = tc.stamp
			if got := buildVersion(tc.info); got != tc.want {
				t.Fatalf("buildVersion() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestVersionAndOutputErrors(t *testing.T) {
	a, b := inputs(t)
	for _, args := range [][]string{
		{"--version"}, {"--help"}, {"compare", "--help"},
		{"compare", "--before", a, "--after", b, "--expected", "operator"},
	} {
		var errout bytes.Buffer
		if code := Run(args, failedWriter{}, &errout); code != 1 || strings.Contains(errout.String(), "SENTINEL") {
			t.Fatal("output failure was not handled privately", code, errout.String())
		}
	}
	var out, errout bytes.Buffer
	if Run([]string{"--version"}, &out, &errout) != 0 || !strings.HasPrefix(out.String(), "fleetdiff dev (unknown) go") {
		t.Fatal("missing build identity", out.String(), errout.String())
	}
}

func inputs(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "before.json"), filepath.Join(dir, "after.json")}
	for i, path := range paths {
		start := int64(i+1) * 60_000_000_000
		e := summary.Envelope{Version: 1, Sequence: 1, ProducerID: "operator", Epoch: "SENTINEL_EPOCH", ScopeID: "SENTINEL_SCOPE", KeyID: "SENTINEL_KEY", AccountingID: "SENTINEL_ACCOUNTING", WindowStart: start, WindowDuration: 60_000_000_000, ObservedStart: start, ObservedEnd: start + 60_000_000_000, EmittedAt: start + 60_000_000_000, Counters: map[string]uint64{"requests": uint64(i + 2), "missing_token_usage": 1}, Sketches: map[string]summary.Payload{}}
		data, err := e.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return paths[0], paths[1]
}

func TestCompareTextAndJSON(t *testing.T) {
	a, b := inputs(t)
	for _, format := range []string{"text", "json"} {
		var out, errout bytes.Buffer
		code := Run([]string{"compare", "--before", a, "--after", b, "--expected", "operator", "--format", format}, &out, &errout)
		if code != 0 || errout.Len() != 0 {
			t.Fatal(code, errout.String())
		}
		if strings.Contains(out.String(), "SENTINEL") || strings.Contains(out.String(), a) {
			t.Fatal("metadata or paths leaked")
		}
		if format == "json" && !json.Valid(out.Bytes()) {
			t.Fatal("invalid report JSON")
		}
		if format == "text" && !strings.Contains(out.String(), "requests") {
			t.Fatal("missing counters")
		}
	}
}

func TestNoPartialOutputOnErrors(t *testing.T) {
	a, b := inputs(t)
	for _, args := range [][]string{
		{"SENTINEL_COMMAND"},
		{"compare", "--SENTINEL_FLAG"},
		{"compare", "--before", a, "--after", b, "--expected", "operator", "--format", "SENTINEL_FORMAT"},
		{"compare", "--before", "SENTINEL_PATH", "--after", b, "--expected", "operator"},
		{"compare", "--before", a, "--after", b, "--expected", "operator", "--before-window", "bad-SENTINEL"},
		{"compare", "--before", a, "--after", b},
		{"compare", "--before", a, "--after", b, "--expected", "operator", "--top", "0"},
	} {
		var out, errout bytes.Buffer
		if Run(args, &out, &errout) == 0 || out.Len() != 0 || strings.Contains(errout.String(), "SENTINEL") {
			t.Fatalf("unsafe failure: %s %s", out.String(), errout.String())
		}
	}
}

func TestPartialRequiresExplicitOptIn(t *testing.T) {
	a, b := inputs(t)
	args := []string{"compare", "--before", a, "--after", b, "--expected", "operator,offline", "--format", "json"}
	var out, errout bytes.Buffer
	if Run(args, &out, &errout) == 0 || out.Len() != 0 {
		t.Fatal("partial input not rejected")
	}
	out.Reset()
	errout.Reset()
	args = append(args, "--allow-partial")
	if Run(args, &out, &errout) != 0 || !strings.Contains(out.String(), "PARTIAL") || strings.Contains(out.String(), "offline") {
		t.Fatal("partial coverage not safely reported", errout.String())
	}
}

func TestHelp(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"compare", "--help"}} {
		var out, errout bytes.Buffer
		if Run(args, &out, &errout) != 0 || !strings.Contains(out.String(), "compare") {
			t.Fatal("help failed")
		}
	}
}

func TestCommittedExampleAndOptInHashes(t *testing.T) {
	for _, show := range []bool{false, true} {
		args := []string{"compare", "--before", "../../examples/two-systems/data/before", "--after", "../../examples/two-systems/data/after", "--expected", "owned,partner", "--format", "json"}
		if show {
			args = append(args, "--show-hashes")
		}
		var out, errout bytes.Buffer
		if code := Run(args, &out, &errout); code != 0 {
			t.Fatal(code, errout.String())
		}
		var r compare.Report
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(out.Bytes(), &object); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		want := []string{"after", "before", "complete_observation_intervals", "concentration", "counters", "distinct", "notes", "omitted_measurements", "version"}
		if !reflect.DeepEqual(keys, want) || string(object["version"]) != "1" {
			t.Fatal("review changes to the report v1 contract", keys)
		}
		if !r.Complete || r.Concentration[0].Name != "top_prompts" || r.Concentration[0].BeforeWeight != 1200 || r.Concentration[0].AfterWeight != 1800 {
			t.Fatal("example totals changed")
		}
		for _, m := range r.Concentration[0].Movers {
			if (len(m.Hash) == 16) != show {
				t.Fatal("hash opt-in failed")
			}
		}
	}
}

func TestExpectedProducerWhitespace(t *testing.T) {
	var baseline string
	for _, expected := range []string{"owned,partner", " owned, partner ", "\towned\t,\npartner\n"} {
		var out, errout bytes.Buffer
		code := Run([]string{"compare", "--before", "../../examples/two-systems/data/before", "--after", "../../examples/two-systems/data/after", "--expected", expected, "--format", "json"}, &out, &errout)
		if code != 0 || errout.Len() != 0 {
			t.Fatal(code, errout.String())
		}
		if baseline == "" {
			baseline = out.String()
		} else if out.String() != baseline {
			t.Fatal("whitespace changed report or producer aliases")
		}
	}
}

func TestExpectedProducerErrorsRemainPrivate(t *testing.T) {
	a, b := inputs(t)
	for _, tc := range []struct{ expected, cause string }{
		{"operator, ", "invalid expected producer"},
		{" ,operator", "invalid expected producer"},
		{"operator, ,offline", "invalid expected producer"},
		{"operator, operator ", "duplicate expected producer"},
		{"operator, /SENTINEL_PATH", "invalid expected producer"},
		{"SENTINEL_PRODUCER", "unexpected summary producer"},
	} {
		var out, errout bytes.Buffer
		code := Run([]string{"compare", "--before", a, "--after", b, "--expected", tc.expected}, &out, &errout)
		if code != 1 || out.Len() != 0 || errout.String() != "before window: "+tc.cause+"\n" {
			t.Fatalf("want private cause %q and no report, got code=%d stdout=%q stderr=%q", tc.cause, code, out.String(), errout.String())
		}
	}
}
