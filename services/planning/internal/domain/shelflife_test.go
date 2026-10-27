package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// AC-22.15: a batch expiring in 20 days cannot serve a demand 5 days out that
// needs 30 days of remaining shelf life.
func TestAllocateBatches_AC2215ShortShelfLifeNotPegged(t *testing.T) {
	batches := []Batch{{ID: "B1", Qty: dec("100"), ExpiryDay: 20}}
	allocs, short := AllocateBatches(batches, dec("30"), 5, 30)
	if len(allocs) != 0 || !short.Equal(dec("30")) {
		t.Fatalf("allocs %+v shortfall %s; want none and 30", allocs, short)
	}
	if Usable(20, 5, 30) {
		t.Fatal("15 days left must not satisfy 30 required")
	}
}

func TestAllocateBatches_FEFOWithinUsable(t *testing.T) {
	batches := []Batch{
		{ID: "late", Qty: dec("50"), ExpiryDay: 300},
		{ID: "early", Qty: dec("40"), ExpiryDay: 200},
		{ID: "tie-b", Qty: dec("10"), ExpiryDay: 250},
		{ID: "tie-a", Qty: dec("10"), ExpiryDay: 250},
		{ID: "expired-soon", Qty: dec("99"), ExpiryDay: 10}, // not usable for day 0 with 30 days required
		{ID: "empty", Qty: decimal.Zero, ExpiryDay: 400},
	}
	allocs, short := AllocateBatches(batches, dec("80"), 0, 30)
	want := []BatchAllocation{{BatchID: "early", Qty: dec("40")}, {BatchID: "tie-a", Qty: dec("10")}, {BatchID: "tie-b", Qty: dec("10")}, {BatchID: "late", Qty: dec("20")}}
	if len(allocs) != len(want) {
		t.Fatalf("allocs = %+v, want %+v", allocs, want)
	}
	for i := range want {
		if allocs[i].BatchID != want[i].BatchID || !allocs[i].Qty.Equal(want[i].Qty) {
			t.Fatalf("alloc %d = %+v, want %+v", i, allocs[i], want[i])
		}
	}
	if !short.IsZero() {
		t.Fatalf("shortfall = %s, want 0", short)
	}
}

func TestUnpeggedExpiring(t *testing.T) {
	batches := []Batch{{ID: "A", Qty: dec("30"), ExpiryDay: 12}, {ID: "B", Qty: dec("20"), ExpiryDay: 90}}
	allocs := []BatchAllocation{{BatchID: "A", Qty: dec("10")}}
	got := UnpeggedExpiring(batches, allocs, 15)
	if len(got) != 1 || got[0].ID != "A" || !got[0].Qty.Equal(dec("20")) {
		t.Fatalf("unpegged expiring = %+v, want A with 20 left", got)
	}
}

func TestProducedExpiryDay(t *testing.T) {
	if ProducedExpiryDay(10, 45) != 55 {
		t.Fatal("produced expiry must be delivery plus shelf life")
	}
}

// Property: allocations never exceed a batch, only usable batches are used, and
// allocated plus shortfall equals the demand.
func TestProperty_AllocationConservesDemand(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 6).Draw(t, "n")
		batches := make([]Batch, n)
		byID := map[string]Batch{}
		for i := range batches {
			batches[i] = Batch{ID: string(rune('a' + i)),
				Qty:       decimal.NewFromInt(int64(rapid.IntRange(0, 100).Draw(t, "qty"))),
				ExpiryDay: rapid.IntRange(0, 120).Draw(t, "expiry")}
			byID[batches[i].ID] = batches[i]
		}
		demand := decimal.NewFromInt(int64(rapid.IntRange(0, 300).Draw(t, "demand")))
		day := rapid.IntRange(0, 60).Draw(t, "day")
		required := rapid.IntRange(0, 60).Draw(t, "required")

		allocs, short := AllocateBatches(batches, demand, day, required)
		sum := short
		for _, a := range allocs {
			b := byID[a.BatchID]
			if a.Qty.GreaterThan(b.Qty) || !Usable(b.ExpiryDay, day, required) {
				t.Fatalf("allocation %+v breaks batch %+v", a, b)
			}
			sum = sum.Add(a.Qty)
		}
		if !sum.Equal(demand) {
			t.Fatalf("allocated + short = %s, want %s", sum, demand)
		}
	})
}
