// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"strings"
	"sync"
)

var bundledPublicModels = sync.OnceValue(func() map[string]string {
	prices, _, err := Bundled()
	if err != nil || validatePrices(prices) != nil {
		return nil
	}
	models := make(map[string]string, len(prices.Models))
	for _, model := range prices.Models {
		models[model.ID] = model.Provider
	}
	return models
})

// PublicModelID resolves an exact catalog ID or provider/ID with both provider
// fields matching the catalog. It never trims, normalizes, or resolves aliases.
// Caller price overrides and identity canonicalization cannot add public IDs.
func PublicModelID(model, provider string) string {
	models := bundledPublicModels()
	if _, ok := models[model]; ok {
		return model
	}
	prefix, name, qualified := strings.Cut(model, "/")
	if qualified && prefix == provider && prefix != "" && models[name] == prefix {
		return name
	}
	return ""
}
