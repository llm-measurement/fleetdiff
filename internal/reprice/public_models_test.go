// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"strings"
	"testing"
)

func TestPublicModelIDExactBundledIDs(t *testing.T) {
	prices, _, err := Bundled()
	if err != nil || validatePrices(prices) != nil {
		t.Fatal("bundled price catalog is invalid", err)
	}
	for _, model := range prices.Models {
		t.Run(model.ID, func(t *testing.T) {
			t.Parallel()
			if PublicModelID(model.ID, "") != model.ID {
				t.Fatal("exact bundled ID was not public")
			}
			for _, private := range []string{
				"", "PRIVATE_UNKNOWN_MODEL", "PRIVATE_" + model.ID,
				" " + model.ID, model.ID + " ", "\t" + model.ID + "\r\n",
				"\u00a0" + model.ID + "\u2003", "\u200b" + model.ID,
				strings.ToUpper(model.ID), "provider/" + model.ID,
				model.ID + "-2026-10-17", model.ID + "\x00", model.ID + "\xff",
				strings.ReplaceAll(model.ID, "-", "\u2011"),
			} {
				if PublicModelID(private, "") != "" {
					t.Fatal("non-exact or unknown value was made public")
				}
			}
		})
	}
}

func TestPublicModelIDIgnoresPriceOverridesAndCallerMutation(t *testing.T) {
	prices, _, err := Bundled()
	if err != nil || len(prices.Models) < 2 {
		t.Fatal("need at least two bundled models", err)
	}
	first, omitted := prices.Models[0].ID, prices.Models[1].ID
	override := prices
	override.Models = []Price{prices.Models[0]}
	override.Models[0].Input = "999"
	result, err := OverridePrices(prices, override)
	if err != nil {
		t.Fatal(err)
	}
	prices.Models[0].ID = "PRIVATE_MUTATED_BUNDLE"
	override.Models[0].ID = "PRIVATE_OVERRIDE"
	result.Models[0].ID = "PRIVATE_MUTATED_RESULT"
	for _, public := range []string{first, omitted} {
		if PublicModelID(public, "") != public {
			t.Fatal("override or caller mutation changed catalog membership")
		}
	}
	for _, private := range []string{prices.Models[0].ID, override.Models[0].ID, result.Models[0].ID} {
		if PublicModelID(private, "") != "" {
			t.Fatal("caller-controlled price ID became public")
		}
	}
}

func TestPublicModelIDStructuredProviderMatch(t *testing.T) {
	prices, _, err := Bundled()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range prices.Models {
		if got := PublicModelID(m.Provider+"/"+m.ID, m.Provider); got != m.ID {
			t.Fatalf("structured exact match failed for %s", m.ID)
		}
		for _, pair := range [][2]string{
			{m.Provider + "/" + m.ID, ""}, {m.Provider + "/" + m.ID, "wrong"},
			{m.Provider + "/" + m.ID, m.Provider + " "},
			{"wrong/" + m.ID, "wrong"}, {"/" + m.ID, ""},
			{m.Provider + "/" + m.ID + " ", m.Provider},
			{m.Provider + "/ " + m.ID, m.Provider},
			{" " + m.Provider + "/" + m.ID, m.Provider},
			{m.Provider + "/" + m.Provider + "/" + m.ID, m.Provider},
			{m.Provider + "/PRIVATE_MODEL", m.Provider},
			{m.Provider + "/" + strings.ToUpper(m.ID), m.Provider},
		} {
			if PublicModelID(pair[0], pair[1]) != "" {
				t.Fatal("non-exact identity resolved")
			}
		}
	}
}
