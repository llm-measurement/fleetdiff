// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package reprice

import (
	"embed"
	"encoding/json"
	"errors"
)

//go:embed data/*.json
var bundled embed.FS

func Bundled() (Prices, Factors, error) {
	var p Prices
	var f Factors
	a, e1 := bundled.ReadFile("data/prices.json")
	b, e2 := bundled.ReadFile("data/factors.json")
	if e1 != nil || e2 != nil || json.Unmarshal(a, &p) != nil || json.Unmarshal(b, &f) != nil {
		return p, f, errors.New("invalid bundled what-if evidence")
	}
	return p, f, nil
}
