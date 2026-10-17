// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func whatIfFailureInput(t *testing.T, change func(int, map[string]string)) string {
	t.Helper()
	data, err := os.ReadFile("../../examples/what-if/spend.csv")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(bytes.NewReader(data)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range rows[1:] {
		values := map[string]string{}
		for j, key := range rows[0] {
			values[key] = row[j]
		}
		values["status"] = "failure"
		if change != nil {
			change(i, values)
		}
		for j, key := range rows[0] {
			row[j] = values[key]
		}
	}
	var body bytes.Buffer
	w := csv.NewWriter(&body)
	if err := w.WriteAll(rows); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "failure-usage.csv")
	if err := os.WriteFile(path, body.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func whatIfFailurePrices(t *testing.T, change func(*reprice.Prices)) string {
	t.Helper()
	p, _, err := reprice.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	change(&p)
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prices.json")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withoutPricedFailures(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var values map[string]json.RawMessage
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatal(err)
	}
	delete(values, "priced_failures")
	// Canonicalize whitespace without converting exact JSON numbers to floats.
	for key, value := range values {
		var b bytes.Buffer
		if err := json.Compact(&b, value); err != nil {
			t.Fatal(err)
		}
		values[key] = b.Bytes()
	}
	return values
}

func TestWhatIfFailureJSONAdditiveCompatibility(t *testing.T) {
	args := whatIfArgs()
	args[2] = whatIfFailureInput(t, nil)
	args = append(args, "--format", "json")
	var out, errs bytes.Buffer
	if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	var got reprice.Report
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	v := got.PricedFailures
	if v == nil || v.Records != 3 || v.Input.String() != "30000" || v.Output.String() != "12000" || v.Total.String() != "42000" || v.CacheRead.String() != "14000" || v.CacheWrite.String() != "2000" {
		t.Fatalf("priced failure records or token components: %+v", v)
	}
	if !reflect.DeepEqual(v.Before, got.Before) || !reflect.DeepEqual(v.Level1, got.Level1) || !reflect.DeepEqual(v.Level2, got.Level2) || !reflect.DeepEqual(v.Envelope, got.Envelope) {
		t.Fatal("all-failure contribution differs from unchanged full-scenario costs")
	}
	if v.Finality != "unknown" || v.UsageBasis != "gateway-recorded" || strings.Contains(out.String(), "SENTINEL_") {
		t.Fatal("failure provenance missing or identity exposed")
	}
	golden, err := os.ReadFile("../../examples/what-if/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	want := withoutPricedFailures(t, golden)
	var source reprice.Source
	if err := json.Unmarshal(want["source"], &source); err != nil {
		t.Fatal(err)
	}
	// This is the pre-existing status-only difference from the success golden.
	source.Gaps = []string{"Complete recorded usage on failed requests: 3; billing finality is unknown."}
	want["source"], err = json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withoutPricedFailures(t, out.Bytes()), want) {
		t.Fatal("removing the additive field does not reproduce legacy failure JSON")
	}
}

