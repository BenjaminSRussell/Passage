package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func str(s string) *string { return &s }

// FR-26.11 against Postgres: a revise changes the name and end date at the version, writes
// rebateagreement.revised, replays on a repeated key and refuses tiers after an accrual.
func TestPostgres_RebateReviseAndClose(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "rl-1")
	if err != nil || a.Version != 1 || a.Status != domain.AgreementActive {
		t.Fatalf("create = %+v, %v; want version 1 ACTIVE", a, err)
	}
	// Tiers may change while nothing has accrued.
	tiers := []domain.Tier{tier("0", "3")}
	r, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 1, Name: str("Renamed"), Tiers: &tiers}, "rl-rev-1")
	if err != nil || r.Version != 2 || r.Name != "Renamed" || r.Tiers[0].RatePct.String() != "3" {
		t.Fatalf("revise = %+v, %v", r, err)
	}
	if again, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 1, Name: str("Renamed"), Tiers: &tiers}, "rl-rev-1"); err != nil || again.Version != 2 {
		t.Fatalf("replayed revise = %+v, %v; want the first response", again, err)
	}
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 1, Name: str("Other")}, "rl-rev-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("reused key = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 1, Name: str("Stale")}, ""); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale revise = %v; want %s", err, store.CodeVersionConflict)
	}

	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-1", "2026-06-15", "10000")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 2, Tiers: &tiers}, ""); problemCode(err) != domain.CodeAgreementHasAccruals {
		t.Fatalf("tiers after accrual = %v; want %s", err, domain.CodeAgreementHasAccruals)
	}
	early := day("2026-06-01")
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 2, ValidTo: &early}, ""); problemCode(err) != domain.CodeAgreementEndsEarly {
		t.Fatalf("end before accrual = %v; want %s", err, domain.CodeAgreementEndsEarly)
	}

	if _, err := s.CloseAgreement(ctx, a.ID, 2, day("2026-06-01"), ""); problemCode(err) != domain.CodeAgreementEndsEarly {
		t.Fatalf("close before accrual = %v; want %s", err, domain.CodeAgreementEndsEarly)
	}
	if _, err := s.CloseAgreement(ctx, a.ID, 2, day("2027-01-31"), ""); problemCode(err) != domain.CodeValidation {
		t.Fatalf("close after validity = %v; want %s", err, domain.CodeValidation)
	}
	c, err := s.CloseAgreement(ctx, a.ID, 2, day("2026-09-30"), "rl-close-1")
	if err != nil || c.Status != domain.AgreementClosed || c.Version != 3 || dayStrOf(c) != "2026-09-30" {
		t.Fatalf("close = %+v, %v", c, err)
	}
	if again, err := s.CloseAgreement(ctx, a.ID, 2, day("2026-09-30"), "rl-close-1"); err != nil || again.Version != 3 {
		t.Fatalf("replayed close = %+v, %v", again, err)
	}
	if _, err := s.CloseAgreement(ctx, a.ID, 3, day("2026-09-30"), ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("close twice = %v; want %s", err, store.CodeStateInvalid)
	}
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 3, Name: str("x")}, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("revise closed = %v; want %s", err, store.CodeStateInvalid)
	}
	// Invoices before the close date still accrue; later ones do not (BR-26.8).
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-2", "2026-09-30", "1000")); err != nil {
		t.Fatalf("invoice on the close date = %v; want accrued", err)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-3", "2026-10-01", "1000")); problemCode(err) != domain.CodeValidation {
		t.Fatalf("invoice after close = %v; want %s", err, domain.CodeValidation)
	}
	got, err := s.GetAgreement(ctx, a.ID)
	if err != nil || got.Status != domain.AgreementClosed {
		t.Fatalf("read = %+v, %v", got, err)
	}
	if _, err := s.CloseAgreement(ctx, "00000000-0000-0000-0005-0000000000ff", 1, day("2026-09-30"), ""); problemCode(err) != store.CodeAgreementNotFound {
		t.Fatalf("unknown agreement = %v; want %s", err, store.CodeAgreementNotFound)
	}

	var revised, closed int
	for _, row := range outboxRows(t, ctx, pool) {
		switch row.Type {
		case "dev.passage.pricing.rebateagreement.revised.v1":
			revised++
		case "dev.passage.pricing.rebateagreement.closed.v1":
			closed++
			if row.Data["status"] != "CLOSED" || row.Data["valid_to"] != "2026-09-30" || row.Data["agreement_id"] != a.ID {
				t.Fatalf("closed event = %v", row.Data)
			}
		}
	}
	if revised != 1 || closed != 1 {
		t.Fatalf("events revised=%d closed=%d; want one each (replays write nothing)", revised, closed)
	}
}

