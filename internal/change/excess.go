// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package change

import (
	"errors"
	"math/big"
)

// RationalInterval holds inclusive bounds in the source measurement's unit.
// Exact rows use identical endpoints; summaries use deterministic sketch bounds.
type RationalInterval struct {
	Lower, Upper *big.Rat
}

type Movement string

const (
	Increased    Movement = "increased"
	Decreased    Movement = "decreased"
	WithinBounds Movement = "within_bounds"
)

type ContributorChange struct {
	Delta         RationalInterval
	ShareExcess   *RationalInterval
	Direction     Movement
	AdjustedShift Movement
}

var errInvalidContributor = errors.New("invalid contributor bounds or window total")

// CompareContributor uses exact units and doesn't mutate its inputs. The caller
// must establish comparable coverage and a shared measurement basis before
// invoking it. A zero baseline leaves share-adjustment unavailable.
func CompareContributor(before, after RationalInterval, beforeTotal, afterTotal *big.Rat) (ContributorChange, error) {
	if !validContributor(before, beforeTotal) || !validContributor(after, afterTotal) {
		return ContributorChange{}, errInvalidContributor
	}
	raw := intervalDifference(after, before)
	result := ContributorChange{Delta: raw, Direction: movement(raw)}
	if beforeTotal.Sign() != 0 {
		ratio := new(big.Rat).Quo(afterTotal, beforeTotal)
		expected := RationalInterval{
			Lower: new(big.Rat).Mul(ratio, before.Lower),
			Upper: new(big.Rat).Mul(ratio, before.Upper),
		}
		adjusted := intervalDifference(after, expected)
		result.ShareExcess, result.AdjustedShift = &adjusted, movement(adjusted)
	}
	return result, nil
}

func validContributor(value RationalInterval, total *big.Rat) bool {
	if value.Lower == nil || value.Upper == nil || total == nil {
		return false
	}
	return value.Lower.Sign() >= 0 && value.Lower.Cmp(value.Upper) <= 0 && value.Upper.Cmp(total) <= 0
}

func intervalDifference(after, before RationalInterval) RationalInterval {
	return RationalInterval{
		Lower: new(big.Rat).Sub(after.Lower, before.Upper),
		Upper: new(big.Rat).Sub(after.Upper, before.Lower),
	}
}

func movement(delta RationalInterval) Movement {
	switch {
	case delta.Lower.Sign() > 0:
		return Increased
	case delta.Upper.Sign() < 0:
		return Decreased
	default:
		return WithinBounds
	}
}
