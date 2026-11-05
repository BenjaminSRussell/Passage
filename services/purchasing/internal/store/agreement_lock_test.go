package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// twoAgreementLines creates two effective agreements of one vendor and returns the ids of their quantity lines,
// the lower id first, with the products they are for.
func twoAgreementLines(t *testing.T, s *store.Postgres, ctx context.Context, vendor string) (lo, hi, loProduct, hiProduct string) {
	t.Helper()
	pa, pb := ids.New(), ids.New()
	a, err := s.CreateAgreement(ctx, blanket(t, vendor, pa), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateAgreement(ctx, blanket(t, vendor, pb), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	lo, hi, loProduct, hiProduct = a.Agreement.Lines[0].ID, b.Agreement.Lines[0].ID, pa, pb
	if lo > hi {
		lo, hi, loProduct, hiProduct = hi, lo, hiProduct, loProduct
	}
	return lo, hi, loProduct, hiProduct
}

// releaseBoth is a PO of two lines, the first drawing first and the second second, so a writer that locks agreement
// lines in PO line order locks them in the order the caller picked.
func releaseBoth(t *testing.T, vendor, firstLine, firstProduct, secondLine, secondProduct string) store.PurchaseOrder {
	po := release(t, vendor, firstProduct, "10", firstLine, "2027-03-01")
	second := release(t, vendor, secondProduct, "10", secondLine, "2027-03-01").Lines[0]
	second.LineNo = 2
	po.Lines = append(po.Lines, second)
	po.Subtotal, po.Total = po.Subtotal.Add(second.LineAmount), po.Total.Add(second.LineAmount)
	return po
}

// waitsInIDOrder holds the lower agreement line in another transaction, runs write, and checks it waits there
// without having locked the higher line first (NOWAIT on it succeeds); a writer that locks agreement lines in the
// order its PO lines name them would already hold the higher one. It then lets write finish and returns its error.
func waitsInIDOrder(t *testing.T, pool *pgxpool.Pool, n int, lo, hi string, write func() error) error {
	t.Helper()
	admin, _ := adminPools.Load(pool)
	tx, err := admin.(*pgxpool.Pool).Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tenantID := testkit.TenantID(n)
	if _, err := tx.Exec(context.Background(), `select 1 from purchase_agreement_line where tenant_id = $1 and id = $2 for update`, tenantID, lo); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- write() }()
	time.Sleep(500 * time.Millisecond) // the writer is waiting for the lower line
	select {
	case err := <-done:
		t.Fatalf("the writer did not wait for the lower agreement line: %v", err)
	default:
	}
	if _, err := tx.Exec(context.Background(), `select 1 from purchase_agreement_line where tenant_id = $1 and id = $2 for update nowait`, tenantID, hi); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			// Let the writer finish before failing, so the pool closes cleanly.
			_ = tx.Rollback(context.Background())
			<-done
			t.Fatalf("the writer holds the higher agreement line while it waits for the lower one: locks are taken in PO line order, not id order")
		}
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return <-done
}

// Two POs that draw the same two agreement lines in opposite order would deadlock if each locked the lines in its
// own line order. Every writer takes agreement lines in id order, so a PO listing the higher id first still waits
// at the lower line without holding the higher one.
func TestPurchaseOrder_LocksAgreementLinesInIDOrder(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(21)
	vendor := ids.New()
	lo, hi, loProduct, hiProduct := twoAgreementLines(t, s, ctx, vendor)
	err := waitsInIDOrder(t, pool, 21, lo, hi, func() error {
		_, err := s.CreatePurchaseOrder(ctx, releaseBoth(t, vendor, hi, hiProduct, lo, loProduct), store.Idem{})
		return err
	})
	if err != nil {
		t.Fatalf("PO = %v", err)
	}
}

// Cancelling a PO gives its agreement lines back in id order too, whatever the order of the PO's lines.
func TestPurchaseOrderCancel_ReleasesAgreementLinesInIDOrder(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(21)
	vendor := ids.New()
	lo, hi, loProduct, hiProduct := twoAgreementLines(t, s, ctx, vendor)
	po, err := s.CreatePurchaseOrder(ctx, releaseBoth(t, vendor, hi, hiProduct, lo, loProduct), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	err = waitsInIDOrder(t, pool, 21, lo, hi, func() error {
		_, err := s.ActOnPurchaseOrder(ctx, po.ID, po.RowVersion, domain.POActionCancel, store.Idem{})
		return err
	})
	if err != nil {
		t.Fatalf("cancel = %v", err)
	}
}

// A consolidation writes several POs in one transaction; it locks the agreement lines of all of them, in id order,
// before it writes the first. Locked PO by PO, the first PO (drawing the higher line) would hold that line while
// the second waits for the lower one, and a writer taking both in id order would deadlock with it.
func TestConsolidation_LocksAgreementLinesOfEveryOrderBeforeTheFirst(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	vendor := ids.New()
	lo, hi, loProduct, hiProduct := twoAgreementLines(t, s, ctx, vendor)
	req := approved(t, s, ids.New(), "10", "20")
	drawing := func(l store.RequisitionLine, agreementLine, product string) store.PurchaseOrder {
		po := orderFor(t, vendor, l)
		po.Lines[0].ProductID, po.Lines[0].AgreementLineID, po.Lines[0].PriceSource = product, agreementLine, "AGREEMENT:"+agreementLine
		po.Lines[0].DeliveryDate = day(t, "2027-03-01")
		return po
	}
	orders := []store.PurchaseOrder{drawing(req.Lines[0], hi, hiProduct), drawing(req.Lines[1], lo, loProduct)}
	var out []store.PurchaseOrder
	err := waitsInIDOrder(t, pool, 31, lo, hi, func() error {
		var err error
		out, err = s.ConsolidateRequisitionLines(ctx, orders, []string{req.Lines[0].ID, req.Lines[1].ID}, store.Idem{})
		return err
	})
	if err != nil || len(out) != 2 {
		t.Fatalf("consolidation = %d POs, %v", len(out), err)
	}
	for _, id := range []string{lo, hi} {
		line, err := s.GetAgreementLine(ctx, id)
		if err != nil || !line.Lines[0].ReleasedQty.Equal(dec("1")) {
			t.Fatalf("agreement line %s after the consolidation = %+v, %v; want 1 released", id, line.Lines, err)
		}
	}
}
