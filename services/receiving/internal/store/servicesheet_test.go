package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const sheetTable = "receiving_service_entry_sheet"

func sampleSheet(id, legalEntityID string, amount string) store.ServiceSheet {
	a := decimal.RequireFromString(amount)
	return store.ServiceSheet{
		ID: id, LegalEntityID: legalEntityID, VendorID: vendorID, PurchaseOrderID: poID, Currency: "USD", Total: a, CreatedBy: "alice",
		Lines: []domain.SheetLine{{POLineNo: 1, Description: "Consulting", Unit: "H", Qty: decimal.NewFromInt(1), UnitPrice: a, Amount: a}},
	}
}

func sheetEvent(s store.ServiceSheet) (events.Event, error) {
	return events.New("dev.passage.receiving.serviceentrysheet.decided.v1", "/passage/receiving/receiving", s.ID, testkit.TenantID(1), map[string]string{"decision": s.Status})
}

func TestServiceSheet_CapDecisionSegregationAndIsolation(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	limits := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}
	if _, err := st.CreateSheet(ctx, sampleSheet(payeeOne, db.one.LegalEntityID, "70"), limits); err != nil {
		t.Fatal(err)
	}
	var de *domain.Error
	if _, err := st.CreateSheet(ctx, sampleSheet(payeeOther, db.one.LegalEntityID, "40"), limits); !errors.As(err, &de) || de.Code != domain.CodeSheetOverLine {
		t.Fatalf("over the cap: %v", err)
	}
	if _, err := st.DecideSheet(ctx, payeeOne, "alice", true, "", sheetEvent); !errors.As(err, &de) || de.Code != domain.CodeSheetSelfAccept {
		t.Fatalf("author decided: %v", err)
	}
	rejected, err := st.DecideSheet(ctx, payeeOne, "bob", false, "not performed", sheetEvent)
	if err != nil || rejected.Status != domain.SheetRejected || rejected.RejectReason != "not performed" || rejected.DecidedAt == nil {
		t.Fatalf("reject: %+v %v", rejected, err)
	}
	if _, err := st.DecideSheet(ctx, payeeOne, "bob", true, "", sheetEvent); !errors.As(err, &de) || de.Code != domain.CodeSheetNotPending {
		t.Fatalf("decided twice: %v", err)
	}
	if _, err := st.CreateSheet(ctx, sampleSheet(payeeOther, db.one.LegalEntityID, "100"), limits); err != nil {
		t.Fatalf("a rejected sheet still holds the cap: %v", err)
	}
	got, err := st.GetSheet(ctx, payeeOther)
	if err != nil || got.Status != domain.SheetPending || len(got.Lines) != 1 || got.Lines[0].Amount.String() != "100" {
		t.Fatalf("read %+v %v", got, err)
	}
	outbox := db.rows(db.one, "outbox")
	if outbox != 1 {
		t.Fatalf("%d outbox rows, want 1 (only the committed decision)", outbox)
	}
	if _, err := st.GetSheet(tenant.With(context.Background(), principal(2)), payeeOther); !errors.Is(err, store.ErrSheetNotFound) {
		t.Fatalf("another tenant read the sheet: %v", err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	if _, err := st.GetSheet(tenant.With(context.Background(), otherEntity), payeeOther); !errors.Is(err, store.ErrSheetNotFound) {
		t.Fatalf("another legal entity read the sheet: %v", err)
	}
	for name, sql := range map[string]string{
		"a delete":                `delete from ` + sheetTable,
		"an update of the lines":  `update ` + sheetTable + ` set lines = '[]'`,
		"an update of the total":  `update ` + sheetTable + ` set total = 0`,
		"a return to PENDING":     `update ` + sheetTable + ` set status = 'PENDING', decided_by = null`,
		"an update of the author": `update ` + sheetTable + ` set created_by = 'bob'`,
	} {
		if err := db.run(db.one, sql); err == nil {
			t.Errorf("passage_app must not run %s", name)
		}
	}
}

func TestServiceSheet_ConcurrentCreatesCannotExceedTheCap(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	limits := map[int]decimal.Decimal{1: decimal.NewFromInt(100)}
	const attempts = 12
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			_, err := st.CreateSheet(ctx, sampleSheet(ids.New(), db.one.LegalEntityID, "20"), limits)
			results <- err
		}()
	}
	created := 0
	for i := 0; i < attempts; i++ {
		var de *domain.Error
		switch err := <-results; {
		case err == nil:
			created++
		case !errors.As(err, &de) || de.Code != domain.CodeSheetOverLine:
			t.Fatalf("unexpected error %v", err)
		}
	}
	if created != 5 {
		t.Fatalf("%d sheets of 20 fit a line of 100, want 5", created)
	}
}

