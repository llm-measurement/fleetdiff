// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/diagnose"
)

func diagnosisFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../examples/diagnose/" + name + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDiagnoseCLI(t *testing.T) {
	for _, name := range []string{"safe", "unsafe", "unsupported"} {
		for _, format := range []string{"text", "json", "shadow"} {
			t.Run(name+"/"+format, func(t *testing.T) {
				args := []string{"--format", format, "-"}
				if format == "shadow" {
					args = []string{"--shadow-config", "-"}
				}
				var out, errout bytes.Buffer
				code := runDiagnose(args, strings.NewReader(diagnosisFixture(t, name)), &out, &errout)
				want := map[string]int{"safe": 0, "unsafe": 3, "unsupported": 4}[name]
				if code != want {
					t.Fatalf("code %d, want %d: %s", code, want, out.String()+errout.String())
				}
				if format == "json" {
					var r diagnose.Report
					if err := json.Unmarshal(out.Bytes(), &r); err != nil {
						t.Fatal(err)
					}
					if r.ExitCode() != want {
						t.Fatal("incorrect JSON status")
					}
				}
				if format == "shadow" {
					if out.String() != diagnose.ShadowConfig() {
						t.Fatal("proposal must be independent of input")
					}
					if errout.Len() == 0 {
						t.Fatal("missing input report")
					}
				} else if errout.Len() != 0 {
					t.Fatal("unexpected stderr")
				}
			})
		}
	}
}

func TestDiagnoseActionableText(t *testing.T) {
	var out, errout bytes.Buffer
	if runDiagnose([]string{"-"}, strings.NewReader(diagnosisFixture(t, "unsafe")), &out, &errout) != 3 {
		t.Fatal("expected blocking findings")
	}
	for _, want := range []string{"2 problems to fix:", "connectors.genaisketch.operation_filter.llm_operations (line 10)", "connectors.genaisketch.slices[0].keys (line 12)", "enduser.id is a metric label", "use a hashed field instead"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
	if strings.Count(out.String(), "(line 12)") != 1 || strings.Contains(out.String(), "Limit:") || strings.Contains(out.String(), "node-") {
		t.Fatal("locations should be grouped, without repeated limits or node ordinals")
	}
	out.Reset()
	if runDiagnose([]string{"-"}, strings.NewReader(diagnosisFixture(t, "safe")), &out, &errout) != 0 || out.String() != "Looks safe: no blocking findings.\n" {
		t.Fatal("safe result should be one plain line")
	}
}

func TestDiagnoseOptionsAndPrivacy(t *testing.T) {
	const secret = "SENTINEL_PRIVATE_2d97"
	for _, args := range [][]string{{}, {"--format", secret, "-"}, {"--" + secret}, {"--format"}, {"--shadow-config", "--format", "text", "-"}, {"-", secret}} {
		var out, errout bytes.Buffer
		if runDiagnose(args, strings.NewReader(""), &out, &errout) != 2 {
			t.Fatal("expected options failure")
		}
		if strings.Contains(out.String()+errout.String(), secret) {
			t.Fatal("options leaked")
		}
	}
	inputs := []string{
		"'" + secret + "': [broken", "? [" + secret + "]: custom", "foo: " + secret + "\nfoo: duplicate",
		strings.ReplaceAll(diagnosisFixture(t, "unsafe"), "genaisketch", "genaisketch/"+secret),
		strings.ReplaceAll(diagnosisFixture(t, "safe"), "127.0.0.1:14317", "${env:"+secret+"}"),
		strings.ReplaceAll(diagnosisFixture(t, "unsafe"), "enduser.id", secret+".email"),
		strings.ReplaceAll(diagnosisFixture(t, "unsafe"), "identity_label", secret),
		diagnosisFixture(t, "unsafe") + "\n" + secret + ": [" + secret + "]\n",
	}
	t.Setenv(secret, "ENVIRONMENT_VALUE_MUST_NOT_BE_READ")
	for _, input := range inputs {
		for _, args := range [][]string{{"-"}, {"--format", "json", "-"}, {"--shadow-config", "-"}} {
			var out, errout bytes.Buffer
			runDiagnose(args, strings.NewReader(input), &out, &errout)
			for _, value := range []string{secret, "ENVIRONMENT_VALUE_MUST_NOT_BE_READ"} {
				if strings.Contains(out.String()+errout.String(), value) {
					t.Fatal("input leaked")
				}
			}
		}
	}
	var out, errout bytes.Buffer
	if runDiagnose([]string{"/" + secret + "/absent"}, nil, &out, &errout) != 1 {
		t.Fatal("expected input failure")
	}
	if strings.Contains(errout.String(), secret) {
		t.Fatal("path leaked")
	}
}

func TestDiagnosisLocationsAndJSONLimits(t *testing.T) {
	var out, errout bytes.Buffer
	if runDiagnose([]string{"--format", "json", "-"}, strings.NewReader(diagnosisFixture(t, "unsafe")), &out, &errout) != 3 {
		t.Fatal("expected unsafe JSON report")
	}
	var r diagnose.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Limits) != 4 || len(r.Findings) != 5 {
		t.Fatal("JSON must retain each check and all limits", r)
	}
	for _, f := range r.Findings {
		if f.Reference == 0 || f.Path == "" || f.Line == 0 || f.Column == 0 {
			t.Fatal("missing source location", f)
		}
		if f.ID == "sensitive_slice" && (f.Attribute != "enduser.id" || f.Line != 12) {
			t.Fatal("missing known attribute or incorrect line", f)
		}
	}
}

