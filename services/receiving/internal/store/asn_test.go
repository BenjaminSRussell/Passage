package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	asnTable = "receiving_asn"
	asnThree = "00000000-0000-0000-0000-0000000000e3"
)

func sampleAsn(id, legalEntityID, tracking, qty string) store.Asn {
	return store.Asn{
		ID: id, LegalEntityID: legalEntityID, VendorID: vendorID, PurchaseOrderID: poID, TrackingNumber: tracking, CreatedBy: "vendor",
		ShipDate: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Lines:    []domain.AsnLine{{POLineNo: 1, Qty: decimal.RequireFromString(qty), Batch: "LOT-A", ExpiryDate: "2027-01-31"}},
	}
}

func asnReceipt(legalEntityID string) store.Receipt {
	return store.Receipt{
		LegalEntityID: legalEntityID, VendorID: vendorID, PurchaseOrderID: poID, PackingSlip: "ASN-" + ids.New()[24:], Date: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), Currency: "USD",
		Lines: []store.Line{{LineNo: 1, POLineNo: 1, ProductID: productID, Qty: decimal.NewFromInt(30), OrderedRemaining: decimal.NewFromInt(30), UnitPrice: decimal.NewFromInt(2), Unit: "EA", BatchTracked: true}},
	}
}

func TestAsn_BoundTrackingIsolationAndOneReceipt(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	ordered := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}

	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOne, db.one.LegalEntityID, "TRK-1", "60"), ordered); err != nil {
		t.Fatal(err)
	}
	var de *domain.Error
	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOther, db.one.LegalEntityID, "TRK-2", "41"), ordered); !errors.As(err, &de) || de.Code != domain.CodeAsnOverQty {
		t.Fatalf("over the open quantity: %v", err)
	}
	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOther, db.one.LegalEntityID, "TRK-1", "5"), ordered); !errors.As(err, &de) || de.Code != domain.CodeAsnTracking {
		t.Fatalf("duplicate tracking number: %v", err)
	}
	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOther, db.one.LegalEntityID, "TRK-2", "40"), ordered); err != nil {
		t.Fatalf("exactly the remainder: %v", err)
	}

	got, err := st.GetAsn(ctx, payeeOne)
	if err != nil || got.Status != domain.AsnOpen || got.Lines[0].Batch != "LOT-A" || got.Lines[0].ExpiryDate != "2027-01-31" || got.ShipDate.IsZero() || !got.ExpectedArrival.IsZero() {
		t.Fatalf("read %+v %v", got, err)
	}
	page, err := st.ListAsns(ctx, store.AsnFilter{PurchaseOrderID: poID, Limit: 1})
	if err != nil || len(page) != 2 {
		t.Fatalf("list with limit 1 returns the extra row for the cursor: %d %v", len(page), err)
	}

	if _, err := st.GetAsn(tenant.With(context.Background(), principal(2)), payeeOne); !errors.Is(err, store.ErrAsnNotFound) {
		t.Fatalf("another tenant read the ASN: %v", err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	otherCtx := tenant.With(context.Background(), otherEntity)
	if _, err := st.GetAsn(otherCtx, payeeOne); !errors.Is(err, store.ErrAsnNotFound) {
		t.Fatalf("another legal entity read the ASN: %v", err)
	}
	if rows, err := st.ListAsns(otherCtx, store.AsnFilter{}); err != nil || len(rows) != 0 {
		t.Fatalf("another legal entity lists %d ASNs: %v", len(rows), err)
	}
	if _, _, err := st.StartAsnReceipt(otherCtx, payeeOne, asnReceipt(otherEntity.LegalEntityID)); !errors.Is(err, store.ErrAsnNotFound) {
		t.Fatalf("another legal entity started a receipt: %v", err)
	}

	asn, rec, err := st.StartAsnReceipt(ctx, payeeOne, asnReceipt(db.one.LegalEntityID))
	if err != nil || asn.Status != domain.AsnReceiving || asn.ReceiptID != rec.ID || rec.State != domain.Arrived || len(rec.Lines) != 1 {
		t.Fatalf("start %+v %+v %v", asn, rec, err)
	}
	if _, _, err := st.StartAsnReceipt(ctx, payeeOne, asnReceipt(db.one.LegalEntityID)); !errors.As(err, &de) || de.Code != domain.CodeAsnConsumed {
		t.Fatalf("second receipt from one ASN: %v", err)
	}
	if n := db.rows(db.one, "receiving_receipt"); n != 2 {
		t.Fatalf("%d receipts, want the seeded one plus the ASN receipt", n)
	}
	if _, err := st.CreateAsn(ctx, sampleAsn(asnThree, db.one.LegalEntityID, "TRK-3", "1"), ordered); !errors.As(err, &de) || de.Code != domain.CodeAsnOverQty {
		t.Fatalf("a notice still holds its quantity while its receipt is ARRIVED: %v", err)
	}
	expiry := time.Date(2027, 1, 31, 0, 0, 0, 0, time.UTC)
	reg, err := st.Register(ctx, rec.ID, func(cur store.Receipt, _ map[domain.SerialKey]struct{}) ([]store.Line, error) {
		l := cur.Lines[0]
		l.LocationID, l.ExpiryDate = "00000000-0000-0000-0000-0000000000f1", expiry
		return []store.Line{l}, nil
	})
	if err != nil || !reg.Lines[0].ExpiryDate.Equal(expiry) {
		t.Fatalf("expiry stored on registration %+v %v", reg.Lines, err)
	}
	if _, err := st.CreateAsn(ctx, sampleAsn(asnThree, db.one.LegalEntityID, "TRK-3", "31"), ordered); !errors.As(err, &de) || de.Code != domain.CodeAsnOverQty {
		t.Fatalf("registered 30 and open 40 leave 30: %v", err)
	}
	if _, err := st.CreateAsn(ctx, sampleAsn(asnThree, db.one.LegalEntityID, "TRK-3", "30"), ordered); err != nil {
		t.Fatalf("a registered receipt releases the notice quantity: %v", err)
	}

	for name, sql := range map[string]string{
		"a delete":                `delete from ` + asnTable,
		"an update of the lines":  `update ` + asnTable + ` set lines = '[]'`,
		"an update of the vendor": `update ` + asnTable + ` set vendor_id = gen_random_uuid()`,
		"a return to OPEN":        `update ` + asnTable + ` set status = 'OPEN', receipt_id = null where id = '` + payeeOne + `'`,
	} {
		if err := db.run(db.one, sql); err == nil {
			t.Errorf("passage_app must not run %s", name)
		}
	}
}

func TestAsn_CancellingTheArrivedReceiptReopensTheNoticeAndItHoldsQuantityAgain(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	ordered := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}
	var de *domain.Error

	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOne, db.one.LegalEntityID, "TRK-1", "60"), ordered); err != nil {
		t.Fatal(err)
	}
	_, rec, err := st.StartAsnReceipt(ctx, payeeOne, asnReceipt(db.one.LegalEntityID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Cancel(ctx, rec.ID, func(store.Receipt) (store.Adjustment, error) { return store.Adjustment{}, nil }); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAsn(ctx, payeeOne)
	if err != nil || got.Status != domain.AsnOpen || got.ReceiptID != "" {
		t.Fatalf("after cancel %+v %v", got, err)
	}
	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOther, db.one.LegalEntityID, "TRK-2", "41"), ordered); !errors.As(err, &de) || de.Code != domain.CodeAsnOverQty {
		t.Fatalf("the reopened notice holds 60 again: %v", err)
	}
	if _, _, err := st.StartAsnReceipt(ctx, payeeOne, asnReceipt(db.one.LegalEntityID)); err != nil {
		t.Fatalf("a reopened notice can start another receipt: %v", err)
	}
}

