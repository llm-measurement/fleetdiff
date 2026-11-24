// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scripts

import (
	"bufio"
	"context"
	"fmt"
	"go/version"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

type testedWorkflow struct {
	On   map[string]yaml.Node         `yaml:"on"`
	Jobs map[string]testedWorkflowJob `yaml:"jobs"`
}

type testedWorkflowJob struct {
	If              string               `yaml:"if"`
	Needs           yaml.Node            `yaml:"needs"`
	RunsOn          string               `yaml:"runs-on"`
	TimeoutMinutes  int                  `yaml:"timeout-minutes"`
	ContinueOnError any                  `yaml:"continue-on-error"`
	Env             map[string]string    `yaml:"env"`
	Steps           []testedWorkflowStep `yaml:"steps"`
}

type testedWorkflowStep struct {
	ID              string            `yaml:"id"`
	Name            string            `yaml:"name"`
	Uses            string            `yaml:"uses"`
	If              string            `yaml:"if"`
	ContinueOnError any               `yaml:"continue-on-error"`
	With            map[string]string `yaml:"with"`
	Env             map[string]string `yaml:"env"`
	Run             string            `yaml:"run"`
	Shell           string            `yaml:"shell"`
}

func TestWorkflowGoToolchainPins(t *testing.T) {
	release := readTestWorkflow(t, "release.yml")
	build := testWorkflowJob(t, release, "build")
	setups := testWorkflowGoSetups(t, build)
	pin := setups[0].With["go-version"]
	if !version.IsValid("go"+pin) || strings.Count(pin, ".") != 2 {
		t.Fatalf("release build must declare a literal Go patch version, got %q", pin)
	}
	if pin != "1.26.11" {
		t.Fatalf("release build Go pin = %q, want 1.26.11", pin)
	}

	for _, target := range []struct{ file, job, pin string }{
		{"ci.yml", "test", pin},
		{"security.yml", "scan", pin},
		{"security.yml", "fuzz", "1.27.4"},
		{"codeql.yml", "analyze", pin},
		{"release.yml", "build", pin},
		{"release.yml", "verify", pin},
	} {
		t.Run(target.file+"/"+target.job, func(t *testing.T) {
			job := testWorkflowJob(t, readTestWorkflow(t, target.file), target.job)
			assertWorkflowBlocking(t, "job", job.ContinueOnError)
			for i, step := range job.Steps {
				assertWorkflowBlocking(t, fmt.Sprintf("step %d (%s)", i, step.Name), step.ContinueOnError)
			}
			setups := testWorkflowGoSetups(t, job)
			if len(setups) != 1 {
				t.Errorf("want exactly one setup-go step, got %d", len(setups))
			}
			for i, step := range setups {
				if got := step.With["go-version"]; got != target.pin {
					t.Errorf("setup-go %d uses %q, want exact pin %q", i, got, target.pin)
				}
			}
		})
	}
}

func TestWorkflowSecurityReleaseScan(t *testing.T) {
	workflow := readTestWorkflow(t, "security.yml")
	if _, ok := workflow.On["workflow_dispatch"]; !ok {
		t.Error("security workflow must retain manual dispatch")
	}
	var schedule []struct {
		Cron string `yaml:"cron"`
	}
	node, ok := workflow.On["schedule"]
	if !ok {
		t.Error("security workflow must retain scheduled runs")
	} else if err := node.Decode(&schedule); err != nil {
		t.Fatalf("decode security schedule: %v", err)
	} else if len(schedule) == 0 {
		t.Error("security schedule must not be empty")
	}
	for _, entry := range schedule {
		if strings.TrimSpace(entry.Cron) == "" {
			t.Error("security schedule must include a cron expression")
		}
	}

	job := testWorkflowJob(t, workflow, "scan")
	assertWorkflowBlocking(t, "scan job", job.ContinueOnError)
	if job.If != "" {
		t.Errorf("security scan must not be conditionally skipped: %q", job.If)
	}
	if job.Needs.Kind != 0 {
		t.Error("security scan must run independently of the fuzz job")
	}
	if got := job.Env["GOTOOLCHAIN"]; got != "local" {
		t.Errorf("scan job GOTOOLCHAIN = %q, want local", got)
	}

	previous := -1
	for _, check := range []struct{ id, command, condition string }{
		{"toolchain", "go version", ""},
		{"vulnerabilities", "go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -scan=symbol ./...", ""},
		{"history", "go run github.com/zricethezav/gitleaks/v8@v8.30.1 git --redact=100 --verbose --log-opts=--all --no-banner .", "${{ !cancelled() }}"},
		{"race", "go test -race ./...", "${{ !cancelled() }}"},
	} {
		step, index := testWorkflowStepByID(t, job, check.id)
		if index <= previous {
			t.Errorf("%s must follow the preceding security check", check.id)
		}
		previous = index
		assertWorkflowBlocking(t, check.id, step.ContinueOnError)
		if step.If != check.condition {
			t.Errorf("%s if = %q, want %q", check.id, step.If, check.condition)
		}
		if step.Uses != "" || strings.TrimSpace(step.Run) != check.command {
			t.Errorf("%s must run the blocking command %q, got uses=%q run=%q", check.id, check.command, step.Uses, step.Run)
		}
		if override, ok := step.Env["GOTOOLCHAIN"]; ok && override != "local" {
			t.Errorf("%s overrides the release toolchain with %q", check.id, override)
		}
		if step.Shell != "" && step.Shell != "bash" && step.Shell != "sh" {
			t.Errorf("%s overrides fail-closed shell defaults: %q", check.id, step.Shell)
		}
	}

	checkout := false
	for _, step := range job.Steps {
		if step.ID == "fuzz" || strings.Contains(step.Run, "-fuzz") {
			t.Error("timed fuzzing must run only in the separate fuzz job")
		}
		if strings.HasPrefix(step.Uses, "actions/checkout@") {
			checkout = true
			if step.With["fetch-depth"] != "0" || step.With["persist-credentials"] != "false" {
				t.Error("history scan requires full history and no persisted checkout credentials")
			}
		}
	}
	if !checkout {
		t.Error("security scan must check out repository history")
	}
}

func TestWorkflowSecurityTimedFuzz(t *testing.T) {
	job := testWorkflowJob(t, readTestWorkflow(t, "security.yml"), "fuzz")
	assertWorkflowBlocking(t, "fuzz job", job.ContinueOnError)
	if job.If != "" || job.Needs.Kind != 0 {
		t.Error("fuzz job must run unconditionally and independently of the scan job")
	}
	if job.RunsOn != "ubuntu-24.04" || job.TimeoutMinutes != 20 {
		t.Errorf("fuzz job must retain ubuntu-24.04 and a 20-minute timeout, got %q and %d", job.RunsOn, job.TimeoutMinutes)
	}
	if got := job.Env["GOTOOLCHAIN"]; got != "local" {
		t.Errorf("fuzz job GOTOOLCHAIN = %q, want local", got)
	}
	if len(job.Steps) != 5 {
		t.Errorf("want checkout, setup-go, toolchain record, timed fuzzing and summary, got %d steps", len(job.Steps))
	}
	for i, step := range job.Steps {
		assertWorkflowBlocking(t, fmt.Sprintf("fuzz step %d (%s)", i, step.Name), step.ContinueOnError)
		if step.Name != "Fuzz check summary" && step.If != "" {
			t.Errorf("fuzz step %d must not be conditionally skipped: %q", i, step.If)
		}
		if override, ok := step.Env["GOTOOLCHAIN"]; ok && override != "local" {
			t.Errorf("fuzz step %d overrides the pinned toolchain with %q", i, override)
		}
		if step.Shell != "" && step.Shell != "bash" && step.Shell != "sh" {
			t.Errorf("fuzz step %d overrides fail-closed shell defaults: %q", i, step.Shell)
		}
	}
	if len(job.Steps) < 2 || !strings.HasPrefix(job.Steps[0].Uses, "actions/checkout@") ||
		job.Steps[0].With["persist-credentials"] != "false" ||
		!strings.HasPrefix(job.Steps[1].Uses, "actions/setup-go@") || job.Steps[1].With["go-version"] != "1.27.4" {
		t.Fatal("fuzz job must check out without persisted credentials, then set up exact Go 1.27.4")
	}
	toolchain, toolchainIndex := testWorkflowStepByID(t, job, "toolchain")
	if toolchainIndex != 2 || toolchain.Uses != "" || strings.TrimSpace(toolchain.Run) != "go version" {
		t.Error("fuzz job must record the pinned toolchain immediately after setup-go")
	}
	fuzz, fuzzIndex := testWorkflowStepByID(t, job, "fuzz")
	if fuzzIndex != 3 || fuzz.Uses != "" {
		t.Error("timed fuzzing must run immediately after recording the toolchain")
	}
	want := strings.Join([]string{
		"go test ./internal/compare -run '^$' -fuzz '^FuzzCanonicalSummary$' -fuzztime 2m -parallel 2",
		"go test ./internal/compare -run '^$' -fuzz '^FuzzEmbeddedSketch$' -fuzztime 2m -parallel 2",
		"go test ./internal/compare -run '^$' -fuzz '^FuzzScanSummary$' -fuzztime 2m -parallel 2",
		"go test ./internal/diagnose -run '^$' -fuzz '^FuzzDiagnose$' -fuzztime 2m -parallel 2",
		"go test ./internal/inspect -run '^$' -fuzz '^FuzzTraceRecord$' -fuzztime 2m -parallel 2",
	}, "\n")
	if got := strings.TrimSpace(fuzz.Run); got != want {
		t.Errorf("fuzz job must retain exactly the five blocking two-minute targets with parallelism 2:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestWorkflowSecuritySummary(t *testing.T) {
	type check struct {
		env, id, label string
		outcomeIndex   int
	}
	workflow := readTestWorkflow(t, "security.yml")
	for _, target := range []struct {
		job, name string
		checks    []check
	}{
		{"scan", "Security check summary", []check{
			{"VULNERABILITY_OUTCOME", "vulnerabilities", "Release-toolchain vulnerability scan", 0},
			{"HISTORY_OUTCOME", "history", "Redacted history scan", 1},
			{"RACE_OUTCOME", "race", "Race tests", 3},
		}},
		{"fuzz", "Fuzz check summary", []check{
			{"FUZZ_OUTCOME", "fuzz", "Malformed-input fuzz tests", 2},
		}},
	} {
		t.Run(target.job, func(t *testing.T) {
			job := testWorkflowJob(t, workflow, target.job)
			var summaries []int
			for i, step := range job.Steps {
				if step.Name == target.name {
					summaries = append(summaries, i)
				}
			}
			if len(summaries) != 1 || summaries[0] != len(job.Steps)-1 {
				t.Fatalf("want exactly one final %s step, got indices %v", target.name, summaries)
			}
			summary := job.Steps[summaries[0]]
			if summary.If != "always()" {
				t.Errorf("summary if = %q, want always()", summary.If)
			}
			assertWorkflowBlocking(t, "summary", summary.ContinueOnError)
			if summary.Uses != "" || strings.TrimSpace(summary.Run) == "" {
				t.Fatal("summary must execute its report script")
			}
			if got := summary.Env["RUN_URL"]; got != "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}" {
				t.Errorf("summary RUN_URL = %q", got)
			}
			if len(summary.Env) != len(target.checks)+1 {
				t.Error("summary must report only its own job's outcomes and run URL")
			}
			for _, check := range target.checks {
				testWorkflowStepByID(t, job, check.id)
				want := "${{ steps." + check.id + ".outcome }}"
				if got := summary.Env[check.env]; got != want {
					t.Errorf("summary env %s = %q, want %q", check.env, got, want)
				}
			}

			for _, scenario := range []struct {
				name     string
				outcomes [4]string
			}{
				{"success", [4]string{"success", "success", "success", "success"}},
				{"vulnerability failure", [4]string{"failure", "skipped", "success", "skipped"}},
				{"history failure", [4]string{"success", "failure", "success", "skipped"}},
				{"fuzz failure", [4]string{"success", "success", "failure", "success"}},
				{"race failure", [4]string{"success", "success", "success", "failure"}},
				{"cancelled", [4]string{"cancelled", "skipped", "cancelled", "skipped"}},
				{"skipped", [4]string{"skipped", "skipped", "skipped", "skipped"}},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					root := t.TempDir()
					path := filepath.Join(root, "step summary.md")
					const existing = "Earlier job notes\n"
					writeTestFile(t, path, existing, 0600)
					const runURL = "https://github.invalid/fixture/repository/actions/runs/123"
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, "bash", "-e", "-o", "pipefail", "-c", summary.Run)
					cmd.Dir = root
					cmd.Env = []string{
						"PATH=" + os.Getenv("PATH"), "GITHUB_STEP_SUMMARY=" + path, "RUN_URL=" + runURL,
					}
					for _, check := range target.checks {
						cmd.Env = append(cmd.Env, check.env+"="+scenario.outcomes[check.outcomeIndex])
					}
					if output, err := cmd.CombinedOutput(); err != nil {
						t.Fatalf("summary script: %v\n%s", err, output)
					}
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					report := string(data)
					if !strings.HasPrefix(report, existing) {
						t.Error("summary must append without discarding earlier notes")
					}
					wantRows := []string{
						"Run: " + runURL,
						"Failed or skipped checks are not passes.",
					}
					for _, check := range target.checks {
						wantRows = append(wantRows, "| "+check.label+" | "+scenario.outcomes[check.outcomeIndex]+" |")
					}
					for _, want := range wantRows {
						if !strings.Contains(report, want) {
							t.Errorf("summary lacks %q:\n%s", want, report)
						}
					}
				})
			}
		})
	}
}