type diagnosisBrokenWriter struct{}

func (diagnosisBrokenWriter) Write([]byte) (int, error) { return 0, errors.New("SENTINEL_PRIVATE_IO") }

type diagnosisShortWriter struct{}

func (diagnosisShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestDiagnoseOutputFailures(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-"}, {"--format", "json", "-"}, {"--shadow-config", "-"}} {
		var errout bytes.Buffer
		if runDiagnose(args, strings.NewReader(diagnosisFixture(t, "safe")), diagnosisBrokenWriter{}, &errout) != 1 {
			t.Fatal("expected output failure")
		}
		if strings.Contains(errout.String(), "SENTINEL_PRIVATE_IO") {
			t.Fatal("writer error leaked")
		}
	}
	var out bytes.Buffer
	if runDiagnose([]string{"--shadow-config", "-"}, strings.NewReader(diagnosisFixture(t, "safe")), &out, diagnosisBrokenWriter{}) != 1 || out.Len() != 0 {
		t.Fatal("failed report must prevent proposal")
	}
	for _, args := range [][]string{{"--help"}, {"-"}, {"--shadow-config", "-"}} {
		var errout bytes.Buffer
		if runDiagnose(args, strings.NewReader(diagnosisFixture(t, "safe")), diagnosisShortWriter{}, &errout) != 1 {
			t.Fatal("short write accepted")
		}
	}
}

func TestDiagnoseHelpAndMalformedShadow(t *testing.T) {
	var out, errout bytes.Buffer
	if runDiagnose([]string{"--help"}, nil, &out, &errout) != 0 || out.String() != diagnoseHelp {
		t.Fatal("missing help")
	}
	out.Reset()
	if runDiagnose([]string{"--shadow-config", "-"}, strings.NewReader("bad: ["), &out, &errout) != 4 || out.Len() != 0 {
		t.Fatal("malformed YAML must not produce a proposal")
	}
}

func TestReleaseBinaryDiagnose(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test a packaged executable")
	}
	for _, tc := range []struct {
		name string
		code int
	}{{"safe", 0}, {"unsafe", 3}, {"unsupported", 4}} {
		for _, format := range []string{"text", "json", "shadow"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				args := []string{"diagnose", "--format", format, "-"}
				if format == "shadow" {
					args = []string{"diagnose", "--shadow-config", "-"}
				}
				cmd := exec.Command(binary, args...)
				input := strings.ReplaceAll(diagnosisFixture(t, tc.name), "genaisketch", "genaisketch/SENTINEL_PRIVATE_COMPONENT")
				cmd.Stdin = strings.NewReader(input)
				var out, errout bytes.Buffer
				cmd.Stdout, cmd.Stderr = &out, &errout
				err := cmd.Run()
				code := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatal("cannot execute packaged binary")
					}
					code = exit.ExitCode()
				}
				if code != tc.code {
					t.Fatalf("packaged status %d, want %d", code, tc.code)
				}
				if strings.Contains(out.String()+errout.String(), "SENTINEL") {
					t.Fatal("packaged privacy failure")
				}
				if format == "json" {
					var r diagnose.Report
					if json.Unmarshal(out.Bytes(), &r) != nil || r.Version != 1 || r.ExitCode() != tc.code {
						t.Fatal("packaged diagnosis JSON failed")
					}
				}
				if format == "shadow" && out.String() != diagnose.ShadowConfig() {
					t.Fatal("packaged shadow proposal changed")
				}
			})
		}
	}
}