// An accrual waits for an in-flight close of its agreement and then sees the new end date:
// an invoice dated after the close date is refused rather than accrued against the stale
// validity the accrual would otherwise read (BR-26.8).
func TestPostgres_RebateAccrualWaitsForClose(t *testing.T) {
	admin, app := adminAndAppPools(t)
	ctx := principal(1, "")
	s := store.New(app)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-1", "2026-06-15", "1000")); err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `update rebate_agreement set status = 'CLOSED', valid_to = '2026-06-30', version = version + 1 where id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-2", "2026-09-15", "1000"))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("accrual finished while the close was in flight: %v; want it to wait", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; problemCode(err) != domain.CodeValidation {
		t.Fatalf("accrual after the close = %v; want %s (outside the validity)", err, domain.CodeValidation)
	}
}

// The per-period delta of a retroactive recompute is dated on the activation day, not on an
// invoice: closing after the last replayed invoice is allowed even when that is before the
// activation (PRC-0048 counts invoice lines only).
func TestPostgres_RebateCloseAfterRetroRecompute(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "")
	if err != nil {
		t.Fatal(err)
	}
	lines := []store.RetroLine{{InvoiceLineID: "INV-R", InvoiceDate: day("2026-03-05"), Value: decimal.NewFromInt(10000), Currency: "USD"}}
	if _, err := s.RecomputeRetro(ctx, store.RetroInput{AgreementID: a.ID, RunKey: "rl-retro", Lines: lines}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseAgreement(ctx, a.ID, 1, day("2026-03-04"), ""); problemCode(err) != domain.CodeAgreementEndsEarly {
		t.Fatalf("close before the replayed invoice = %v; want %s", err, domain.CodeAgreementEndsEarly)
	}
	c, err := s.CloseAgreement(ctx, a.ID, 1, day("2026-03-31"), "")
	if err != nil || c.Status != domain.AgreementClosed {
		t.Fatalf("close after the replayed invoice = %+v, %v; want CLOSED", c, err)
	}
}

func dayStrOf(a domain.RebateAgreement) string { return a.ValidTo.Format("2006-01-02") }

// FR-26.11 against Postgres: the product scope changes until the first accrual, the revised
// event carries it, the list filters by it, and a scope change after an accrual is PRC-0071.
func TestPostgres_RebateReviseProductScope(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	const product, group = "00000000-0000-0000-0008-0000000000a1", "00000000-0000-0000-0008-0000000000a2"
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "scope-1")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 1, Scope: &store.ProductScope{ProductID: product}}, "scope-rev-1")
	if err != nil || r.ProductID != product || r.ProductGroupID != "" || r.Version != 2 {
		t.Fatalf("scope to product = %+v, %v", r, err)
	}
	if again, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 1, Scope: &store.ProductScope{ProductID: product}}, "scope-rev-1"); err != nil || again.Version != 2 {
		t.Fatalf("replayed scope revise = %+v, %v", again, err)
	}
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 1, Scope: &store.ProductScope{ProductGroupID: group}}, "scope-rev-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("reused key with another scope = %v; want %s", err, store.CodeIdempotencyUse)
	}
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 2, Scope: &store.ProductScope{ProductID: product, ProductGroupID: group}}, ""); problemCode(err) != domain.CodeValidation {
		t.Fatalf("both scopes = %v; want %s", err, domain.CodeValidation)
	}
	byProduct, err := s.ListAgreements(ctx, store.AgreementFilter{ProductID: product})
	if err != nil || len(byProduct) != 1 || byProduct[0].ID != a.ID {
		t.Fatalf("list by product = %+v, %v; want the agreement", byProduct, err)
	}
	if byGroup, err := s.ListAgreements(ctx, store.AgreementFilter{ProductGroupID: group}); err != nil || len(byGroup) != 0 {
		t.Fatalf("list by group = %+v, %v; want none", byGroup, err)
	}
	g, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 2, Scope: &store.ProductScope{ProductGroupID: group}}, "")
	if err != nil || g.ProductID != "" || g.ProductGroupID != group {
		t.Fatalf("scope to group = %+v, %v", g, err)
	}
	line := invoice(a.ID, "INV-S1", "2026-06-15", "10000")
	line.ProductGroupID = group
	if _, _, err := s.AccrueInvoiceLine(ctx, line); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 3, Scope: &store.ProductScope{}}, ""); problemCode(err) != domain.CodeScopeHasAccruals {
		t.Fatalf("clear scope after accrual = %v; want %s", err, domain.CodeScopeHasAccruals)
	}
	// Restating the same scope is not a change.
	if same, err := s.ReviseAgreement(ctx, store.AgreementRevision{ID: a.ID, Version: 3, Scope: &store.ProductScope{ProductGroupID: group}}, ""); err != nil || same.Version != 4 {
		t.Fatalf("same scope after accrual = %+v, %v; want accepted", same, err)
	}
}
