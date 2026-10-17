// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestFailureTokenTextOnlyWhenApplicable(t *testing.T) {
	r := spend.Report{Before: spend.Window{Tokens: 36}, After: spend.Window{Tokens: 36}}
	var old, now bytes.Buffer
	renderSpend(&old, r)
	r.After.FailureTokens = &spend.FailureTokens{Records: 2, Input: 33, Output: 3, Total: 36, Finality: "unknown"}
	renderSpend(&now, r)
	line := "  Tokens recorded on failure rows: 0 -> 36 (input 0 -> 33; output 0 -> 3); gateway-recorded, finality unknown.\n"
	if !strings.Contains(now.String(), line) {
		t.Fatal(now.String())
	}
	if strings.Replace(now.String(), line, "", 1) != old.String() {
		t.Fatal("existing report lines changed")
	}
}