func TestWhatIfFailureDisclosureEligibility(t *testing.T) {
	for _, tc := range []struct {
		name          string
		change        func(int, map[string]string)
		extra         func(*testing.T) []string
		priced        bool
		wantError     bool
		prepriceCount bool
	}{
		{name: "priced", priced: true, prepriceCount: true},
		{name: "unknown-model", change: func(_ int, r map[string]string) { r["model"] = "SENTINEL_UNKNOWN_MODEL" }, prepriceCount: true},
		{name: "missing-cache", change: func(_ int, r map[string]string) { r["cache_read_input_tokens"] = "" }},
		{name: "missing-scope", change: func(_ int, r map[string]string) { r["api_key"] = "" }, extra: func(*testing.T) []string {
			return []string{"--scope-key", "SENTINEL_SELECTED_KEY"}
		}, prepriceCount: true},
		{name: "source-price-tier", extra: func(t *testing.T) []string {
			return []string{"--prices", whatIfFailurePrices(t, func(p *reprice.Prices) { p.Models[0].InputTierMax = 9999 })}
		}, prepriceCount: true},
		{name: "target-tier-rejection", extra: func(t *testing.T) []string {
			return []string{"--prices", whatIfFailurePrices(t, func(p *reprice.Prices) { p.Models[1].InputTierMax = 1 })}
		}, wantError: true},
		{name: "zero-origin", change: func(_ int, r map[string]string) {
			for _, k := range []string{"prompt_tokens", "completion_tokens", "total_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "reasoning_output_tokens"} {
				r[k] = "0"
			}
		}},
		{name: "nonfailure", change: func(_ int, r map[string]string) { r["status"] = "success" }},
		{name: "unselected-priced-baseline", extra: func(*testing.T) []string {
			return []string{"--scope-key", "SENTINEL_UNSELECTED_KEY"}
		}, priced: true, prepriceCount: true},
		{name: "zero-price-nonzero-tokens", extra: func(t *testing.T) []string {
			return []string{"--prices", whatIfFailurePrices(t, func(p *reprice.Prices) {
				for i := range p.Models {
					p.Models[i].Input, p.Models[i].Output, p.Models[i].CacheRead, p.Models[i].CacheWrite = "0", "0", "0", "0"
				}
			})}
		}, priced: true, prepriceCount: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"what-if", "--litellm-spend", whatIfFailureInput(t, tc.change), "--period", "2026-10-07", "--from", "claude-sonnet-5-5", "--to", "gpt-6.1-sol", "--fraction", "0.5"}
			if tc.extra != nil {
				args = append(args, tc.extra(t)...)
			}
			for _, format := range []string{"json", "text"} {
				var out, errs bytes.Buffer
				formatArgs := append(append([]string(nil), args...), "--format", format)
				code := Run(formatArgs, &out, &errs)
				if strings.Contains(out.String()+errs.String(), "SENTINEL_") {
					t.Fatal("failure report exposed a raw identity")
				}
				if tc.wantError {
					if code != 1 || out.Len() != 0 || !strings.Contains(errs.String(), "target's priced input tier") {
						t.Fatalf("target tier: exit %d, %s, %s", code, &out, &errs)
					}
					continue
				}
				if code != 0 || errs.Len() != 0 {
					t.Fatalf("exit %d: %s", code, &errs)
				}
				if format == "json" {
					var r reprice.Report
					if err := json.Unmarshal(out.Bytes(), &r); err != nil {
						t.Fatal(err)
					}
					if (r.PricedFailures != nil) != tc.priced {
						t.Fatalf("unexpected priced subset: %+v", r.PricedFailures)
					}
					if tc.prepriceCount && !reflect.DeepEqual(r.Source.Gaps, []string{"Complete recorded usage on failed requests: 3; billing finality is unknown."}) {
						t.Fatalf("existing source gaps changed: %v", r.Source.Gaps)
					}
					if tc.name == "unselected-priced-baseline" && (*r.PricedFailures.Before != *r.PricedFailures.Level1 || *r.PricedFailures.Before != *r.Before || len(r.PricedFailures.Level2) != 0) {
						t.Fatal("unselected failure did not contribute unchanged baseline cost")
					}
					// Optional offline differential against the saved pre-edit binary.
					if binary := os.Getenv("FLEETDIFF_WHATIF_BASELINE_BINARY"); binary != "" {
						old, err := exec.Command(binary, formatArgs...).Output()
						if err != nil {
							t.Fatal(err)
						}
						if !reflect.DeepEqual(withoutPricedFailures(t, out.Bytes()), withoutPricedFailures(t, old)) {
							t.Fatal("existing JSON differs from the pre-edit binary")
						}
					}
				} else {
					if strings.Contains(out.String(), "Priced failure subset:") != tc.priced {
						t.Fatalf("failure text did not follow priced-token eligibility: %s", &out)
					}
					if tc.priced && (!strings.Contains(out.String(), "gateway-recorded tokens, provider/billing finality unknown") || !strings.Contains(out.String(), "Cost contribution already included above:")) {
						t.Fatal("text did not distinguish recorded contributions from final billing")
					}
					if tc.priced && !strings.Contains(out.String(), "Tokens: input 30,000, output 12,000, total 42,000; input includes cache read 14,000 and cache write 2,000.") {
						t.Fatal("token components did not use the existing number formatter")
					}
					if tc.prepriceCount && !strings.Contains(out.String(), "pre-pricing source count, not a priced-failure count") {
						t.Fatal("source warning was mistaken for priced coverage")
					}
				}
			}
		})
	}
}

func TestWhatIfFailureTextSuppressesZeroPricedTokens(t *testing.T) {
	var out, errs bytes.Buffer
	if code := Run(append(whatIfArgs(), "--format", "json"), &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, &errs)
	}
	var r reprice.Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	r.PricedFailures = &reprice.PricedFailures{Records: 1, Total: new(big.Int)}
	out.Reset()
	renderWhatIf(&out, r)
	if strings.Contains(out.String(), "Priced failure subset:") {
		t.Fatal("zero-token failure disclosure rendered")
	}
}
