package store_test

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// The candidate inquiry lists what a FEFO request would take, in the order it
// would take it, and says why the other rows are left out; it moves nothing.
func TestReservationCandidatesFollowThePolicyAndExplainTheRest(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for batch, days := range map[string]int{"B1": 40, "B2": 10, "B3": 20, "B4": 3} {
		at := day.AddDate(0, 0, days)
		if err := s.UpsertBatchExpiry(ctx, product, batch, &at); err != nil {
			t.Fatal(err)
		}
	}
	post(t, ctx, s, domain.JournalOpeningBalance,
		line(product, "5", "1", dims("batch", "B1")), line(product, "6", "1", dims("batch", "B2")),
		line(product, "7", "1", dims("batch", "B3")), line(product, "8", "1", dims("batch", "B4")),
		line(product, "9", "1", dims("batch", "B5")), line(product, "2", "1", dims("batch", "B6", "inventory_status", "AVAILABLE")))
	if _, _, err := s.CreateBlocking(ctx, store.BlockingInput{IdempotencyKey: "blk", LegalEntityID: legalEntity(ctx), ProductID: product,
		Dims: dims("batch", "B2"), Qty: dec("6"), Unit: "EA", Target: domain.StockBlocked, Reason: "QUALITY", PhysicalDate: day}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec("3"), DemandType: domain.DemandManual,
		DemandID: "D1", Policy: domain.PolicyManual, Dims: dims("batch", "B1"), RequiredDate: day, IdempotencyKey: "res"}); err != nil {
		t.Fatal(err)
	}
	q := store.CandidateQuery{LegalEntityID: legalEntity(ctx), ProductID: product, Policy: domain.PolicyFEFO, RequiredDate: day, ShelfLifeMarginDays: 5, SegmentFallback: true}
	got, err := s.ReservationCandidates(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, c := range got {
		tag := fmt.Sprintf("%s:%s", c.Dims["batch"], c.Available)
		if !c.Eligible {
			tag += "!" + c.Reason
		}
		seen = append(seen, tag)
	}
	// B3 (day 20), B1 (day 40, 3 of 5 reserved), then B5 and B6 with no expiry; B4 expires inside the margin and B2 is blocked.
	// B6 is listed with its explicit status. B5/B6 have no expiry, so receipt date then batch id orders them.
	if want := "[B3:7 B1:2 B5:9 B6:2 B4:8!EXPIRES_BEFORE B2:6!RESTRICTED]"; fmt.Sprint(seen) != want {
		t.Fatalf("candidates %v, want %s", seen, want)
	}
	if got[0].Expiry == nil || !got[0].Expiry.Equal(day.AddDate(0, 0, 20)) {
		t.Fatalf("first candidate expiry %v, want day+20", got[0].Expiry)
	}
	// A dimension filter narrows the list; another legal entity of the tenant sees nothing.
	q.Dims = domain.Dimensions{"batch": "B5"}
	if got, err := s.ReservationCandidates(ctx, q); err != nil || len(got) != 1 || got[0].Dims["batch"] != "B5" {
		t.Fatalf("filtered: %v %v", got, err)
	}
	q.Dims = nil
	if got, err := s.ReservationCandidates(entityCtx(1, ids.New()), q); err != nil || len(got) != 0 {
		t.Fatalf("other legal entity: %v %v", got, err)
	}
	// Reading reserved nothing: a FEFO reservation for everything eligible still succeeds.
	if _, err := s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec("20"), DemandType: domain.DemandManual,
		DemandID: "D2", Policy: domain.PolicyFEFO, RequiredDate: day, ShelfLifeMarginDays: 5, IdempotencyKey: "fefo", Dims: domain.Dimensions{"site": "S1"}}); err != nil {
		t.Fatalf("reserve after the inquiry: %v", err)
	}
}

// Regression for the rest of the multi-row writes found by the Wave 16 proof: two
// requests introducing the same new dimension sets in different line orders used to
// deadlock (40P01) on each other's uncommitted dimension_set inserts.
func TestConcurrentOrdersWithSharedNewDimensionSetsDoNotDeadlock(t *testing.T) {
	_, app, _ := setup(t)
	hot := wideStore(t, app, 24)
	ctx := entityCtx(1, ids.New())
	const rounds, writers, sets = 6, 12, 20
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		errs := make([]error, writers*2)
		for w := 0; w < writers; w++ {
			order := rand.New(rand.NewPCG(uint64(round), uint64(w))).Perm(sets)
			wg.Add(2)
			go func() {
				defer wg.Done()
				o := transferOrder(ctx, ids.New(), "1", false)
				o.Lines = nil
				for n, i := range order {
					o.Lines = append(o.Lines, domain.TransferLine{LineNo: n + 1, ProductID: ids.New(), Unit: "EA", Qty: dec("1"),
						Dims: dims("location", fmt.Sprintf("T%d-%02d", round, i))})
				}
				_, _, errs[w] = hot.CreateTransfer(ctx, ids.New(), o)
			}()
			go func() {
				defer wg.Done()
				o := replenishment(ctx, ids.New(), "1", "1")
				o.Lines = nil
				for n, i := range order {
					o.Lines = append(o.Lines, domain.ConsignmentLine{LineNo: n + 1, ProductID: ids.New(), Unit: "EA", Qty: dec("1"), Price: dec("1"),
						Dims: domain.Dimensions{"location": fmt.Sprintf("C%d-%02d", round, i)}})
				}
				_, _, errs[writers+w] = hot.CreateConsignment(ctx, ids.New(), o)
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d writer %d: %v", round, i, err)
			}
		}
	}
}
