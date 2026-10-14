// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package accounting

import (
	"math/big"
	"regexp"
)

var decimalPattern = regexp.MustCompile(`^[0-9]{1,19}(\.[0-9]{1,20})?([eE][+-]?[0-9]{1,3})?$`)
var maxCost = new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))

// Cost preserves the exact bounded source decimal, not provider billing truth.
func Cost(s string) (*big.Rat, bool) {
	if len(s) > 48 || !decimalPattern.MatchString(s) {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || r.Sign() < 0 || r.Cmp(maxCost) > 0 {
		return nil, false
	}
	if digits, exact := r.FloatPrec(); !exact || digits > 344 {
		return nil, false
	}
	return r, true
}

func CostText(n *big.Rat) string {
	digits, _ := n.FloatPrec()
	return n.FloatString(digits)
}