func TestWorkflowHistoryExceptions(t *testing.T) {
	expected := map[string]int{
		"b214e8466291879aa7862f7d26875778ee20ea95:.github/workflows/ci.yml:generic-api-key:61":                    0,
		"b214e8466291879aa7862f7d26875778ee20ea95:internal/diagnose/shadow_test.go:generic-api-key:43":            0,
		"b214e8466291879aa7862f7d26875778ee20ea95:internal/cli/diagnose_test.go:generic-api-key:85":               0,
		"b214e8466291879aa7862f7d26875778ee20ea95:internal/diagnose/diagnose_test.go:generic-api-key:65":          0,
		"646b6b4cfd03ea044557aece5bfc3c4621762511:evidence/model-mix/manifest.json:generic-api-key:9":             0,
		"a7dcb195cc59d310093604d55adbc6c355fb601a:internal/cli/refactor_golden_test.go:generic-api-key:15":        0,
		"1e310e9b446871cda5a3275bc0680f369e51dd1a:internal/cli/refactor_golden_test.go:generic-api-key:15":        0,
		"f64d4bec6f7bba032c908839a33cae7b0125010a:internal/clauderecords/period_test.go:generic-api-key:69":       0,
		"f64d4bec6f7bba032c908839a33cae7b0125010a:internal/clauderecords/investigate_test.go:generic-api-key:241": 0,
		"d47787238fa657f585d461260581f503eaa01d10:internal/cli/spend_text_test.go:generic-api-key:160":            0,
	}
	file, err := os.Open("../.gitleaksignore")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	entries, lineNumber := 0, 0
	for scanner.Scan() {
		lineNumber++
		entry := strings.TrimSpace(scanner.Text())
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		entries++
		parts := strings.Split(entry, ":")
		if len(parts) != 4 || len(parts[0]) != 40 || strings.ContainsAny(entry, "*?[]") {
			t.Errorf("ignore line %d must be a commit:file:rule:line fingerprint without wildcards", lineNumber)
			continue
		}
		if _, ok := expected[entry]; !ok {
			t.Errorf("ignore line %d is not an approved exact fingerprint", lineNumber)
			continue
		}
		expected[entry]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if entries != len(expected) {
		t.Errorf("got %d history exceptions, want %d", entries, len(expected))
	}
	for fingerprint, count := range expected {
		if count != 1 {
			t.Errorf("history exception %s appears %d times, want exactly once", fingerprint, count)
		}
	}
}

func readTestWorkflow(t *testing.T, name string) testedWorkflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	var workflow testedWorkflow
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return workflow
}

