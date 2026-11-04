package store_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func claimOf(ctx context.Context, product, key, kind, qty string) store.IssueClaimInput {
	return store.IssueClaimInput{IdempotencyKey: key, LegalEntityID: legalEntityOf(ctx), ProductID: product, Warehouse: "W1", Kind: kind, Qty: dec(qty)}
}

func TestIssueClaim_RepeatedClaimsNeverEatHeldStock(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims("warehouse", "W1", "batch", "B1")))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "8", "qo-1", dims("warehouse", "W1", "batch", "B1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "c1", "CONSUME", "2")); err != nil {
		t.Fatalf("claim of the free quantity: %v", err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "c2", "CONSUME", "2")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("second claim of the same free quantity: %v, want %s", err, domain.CodeQualityHold)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "c3", "SHIP", "2")); err != nil {
		t.Fatalf("another kind has its own tally: %v", err)
	}
	if err := s.ReleaseIssueClaim(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "c2", "CONSUME", "2")); err != nil {
		t.Fatalf("claim after the first was released: %v", err)
	}
}

func TestIssueClaim_ReplayReuseAndUsageDecision(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims("warehouse", "W1")))
	res, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "9", "qo-1", dims("warehouse", "W1")))
	if err != nil {
		t.Fatal(err)
	}
	if replayed, err := s.ClaimIssue(ctx, claimOf(ctx, product, "k", "PICK", "1")); err != nil || replayed {
		t.Fatalf("first claim: replayed %t, %v", replayed, err)
	}
	if replayed, err := s.ClaimIssue(ctx, claimOf(ctx, product, "k", "PICK", "1")); err != nil || !replayed {
		t.Fatalf("repeat: replayed %t, %v", replayed, err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "k", "PICK", "2")); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other quantity: %v", err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "big", "PICK", "5")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("claim into the hold: %v", err)
	}
	if _, err := s.ReleaseHold(ctx, res.Holds[0].ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "big", "PICK", "5")); err != nil {
		t.Fatalf("claim after the usage decision: %v", err)
	}
}

func TestIssueClaim_OtherWarehouseAndRestrictedStockAreNotFree(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims("warehouse", "W1")),
		line(product, "100", "1", dims("warehouse", "W2")), line(product, "50", "1", dims("warehouse", "W1", domain.DimStatus, domain.StockBlocked)))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "9", "qo-1", dims("warehouse", "W1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "c1", "SHIP", "2")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("free stock elsewhere or in a blocked row hid the hold: %v", err)
	}
	other := claimOf(ctx, product, "c2", "SHIP", "50")
	other.Warehouse = "W2"
	if _, err := s.ClaimIssue(ctx, other); err != nil {
		t.Fatalf("claim in a warehouse with no hold: %v", err)
	}
}

func TestIssueClaim_ConcurrentClaimsAgainstOneHoldAdmitOnlyTheFreeQuantity(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims("warehouse", "W1")))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "7", "qo-1", dims("warehouse", "W1"))); err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, fmt.Sprintf("c%d", i), "CONSUME", "1")); err == nil {
				admitted.Add(1)
			} else if problemCode(err) != domain.CodeQualityHold {
				t.Errorf("claim %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 3 {
		t.Fatalf("%d claims admitted against 3 free units", admitted.Load())
	}
}
