// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package spend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/llm-measurement/fleetdiff/internal/reprice"
)

func TestInvestigatePublicModelLabelsAndJSON(t *testing.T) {
	prices, _, err := reprice.Bundled()
	if err != nil || len(prices.Models) < 2 {
		t.Fatal("need two bundled models", err)
	}
	first, second := prices.Models[0].ID, prices.Models[1].ID
	names := []string{first, "PRIVATE_MODEL", second}
	rows := []map[string]string{}
	for i, name := range names {
		before := sample(fmt.Sprintf("before-%d", i), "2026-09-01", "PRIVATE_KEY", 10, 2)
		after := sample(fmt.Sprintf("after-%d", i), "2026-09-02", "PRIVATE_KEY", 100-30*i, 20)
		before["model"], after["model"] = name, name
		rows = append(rows, before, after)
	}
	for _, hashes := range []bool{false, true} {
		t.Run(fmt.Sprintf("hashes-%t", hashes), func(t *testing.T) {
			opts := options(t)
			opts.ShowHashes = hashes
			report, err := Investigate(writeRows(t, rows), opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Models) != 3 || report.Before.Tokens != 36 || report.After.Tokens != 270 {
				t.Fatal("label selection changed models or totals")
			}
			// Text rendering consumes the same Item field serialized into JSON.
			for i, label := range []string{first, "model-2", second} {
				model := report.Models[i]
				if model.Item != label || model.Before.Tokens != 12 || model.After.Tokens != uint64(120-30*i) {
					t.Fatalf("model %d lost its label, alias ordinal, or totals: %+v", i, model)
				}
				wantHash := ""
				if hashes {
					digest, err := identity([]byte(os.Getenv(opts.SecretEnv)), "model", names[i])
					if err != nil {
						t.Fatal(err)
					}
					wantHash = fmt.Sprintf("%x", digest)
				}
				if model.Hash != wantHash {
					t.Fatal("public-label selection changed model hashing")
				}
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Models []Model `json:"models"`
			}
			if err := json.Unmarshal(encoded, &wire); err != nil || !reflect.DeepEqual(wire.Models, report.Models) {
				t.Fatal("JSON does not preserve selected model labels", err)
			}
			if bytes.Contains(encoded, []byte("PRIVATE_")) {
				t.Fatal("private identity escaped through public model labels")
			}
		})
	}
}

func TestInvestigatePublicModelCanonicalCollisionsStayPrivate(t *testing.T) {
	prices, _, err := reprice.Bundled()
	if err != nil || len(prices.Models) == 0 {
		t.Fatal("need a bundled model", err)
	}
	public := prices.Models[0].ID
	for _, variant := range []struct{ name, value string }{
		{"leading-space", " " + public},
		{"trailing-space", public + " "},
		{"tabs-newlines", "\t" + public + "\r\n"},
		{"unicode-space", "\u00a0" + public + "\u2003"},
	} {
		for _, order := range []struct {
			name   string
			values []string
		}{
			{"public-first", []string{public, variant.value, public}},
			{"private-first", []string{variant.value, public, public}},
			{"private-only", []string{variant.value, variant.value, variant.value}},
		} {
			t.Run(variant.name+"/"+order.name, func(t *testing.T) {
				opts := options(t)
				opts.ShowHashes = true
				rows := []map[string]string{}
				for i, value := range order.values {
					row := sample(fmt.Sprint(i), fmt.Sprintf("2026-09-%02d", 1+i%2), "PRIVATE_KEY", 100, 20)
					row["model"] = value
					rows = append(rows, row)
				}
				report, err := Investigate(writeRows(t, rows), opts)
				if err != nil {
					t.Fatal(err)
				}
				if len(report.Models) != 1 || report.Models[0].Item != "model-1" {
					t.Fatal("mixed or private canonical group acquired a public label")
				}
				encoded, err := json.Marshal(report)
				if err != nil || bytes.Contains(encoded, []byte(public)) || bytes.Contains(encoded, []byte("PRIVATE_")) {
					t.Fatal("canonical variant escaped JSON", err)
				}
				for _, row := range rows {
					row["model"] = public
				}
				clean, err := Investigate(writeRows(t, rows), opts)
				if err != nil || len(clean.Models) != 1 || clean.Models[0].Item != public {
					t.Fatal("exact repeated public identity was not labeled", err)
				}
				clean.Models[0].Item = "model-1"
				if !reflect.DeepEqual(report, clean) {
					t.Fatal("label downgrade changed grouping, hashes, totals, or other report fields")
				}
			})
		}
	}
}

func TestInvestigateUnknownModelLabelsStayPrivate(t *testing.T) {
	prices, _, err := reprice.Bundled()
	if err != nil || len(prices.Models) == 0 {
		t.Fatal("need a bundled model", err)
	}
	public := prices.Models[0].ID
	for _, model := range []string{
		"PRIVATE_UNKNOWN_MODEL", "provider/" + public, public + "-2026-10-17",
		strings.ToUpper(public), "\u200b" + public, strings.ReplaceAll(public, "-", "\u2011"),
	} {
		rows := []map[string]string{
			sample("1", "2026-09-01", "PRIVATE_KEY", 100, 20),
			sample("2", "2026-09-02", "PRIVATE_KEY", 200, 20),
		}
		for _, row := range rows {
			row["model"] = model
		}
		report, err := Investigate(writeRows(t, rows), options(t))
		if err != nil || len(report.Models) != 1 || report.Models[0].Item != "model-1" {
			t.Fatal("unknown model was not aliased", err)
		}
		encoded, err := json.Marshal(report)
		privateJSON, _ := json.Marshal(model)
		if err != nil || bytes.Contains(encoded, privateJSON) || bytes.Contains(encoded, []byte("PRIVATE_")) {
			t.Fatal("unknown model escaped JSON", err)
		}
	}
}

func TestInvestigatePublicLabelsRetainModelLimit(t *testing.T) {
	prices, _, err := reprice.Bundled()
	if err != nil || len(prices.Models) == 0 {
		t.Fatal("need a bundled model", err)
	}
	rows := []map[string]string{}
	for i := range MaxModels {
		row := sample(fmt.Sprint(i), "2026-09-02", "PRIVATE_KEY", 100, 20)
		row["model"] = fmt.Sprintf("PRIVATE_MODEL_%d", i)
		if i < len(prices.Models) {
			row["model"] = prices.Models[i].ID
		}
		rows = append(rows, row)
	}
	opts := options(t)
	report, err := Investigate(writeRows(t, rows), opts)
	if err != nil || len(report.Models) != MaxModels {
		t.Fatal("128 model groups should remain supported", err)
	}
	extra := sample("extra", "2026-09-02", "PRIVATE_KEY", 100, 20)
	extra["model"] = "PRIVATE_EXTRA_MODEL"
	if _, err := Investigate(writeRows(t, append(rows, extra)), opts); err == nil || !strings.Contains(err.Error(), "128 models") || strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("model limit was relaxed or leaked input", err)
	}
}