func testWorkflowJob(t *testing.T, workflow testedWorkflow, id string) testedWorkflowJob {
	t.Helper()
	job, ok := workflow.Jobs[id]
	if !ok {
		t.Fatalf("missing workflow job %q", id)
	}
	return job
}

func testWorkflowGoSetups(t *testing.T, job testedWorkflowJob) []testedWorkflowStep {
	t.Helper()
	var setups []testedWorkflowStep
	for _, step := range job.Steps {
		if strings.HasPrefix(step.Uses, "actions/setup-go@") {
			setups = append(setups, step)
		}
	}
	if len(setups) == 0 {
		t.Fatal("job lacks actions/setup-go")
	}
	return setups
}

func testWorkflowStepByID(t *testing.T, job testedWorkflowJob, id string) (testedWorkflowStep, int) {
	t.Helper()
	index := -1
	for i, step := range job.Steps {
		if step.ID == id {
			if index >= 0 {
				t.Fatalf("duplicate step id %q", id)
			}
			index = i
		}
	}
	if index < 0 {
		t.Fatalf("missing step id %q", id)
	}
	return job.Steps[index], index
}

func assertWorkflowBlocking(t *testing.T, name string, continueOnError any) {
	t.Helper()
	if continueOnError != nil {
		t.Errorf("%s must omit continue-on-error, got %v", name, continueOnError)
	}
}
