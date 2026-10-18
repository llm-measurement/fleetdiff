// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func checkStructuredModelPricing(t *testing.T, run func([]string) []byte) {
	t.Helper()
	for _, scoped := range []bool{false, true} {
		var reference []byte
		for _, tc := range []struct {
			model, provider string
			priced          bool
		}{
			{"gpt-6.1-sol", "openai", true},
			{"openai/gpt-6.1-sol", "openai", true},
			{"openai/gpt-6.1-sol", "", false},
			{"openai/gpt-6.1-sol", "anthropic", false},
			{"anthropic/gpt-6.1-sol", "anthropic", false},
			{"openai/private-model-sentinel", "openai", false},
		} {
			row := whatIfRoutingRow("synthetic-structured")
			row["model"], row["custom_llm_provider"] = tc.model, tc.provider
			path := whatIfRoutingFile(t, []map[string]any{row})
			args := []string{"what-if", "--litellm-spend", path, "--period", "2020-01-02",
				"--from", "gpt-6.1-sol", "--to", "gpt-6-luna", "--fraction", "0.5", "--format", "json"}
			if scoped {
				args = append(args, "--scope-route", "private-route-sentinel")
			}
			out := run(args)
			if bytes.Contains(out, []byte("sentinel")) {
				t.Fatal("raw identity escaped")
			}
			var r reprice.Report
			if err := json.Unmarshal(out, &r); err != nil {
				t.Fatal(err)
			}
			if tc.priced {
				if r.Covered != 1 || r.Moves[0].Covered != 1 || r.Before == nil || r.PricedFailures == nil || r.PricedFailures.Records != 1 {
					t.Fatal("structured model was not fully priced")
				}
				if reference == nil {
					reference = out
				} else if !bytes.Equal(reference, out) {
					t.Fatal("qualification changed arithmetic or coverage")
				}
			} else if r.Covered != 0 || r.Excluded["unknown_model_price"] != 1 || r.Before != nil || r.PricedFailures != nil {
				t.Fatal("unverified model was priced")
			}
		}
	}
}

func TestStructuredModelPricing(t *testing.T) {
	checkStructuredModelPricing(t, func(args []string) []byte {
		var out, errs bytes.Buffer
		if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
			t.Fatal("what-if failed", code, errs.String())
		}
		return out.Bytes()
	})
}

func TestStructuredModelDisplayCollisionTextAndJSON(t *testing.T) {
	for _, names := range [][]string{{"gpt-6.1-sol", "openai/gpt-6.1-sol"}, {"openai/gpt-6.1-sol", "gpt-6.1-sol"}} {
		rows := []map[string]any{whatIfRoutingRow("before"), whatIfRoutingRow("after")}
		for i, row := range rows {
			row["model"], row["custom_llm_provider"] = names[i], "openai"
		}
		rows[1]["startTime"], rows[1]["endTime"] = "2020-01-03T12:00:00Z", "2020-01-03T12:00:01Z"
		path := whatIfRoutingFile(t, rows)
		for _, format := range []string{"text", "json"} {
			var out, errs bytes.Buffer
			args := []string{"investigate", "--litellm-spend", path, "--before-period", "2020-01-02", "--after-period", "2020-01-03", "--routing-audit", "--format", format}
			if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
				t.Fatal("mixed spelling report failed")
			}
			if strings.Contains(out.String(), "sentinel") {
				t.Fatal("mixed spelling exposed private labels")
			}
			if format == "text" {
				for _, label := range []string{"gpt-6.1-sol [model-1]", "gpt-6.1-sol [model-2]", "gpt-6.1-sol [selected-model-1]", "gpt-6.1-sol [selected-model-2]"} {
					if !strings.Contains(out.String(), label) {
						t.Fatal("text lost group discriminator")
					}
				}
			} else {
				var r spend.Report
				if json.Unmarshal(out.Bytes(), &r) != nil || len(r.Models) != 2 || r.Models[0].Item == r.Models[1].Item {
					t.Fatal("JSON model identities collided")
				}
				models := r.RoutingAudit.Cohorts[0].Models
				if len(models) != 2 || models[0].Item == models[1].Item {
					t.Fatal("JSON routing identities collided")
				}
			}
		}
	}
}

func TestReleaseBinaryStructuredModelPricing(t *testing.T) {
	binary := os.Getenv("FLEETDIFF_RELEASE_BINARY")
	if binary == "" {
		t.Skip("set FLEETDIFF_RELEASE_BINARY to test an unpacked release archive")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	checkStructuredModelPricing(t, func(args []string) []byte {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var errs bytes.Buffer
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stderr = &errs
		out, err := cmd.Output()
		if err != nil || errs.Len() != 0 {
			t.Fatal("packaged what-if failed", err, errs.String())
		}
		return out
	})
}