func TestAsn_TrackingNumberIsTrimmedCaseFoldedAndScopedToTheVendor(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	ordered := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}
	var de *domain.Error

	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOne, db.one.LegalEntityID, "TRK-1", "5"), ordered); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOther, db.one.LegalEntityID, "  trk-1 ", "5"), ordered); !errors.As(err, &de) || de.Code != domain.CodeAsnTracking {
		t.Fatalf("same number in other case and padding: %v", err)
	}
}

func TestAsn_ConcurrentCreatesOnOnePurchaseOrderLineNeverExceedTheOrderedQuantity(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	ordered := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}
	ids := []string{payeeOne, payeeOther, asnThree}

	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = st.CreateAsn(ctx, sampleAsn(id, db.one.LegalEntityID, fmt.Sprintf("TRK-%d", i), "60"), ordered)
		}()
	}
	wg.Wait()
	accepted := 0
	for _, err := range errs {
		var de *domain.Error
		switch {
		case err == nil:
			accepted++
		case !errors.As(err, &de) || de.Code != domain.CodeAsnOverQty:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d notices of 60 accepted against 100, want 1", accepted)
	}
}

func TestAsn_ReopeningIsRefusedWhileTheReceiptIsLive(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	ordered := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}
	reopen := `update ` + asnTable + ` set status = 'OPEN', receipt_id = null where id = '` + payeeOne + `'`

	if _, err := st.CreateAsn(ctx, sampleAsn(payeeOne, db.one.LegalEntityID, "TRK-1", "60"), ordered); err != nil {
		t.Fatal(err)
	}
	_, rec, err := st.StartAsnReceipt(ctx, payeeOne, asnReceipt(db.one.LegalEntityID))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.run(db.one, reopen); err == nil {
		t.Fatal("reopened a notice whose receipt is ARRIVED")
	}
	if _, err := st.Register(ctx, rec.ID, func(cur store.Receipt, _ map[domain.SerialKey]struct{}) ([]store.Line, error) {
		l := cur.Lines[0]
		l.LocationID = "00000000-0000-0000-0000-0000000000f1"
		return []store.Line{l}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.run(db.one, reopen); err == nil {
		t.Fatal("reopened a notice whose receipt is REGISTERED")
	}
}
