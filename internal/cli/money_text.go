// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package cli

import (
	"math/big"
	"strings"
)

var oneCent = big.NewRat(1, 100)

func usdText(value string) string {
	n, ok := new(big.Rat).SetString(value)
	if !ok {
		return "unknown"
	}
	if n.Sign() != 0 && new(big.Rat).Abs(n).Cmp(oneCent) < 0 {
		if n.Sign() < 0 {
			return "-<$0.01"
		}
		return "<$0.01"
	}
	return usdFixed(n, 2)
}

func usdFixed(n *big.Rat, digits int) string {
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	amount := new(big.Int).Mul(new(big.Int).Abs(n.Num()), scale)
	whole, remainder := new(big.Int).QuoRem(amount, n.Denom(), new(big.Int))
	cmp := new(big.Int).Lsh(remainder, 1).Cmp(n.Denom())
	if cmp > 0 || cmp == 0 && whole.Bit(0) == 1 {
		whole.Add(whole, big.NewInt(1))
	}
	integer, fraction := new(big.Int).QuoRem(whole, scale, new(big.Int))
	fractionText := fraction.String()
	formatted := "$" + spendNumber(integer.String()) + "." + strings.Repeat("0", digits-len(fractionText)) + fractionText
	if n.Sign() < 0 {
		return "-" + formatted
	}
	return formatted
}

func usdCompared(values ...string) []string {
	result := make([]string, len(values))
	numbers := make([]*big.Rat, len(values))
	for i, value := range values {
		result[i] = usdText(value)
		numbers[i], _ = new(big.Rat).SetString(value)
	}
	needsDetail := false
	for i, a := range numbers {
		if a == nil || a.Sign() == 0 || new(big.Rat).Abs(a).Cmp(oneCent) >= 0 {
			continue
		}
		for j := i + 1; j < len(numbers); j++ {
			b := numbers[j]
			if b != nil && b.Sign() != 0 && new(big.Rat).Abs(b).Cmp(oneCent) < 0 && a.Cmp(b) != 0 && result[i] == result[j] {
				needsDetail = true
			}
		}
	}
	if needsDetail {
		for digits := 3; ; digits++ {
			for k, n := range numbers {
				if n != nil && n.Sign() != 0 && new(big.Rat).Abs(n).Cmp(oneCent) < 0 {
					result[k] = usdFixed(n, digits)
				}
			}
			collision := false
			for i, a := range numbers {
				if a == nil || a.Sign() == 0 || new(big.Rat).Abs(a).Cmp(oneCent) >= 0 {
					continue
				}
				for j := i + 1; j < len(numbers); j++ {
					if b := numbers[j]; b != nil && b.Sign() != 0 && new(big.Rat).Abs(b).Cmp(oneCent) < 0 && a.Cmp(b) != 0 && result[i] == result[j] {
						collision = true
					}
				}
			}
			if !collision {
				return result
			}
		}
	}
	return result
}

func signedUSD(value string) string {
	formatted := usdText(value)
	if strings.HasPrefix(formatted, "$") || strings.HasPrefix(formatted, "<") {
		n, ok := new(big.Rat).SetString(value)
		if ok && n.Sign() > 0 {
			return "+" + formatted
		}
	}
	return formatted
}
