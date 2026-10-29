// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/spend"
)

func TestRoutingShowLabelsPrivacyBoundary(t *testing.T) {
	t.Setenv("ROUTING_CLI_SECRET", "synthetic-routing-comparison-secret-32-bytes")
	path := routingCLIInput(t, true)
	for _, format := range []string{"text", "json"} {
		var out, errs bytes.Buffer
		args := append(routingCLIArgs(path), "--routing-audit", "--routing-by", "route-agent", "--show-labels", "--acknowledge-sensitive-labels", "--show-hashes", "--format", format)
		if code := Run(args, &out, &errs); code != 0 || errs.Len() != 0 {
			t.Fatal("opted-in report failed", code, errs.String())
		}
		for _, label := range []string{"ROUTING_PRIVATE_ROUTE", "ROUTING_PRIVATE_AGENT"} {
			if !strings.Contains(out.String(), label) {
				t.Fatal("requested label missing")
			}
		}
		for _, secret := range []string{"ROUTING_PRIVATE_KEY", "ROUTING_PRIVATE_USER", "ROUTING_PRIVATE_SESSION", "ROUTING_PRIVATE_MODEL", "ROUTING_PRIVATE_DEPLOYMENT", "ROUTING_PRIVATE_RECOVERY_GROUP", path} {
			if strings.Contains(out.String()+errs.String(), secret) {
				t.Fatal("show-labels exposed an unrelated private value")
			}
		}
		if format == "json" {
			var r spend.Report
			if json.Unmarshal(out.Bytes(), &r) != nil || !r.RoutingAudit.ConfigurationLabelsIncluded {
				t.Fatal("JSON did not identify configuration labels")
			}
		} else if !strings.Contains(out.String(), "Configuration labels included (--show-labels); review before sharing.") {
			t.Fatal("text did not identify configuration labels")
		}
	}
}

func TestRoutingLabelTerminalEscaping(t *testing.T) {
	id := spend.RoutingIdentity{Item: "route-1", State: "known", Hash: "abc", Label: "name\n\x1b[31m\u202eend"}
	got := routingLabel(id)
	if got != `route-1 "name\n\x1b[31m\u202eend" (abc)` || strings.ContainsAny(got, "\n\x1b\u202e") {
		t.Fatal("configuration label changed terminal structure")
	}
}

func TestShowLabelsFlagIsolation(t *testing.T) {
	for _, args := range [][]string{
		{"investigate", "--litellm-spend", "PRIVATE_PATH", "--show-labels"},
		{"investigate", "--litellm-spend", "PRIVATE_PATH", "--routing-audit", "--show-labels", "--period", "2026-10-01"},
		{"investigate", "--before", "PRIVATE_PATH", "--after", "PRIVATE_PATH", "--expected", "p", "--show-labels"},
		{"compare", "--litellm-spend", "PRIVATE_PATH", "--routing-audit", "--show-labels"},
		{"what-if", "--litellm-spend", "PRIVATE_PATH", "--show-labels"},
	} {
		var out, errs bytes.Buffer
		if code := Run(args, &out, &errs); code != 2 || out.Len() != 0 || strings.Contains(errs.String(), "PRIVATE_") {
			t.Fatal("show-labels crossed command boundaries or leaked input")
		}
	}
}
