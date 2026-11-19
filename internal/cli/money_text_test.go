// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestUSDTextExactHalfEven(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"1.005", "$1.00"},
		{"1.015", "$1.02"},
		{"2.675", "$2.68"},
		{"-1.005", "-$1.00"},
		{"1000.005", "$1,000.00"},
		{"0", "$0.00"},
		{"0.0000001", "<$0.01"},
		{"-0.0000001", "-<$0.01"},
	} {
		if got := usdText(tc.input); got != tc.want {
			t.Errorf("usdText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestUSDComparedKeepsTinyChangeVisible(t *testing.T) {
	for _, tc := range []struct {
		inputs []string
		want   []string
	}{
		{[]string{"0.0017", "0.0016"}, []string{"$0.0017", "$0.0016"}},
		{[]string{"0.00005", "0.00005"}, []string{"<$0.01", "<$0.01"}},
		{[]string{"0.000004", "0.000005", "1.015"}, []string{"$0.000004", "$0.000005", "$1.02"}},
	} {
		if got := usdCompared(tc.inputs...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("usdCompared(%q) = %q, want %q", tc.inputs, got, tc.want)
		}
	}
}

func TestSessionDistributionShowsTinySpread(t *testing.T) {
	p50, p95, max := "0.0016", "0.0017", "0.0017"
	var out bytes.Buffer
	renderSessionDistribution(&out, "Recorded USD", spend.Distribution{N: 2, P50: &p50, P95: &p95, Max: &max}, true)
	if want := "Recorded USD: p50 $0.0016; p95 $0.0017; max $0.0017 (n=2, exact nearest-rank).\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}
