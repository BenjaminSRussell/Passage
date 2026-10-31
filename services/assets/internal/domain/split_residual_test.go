package domain

import "testing"

// TestAllocateShares_SmallShareRoundingNeverLeavesTheLastTargetNegative pins the
// case the rapid property found: 0.07 split 7.15 / 7.15 / 78.58 / 7.12% used to
// round to 0.01 + 0.01 + 0.06 and leave -0.01 on the last target.
func TestAllocateShares_SmallShareRoundingNeverLeavesTheLastTargetNegative(t *testing.T) {
	shares, err := AllocateShares(BookAmounts{Cost: d("0.07"), Accumulated: d("0"), Impairment: d("0"), Salvage: d("0"), AcquisitionCost: d("0.07")},
		pcts("7.15", "7.15", "78.58", "7.12"), "USD")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0.01", "0.01", "0.05", "0.00"} // hand-derived: 0.005005 -> 0.01 twice, 0.055006 -> 0.06 capped at the 0.05 left
	for i, s := range shares {
		if s.Cost.StringFixed(2) != want[i] || s.AcquisitionCost.StringFixed(2) != want[i] {
			t.Errorf("share %d = cost %s / acquisition %s, want %s", i+1, s.Cost, s.AcquisitionCost, want[i])
		}
	}
}
