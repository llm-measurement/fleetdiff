// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRoutingPublicModelLabelsAreExact(t *testing.T) {
	for _, value := range []string{"gpt-6.1-sol", "gpt-6.1-sol-private", "openai/gpt-6.1-sol", " gpt-6.1-sol", "gpt-6.1-sol\n", "GPT-6.1-SOL"} {
		rows := []map[string]string{routingSample("a", "2026-09-01", 90, 10), routingSample("b", "2026-09-02", 90, 10)}
		for _, row := range rows {
			row["model"] = value
		}
		for _, hashes := range []bool{false, true} {
			o := routingOptions(t)
			o.ShowHashes = hashes
			r := routingReport(t, rows, o)
			model := r.RoutingAudit.Cohorts[0].Models[0]
			want := "selected-model-1"
			if value == "gpt-6.1-sol" {
				want = value
			}
			if model.Item != want || model.Label != "" || (model.Hash != "") != hashes {
				t.Fatal("public model label was inferred or hash behavior changed")
			}
		}
	}
}

func TestRoutingPublicLabelCanonicalCollisionStaysPrivate(t *testing.T) {
	for _, values := range [][]string{{"gpt-6.1-sol", " gpt-6.1-sol ", "gpt-6.1-sol"}, {" gpt-6.1-sol ", "gpt-6.1-sol", "gpt-6.1-sol"}} {
		var rows []map[string]string
		for i, value := range values {
			r := routingSample(string(rune('a'+i)), "2026-09-01", 90, 10)
			r["model"] = value
			rows = append(rows, r)
		}
		r := routingReport(t, rows, routingOptions(t))
		models := r.RoutingAudit.Cohorts[0].Models
		if len(models) != 1 || models[0].Item != "selected-model-1" || models[0].Before.Requests != 3 {
			t.Fatal("canonical grouping changed or ambiguous public spelling escaped")
		}
	}
}

func TestRoutingLabelsOptInAndAccountingUnchanged(t *testing.T) {
	rows := []map[string]string{routingSample("a", "2026-09-01", 90, 10), routingSample("b", "2026-09-02", 190, 10)}
	rows[0]["model"], rows[1]["model"] = "gpt-6.1-sol", "gpt-6.1-sol"
	o := routingOptions(t)
	o.RoutingBy, o.ShowHashes = "route-agent", true
	baseline := routingReport(t, rows, o)
	assertRoutingPrivate(t, baseline)
	o.ShowLabels = true
	o.AcknowledgeSensitiveLabels = true
	shown := routingReport(t, rows, o)
	c := shown.RoutingAudit.Cohorts[0]
	if !shown.RoutingAudit.ConfigurationLabelsIncluded || c.Route.Label != "PRIVATE_ROUTE" || c.Agent == nil || c.Agent.Label != "PRIVATE_AGENT" {
		t.Fatal("opted-in configuration labels missing")
	}
	encoded, _ := json.Marshal(shown)
	for _, secret := range []string{"PRIVATE_KEY", "PRIVATE_DEPLOYMENT", "PRIVATE_RECOVERY_GROUP", "shared-session"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("show-labels exposed an unrelated identity")
		}
	}
	shown.RoutingAudit.ConfigurationLabelsIncluded = false
	shown.RoutingAudit.Cohorts[0].Route.Label = ""
	shown.RoutingAudit.Cohorts[0].Agent.Label = ""
	if !reflect.DeepEqual(shown, baseline) {
		t.Fatal("show-labels changed more than additive display fields")
	}
}

func TestRoutingUnknownAndAmbiguousLabelsRemainAliased(t *testing.T) {
	o := routingOptions(t)
	o.ShowLabels, o.RoutingBy = true, "route-agent"
	o.AcknowledgeSensitiveLabels = true
	rows := []map[string]string{routingSample("a", "2026-09-01", 90, 10), routingSample("b", "2026-09-02", 90, 10)}
	rows[0]["model"], rows[1]["model"] = "gpt-6.1-sol", "gpt-6.1-sol"
	delete(rows[0], "model_id")
	delete(rows[1], "model_id")
	rows[0]["original_model_group"], rows[1]["original_model_group"] = "route", " route "
	delete(rows[0], "agent_id")
	delete(rows[1], "agent_id")
	r := routingReport(t, rows, o)
	c := r.RoutingAudit.Cohorts[0]
	if c.Route.Label != "" || c.Agent.Label != "" || c.Agent.State != "unknown" || c.Models[0].Item != "selected-model-unknown" {
		t.Fatal("unknown identity or ambiguous raw spelling was displayed")
	}
	o.RoutingAudit = false
	if _, err := Investigate(writeRows(t, rows), o); err == nil {
		t.Fatal("show-labels accepted without routing audit")
	}
}
