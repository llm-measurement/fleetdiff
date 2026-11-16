// SPDX-License-Identifier: Apache-2.0
// Code authors: Vijay and Codex

package change

import (
	"math"
	"math/big"
	"testing"
)

func integerInterval(lower, upper int64) RationalInterval {
	return RationalInterval{big.NewRat(lower, 1), big.NewRat(upper, 1)}
}

func assertInterval(t *testing.T, interval RationalInterval, lower, upper string) {
	t.Helper()
	if interval.Lower.RatString() != lower || interval.Upper.RatString() != upper {
		t.Fatalf("interval = [%s, %s], want [%s, %s]", interval.Lower, interval.Upper, lower, upper)
	}
}

func TestUniformGrowthHasZeroShareExcess(t *testing.T) {
	before := RationalInterval{big.NewRat(1, 10), big.NewRat(1, 10)}
	after := RationalInterval{big.NewRat(3, 10), big.NewRat(3, 10)}
	got, err := CompareContributor(before, after, big.NewRat(1, 2), big.NewRat(3, 2))
	if err != nil || got.Direction != Increased || got.AdjustedShift != WithinBounds {
		t.Fatalf("unexpected change %+v: %v", got, err)
	}
	assertInterval(t, got.Delta, "1/5", "1/5")
	assertInterval(t, *got.ShareExcess, "0", "0")
}

func TestContributorIntervalsUseBothWindows(t *testing.T) {
	got, err := CompareContributor(integerInterval(18, 24), integerInterval(25, 30), big.NewRat(100, 1), big.NewRat(120, 1))
	if err != nil || got.Direction != Increased || got.AdjustedShift != WithinBounds {
		t.Fatalf("unexpected change %+v: %v", got, err)
	}
	assertInterval(t, got.Delta, "1", "12")
	assertInterval(t, *got.ShareExcess, "-19/5", "42/5")

	got, err = CompareContributor(integerInterval(20, 25), integerInterval(25, 30), big.NewRat(50, 1), big.NewRat(50, 1))
	if err != nil || got.Direction != WithinBounds {
		t.Fatalf("touching intervals are not a proven mover: %+v, %v", got, err)
	}
	assertInterval(t, got.Delta, "0", "10")
}

func TestShareExcessDoesNotOverflowOrRoundAwayChange(t *testing.T) {
	before := big.NewRat(math.MaxInt64, 1)
	after := big.NewRat(math.MaxInt64-1, 1)
	got, err := CompareContributor(integerInterval(1, 1), integerInterval(1, 1), before, after)
	if err != nil || got.Direction != WithinBounds || got.AdjustedShift != Increased {
		t.Fatalf("unexpected change %+v: %v", got, err)
	}
	assertInterval(t, *got.ShareExcess, "1/9223372036854775807", "1/9223372036854775807")
}

func TestMissingDenominatorAndInvalidBounds(t *testing.T) {
	got, err := CompareContributor(integerInterval(0, 0), integerInterval(1, 1), big.NewRat(0, 1), big.NewRat(2, 1))
	if err != nil || got.ShareExcess != nil || got.AdjustedShift != "" || got.Direction != Increased {
		t.Fatalf("zero baseline did not leave the adjusted result unavailable: %+v, %v", got, err)
	}
	for _, invalid := range []RationalInterval{{}, integerInterval(-1, 1), integerInterval(3, 2), integerInterval(1, 3)} {
		if _, err := CompareContributor(invalid, integerInterval(0, 0), big.NewRat(2, 1), big.NewRat(1, 1)); err == nil {
			t.Fatalf("accepted invalid interval %+v", invalid)
		}
	}
}
