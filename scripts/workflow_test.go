// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package scripts

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestCIWriterTimeout(t *testing.T) {
	data, err := os.ReadFile("../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Timeout int `yaml:"timeout-minutes"`
			Steps   []struct {
				Name    string `yaml:"name"`
				If      string `yaml:"if"`
				Timeout int    `yaml:"timeout-minutes"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	job := workflow.Jobs["test"]
	found := 0
	for _, step := range job.Steps {
		if step.Name != "Pinned LiteLLM writer to read-only SQL export" {
			continue
		}
		found++
		if step.If != "runner.os == 'Linux'" {
			t.Errorf("writer step condition = %q, want Linux only", step.If)
		}
		if step.Timeout != 15 {
			t.Errorf("writer step timeout = %d, want 15 minutes", step.Timeout)
		}
		if job.Timeout < step.Timeout+5 {
			t.Errorf("job timeout = %d, want at least five minutes beyond the writer step", job.Timeout)
		}
	}
	if found != 1 {
		t.Errorf("found %d writer steps, want exactly one", found)
	}
}