func TestServiceSheet_AcceptWritesPostedReceiptAndGrir(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	created, err := st.CreateSheet(ctx, sampleSheet(payeeOne, db.one.LegalEntityID, "70"), map[int]decimal.Decimal{1: decimal.NewFromInt(100)})
	if err != nil {
		t.Fatal(err)
	}
	productID := "00000000-0000-0000-0000-0000000000d1"
	accepted, err := st.AcceptSheet(ctx, created.ID, "bob", func(s store.ServiceSheet, allocated store.Receipt) (store.SheetAcceptOutcome, error) {
		rec := allocated
		rec.PackingSlip, rec.Date, rec.Currency = "SES-"+s.ID, s.CreatedAt, s.Currency
		rec.Lines = []store.Line{{LineNo: 1, POLineNo: 1, ProductID: productID, Qty: s.Lines[0].Qty, UnitPrice: s.Lines[0].UnitPrice, Currency: s.Currency}}
		decided, err := sheetEvent(s)
		if err != nil {
			return store.SheetAcceptOutcome{}, err
		}
		posted, err := events.New("dev.passage.receiving.receipt.posted.v1", "/passage/receiving/receiving", rec.ID, testkit.TenantID(1), map[string]string{"receipt_id": rec.ID})
		if err != nil {
			return store.SheetAcceptOutcome{}, err
		}
		return store.SheetAcceptOutcome{
			Receipt: rec, LedgerEntryID: "ses-entry",
			Amounts: []store.LineAmount{{LineNo: 1, Amount: s.Total, AmountAcct: s.Total}},
			Events:  []events.Event{decided, posted},
		}, nil
	})
	if err != nil || accepted.Status != domain.SheetAccepted || accepted.ReceiptID == "" || accepted.LedgerEntryID != "ses-entry" {
		t.Fatalf("accept %+v %v", accepted, err)
	}
	rec, err := st.GetReceipt(ctx, accepted.ReceiptID)
	if err != nil || rec.Source != domain.ReceiptSourceService || rec.PackingSlip != "SES-"+created.ID || rec.State != domain.Posted || rec.Number == "" {
		t.Fatalf("receipt %+v %v", rec, err)
	}
	page, err := st.ListGrir(ctx, store.GrirFilter{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ReceiptID != rec.ID || page.Items[0].Status != "OPEN" {
		t.Fatalf("GR/IR %+v %v", page.Items, err)
	}
	received, err := st.ReceivedOnPurchaseOrder(ctx, poID)
	if err != nil || received[1].Sign() != 0 {
		t.Fatalf("service receipts must not count as goods received: %v err %v", received, err)
	}
	if db.rows(db.one, "outbox") != 2 {
		t.Fatalf("outbox %d, want decided and receipt.posted", db.rows(db.one, "outbox"))
	}
}

func TestSelfBill_PutGetAndIsolation(t *testing.T) {
	db := seedInvoice(t)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	evt, err := events.New("dev.passage.receiving.ersselfbill.issued.v1", "/passage/receiving/receiving", invoiceID, testkit.TenantID(1), map[string]string{"invoice_id": invoiceID})
	if err != nil {
		t.Fatal(err)
	}
	in := store.SelfBill{
		InvoiceID: invoiceID, LegalEntityID: db.one.LegalEntityID, VendorID: vendorID, InvoiceNumber: "ERS-RCV-00000001",
		ContentType: "application/pdf", Body: []byte("%PDF-1.4 test"), TaxCode: "VAT19", TaxAmount: decimal.RequireFromString("95"), Total: decimal.RequireFromString("595"),
	}
	if err := st.PutSelfBill(ctx, in, evt); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSelfBill(ctx, in, evt); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSelfBill(ctx, invoiceID)
	if err != nil || got.InvoiceNumber != in.InvoiceNumber || string(got.Body) != string(in.Body) || got.TaxCode != "VAT19" {
		t.Fatalf("got %+v %v", got, err)
	}
	if db.rows(db.one, "outbox") != 1 {
		t.Fatalf("duplicate put wrote another outbox row")
	}
	if _, err := st.GetSelfBill(tenant.With(context.Background(), principal(2)), invoiceID); !errors.Is(err, store.ErrSelfBillNotFound) {
		t.Fatalf("another tenant read the self-bill: %v", err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	if _, err := st.GetSelfBill(tenant.With(context.Background(), otherEntity), invoiceID); !errors.Is(err, store.ErrSelfBillNotFound) {
		t.Fatalf("another legal entity read the self-bill: %v", err)
	}
}
