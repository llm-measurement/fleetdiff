// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package diagnose

import (
	"encoding/json"
	"strings"
	"testing"
)

func memoryLimiterInput(t *testing.T, settings string) string {
	t.Helper()
	input := strings.ReplaceAll(fixture(t, "safe"), "\r\n", "\n")
	const processors = "processors:\n  batch: {timeout: 1s}"
	if !strings.Contains(input, processors) {
		t.Fatal("safe fixture no longer has expected processors")
	}
	input = strings.Replace(input, processors, processors+"\n  memory_limiter:\n    check_interval: 1s\n"+settings, 1)
	const pipeline = "processors: [batch]"
	if !strings.Contains(input, pipeline) {
		t.Fatal("safe fixture no longer has expected trace processors")
	}
	return strings.Replace(input, pipeline, "processors: [batch, memory_limiter]", 1)
}

func TestMemoryLimiterPercentageSettings(t *testing.T) {
	for _, tc := range []struct {
		name          string
		settings      string
		wantStatus    string
		wantFindingID string
		wantPath      string
		lineField     string
		redact        string
	}{
		{name: "percentage only", settings: "    limit_percentage: 80\n    spike_limit_percentage: 20\n", wantStatus: "supported_safe"},
		{name: "default spike percentage", settings: "    limit_percentage: 80\n", wantStatus: "supported_safe"},
		{name: "100 percent limit", settings: "    limit_percentage: 100\n    spike_limit_percentage: 99\n", wantStatus: "supported_safe"},
		{name: "zero percentage requires a fixed MiB limit", settings: "    limit_percentage: 0\n", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter.limit_percentage", lineField: "limit_percentage", redact: ""},
		{name: "one limit is required", settings: "", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter", lineField: "check_interval"},
		{name: "percentage limit above range", settings: "    limit_percentage: 101\n    spike_limit_percentage: 20\n", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter.limit_percentage", lineField: "limit_percentage", redact: "101"},
		{name: "percentage limit below range", settings: "    limit_percentage: -1\n", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter.limit_percentage", lineField: "limit_percentage", redact: "-1"},
		{name: "spike percentage above range", settings: "    limit_percentage: 80\n    spike_limit_percentage: 101\n", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter.spike_limit_percentage", lineField: "spike_limit_percentage", redact: "101"},
		{name: "spike at limit", settings: "    limit_percentage: 80\n    spike_limit_percentage: 80\n", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter.spike_limit_percentage", lineField: "spike_limit_percentage", redact: "80"},
		{name: "spike above limit", settings: "    limit_percentage: 80\n    spike_limit_percentage: 90\n", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter.spike_limit_percentage", lineField: "spike_limit_percentage", redact: "90"},
		{name: "fixed MiB pair takes precedence over percentages", settings: "    limit_mib: 1024\n    spike_limit_mib: 128\n    limit_percentage: 80\n    spike_limit_percentage: 20\n", wantStatus: "supported_safe"},
		{name: "percentage validation still applies with fixed MiB", settings: "    limit_mib: 1024\n    spike_limit_mib: 128\n    limit_percentage: 101\n", wantStatus: "indeterminate", wantFindingID: "unsupported_mapping", wantPath: "processors.memory_limiter.limit_percentage", lineField: "limit_percentage", redact: "101"},
		{name: "unknown setting remains unsupported", settings: "    limit_percentage: 80\n    mystery_setting: PRIVATE_SENTINEL_8c4a\n", wantStatus: "indeterminate", wantFindingID: "unsupported_field", wantPath: "processors.memory_limiter.[key-3]", lineField: "mystery_setting", redact: "PRIVATE_SENTINEL_8c4a"},
		{name: "existing MiB-only form", settings: "    limit_mib: 1024\n    spike_limit_mib: 128\n", wantStatus: "supported_safe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := memoryLimiterInput(t, tc.settings)
			report := check(t, input)
			if report.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q; findings: %+v", report.Status, tc.wantStatus, report.Findings)
			}
			if tc.wantFindingID != "" {
				wantLine := strings.Count(input[:strings.Index(input, tc.lineField)], "\n") + 1
				found := false
				for _, finding := range report.Findings {
					if finding.ID == tc.wantFindingID && finding.Path == tc.wantPath {
						found = true
						if finding.Line != wantLine {
							t.Errorf("finding line = %d, want %d", finding.Line, wantLine)
						}
					}
				}
				if !found {
					t.Fatalf("missing %s at %s; findings: %+v", tc.wantFindingID, tc.wantPath, report.Findings)
				}
				encoded, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				if tc.redact != "" && strings.Contains(string(encoded), tc.redact) {
					t.Fatalf("report leaked the rejected value %q", tc.redact)
				}
			}
		})
	}
}
