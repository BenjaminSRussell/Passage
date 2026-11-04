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

func selfBillBuilder(t *testing.T, db settlementDB, inv store.Invoice) store.SelfBillBuild {
	return func(number string) (store.SelfBill, events.Event, error) {
		evt, err := events.New("dev.passage.receiving.ersselfbill.issued.v1", "/passage/receiving/receiving", inv.ID, testkit.TenantID(1), map[string]string{"invoice_id": inv.ID, "document_number": number})
		if err != nil {
			t.Fatal(err)
		}
		return store.SelfBill{
			Number: number, InvoiceID: inv.ID, LegalEntityID: inv.LegalEntityID, VendorID: inv.VendorID, InvoiceNumber: inv.InvoiceNumber,
			ContentType: "application/pdf", Body: []byte("%PDF-1.4 " + number), TaxCode: "VAT19", TaxAmount: decimal.RequireFromString("1.9"), Total: decimal.RequireFromString("11.9"),
		}, evt, nil
	}
}

func TestSelfBill_IsWrittenInTheReleaseTransactionWithAGaplessNumber(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	held := fullInvoice(db, "00000000-0000-0000-0000-00000000c500", "ERS-RCV-SB", domain.InvoiceSourceERS)
	if _, err := st.PostInvoice(ctx, held, holdFunc(t, db, held, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSelfBill(ctx, held.ID); !errors.Is(err, store.ErrSelfBillNotFound) {
		t.Fatalf("a blocked invoice already has a self-bill: %v", err)
	}
	boom := errors.New("party master down")
	_, err := st.ReleaseInvoice(ctx, held.ID, db.one.LegalEntityID, "bob", func(store.Invoice) (store.InvoiceOutcome, error) {
		return store.InvoiceOutcome{
			LedgerEntryID: "released", Total: decimal.NewFromInt(10), Event: event(t, db.one, "vendorinvoice", "posted"),
			SelfBill: func(string) (store.SelfBill, events.Event, error) { return store.SelfBill{}, events.Event{}, boom },
		}, nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("release with a failing self-bill: %v", err)
	}
	if got := scalar(t, db, `select status from receiving_vendor_invoice where invoice_number = 'ERS-RCV-SB'`); got != "AUDIT_BLOCKED" {
		t.Fatalf("the invoice left AUDIT_BLOCKED (%s) although its self-bill failed", got)
	}
	released, err := st.ReleaseInvoice(ctx, held.ID, db.one.LegalEntityID, "bob", func(inv store.Invoice) (store.InvoiceOutcome, error) {
		return store.InvoiceOutcome{LedgerEntryID: "released", Total: decimal.NewFromInt(10), Event: event(t, db.one, "vendorinvoice", "posted"), SelfBill: selfBillBuilder(t, db, inv)}, nil
	})
	if err != nil || released.Status != domain.InvoiceStatusPosted {
		t.Fatalf("release %+v %v", released, err)
	}
	got, err := st.GetSelfBill(ctx, held.ID)
	if err != nil || got.Number != "SB-00000001" || got.InvoiceNumber != "ERS-RCV-SB" || string(got.Body) != "%PDF-1.4 SB-00000001" || got.TaxCode != "VAT19" {
		t.Fatalf("got %+v %v", got, err)
	}
	if n := scalar(t, db, `select count(*) from outbox where aggregate_type = 'ersselfbill'`); n != "1" {
		t.Fatalf("%s self-bill outbox rows, want 1", n)
	}
	if _, err := st.GetSelfBill(tenant.With(context.Background(), principal(2)), held.ID); !errors.Is(err, store.ErrSelfBillNotFound) {
		t.Fatalf("another tenant read the self-bill: %v", err)
	}
	otherEntity := tenant.Principal{TenantID: db.one.TenantID, LegalEntityID: testkit.TenantID(8)}
	if _, err := st.GetSelfBill(tenant.With(context.Background(), otherEntity), held.ID); !errors.Is(err, store.ErrSelfBillNotFound) {
		t.Fatalf("another legal entity read the self-bill: %v", err)
	}
}

func TestPostInvoice_ASelfBillFailureRollsTheInvoiceBack(t *testing.T) {
	db := seedInvoice(t)
	seedOpenGrir(t, db)
	st := store.New(db.pool)
	ctx := tenant.With(context.Background(), db.one)
	in := fullInvoice(db, "00000000-0000-0000-0000-00000000c501", "ERS-RCV-RB", domain.InvoiceSourceERS)
	boom := errors.New("render failed")
	fn := holdFunc(t, db, in, false)
	_, err := st.PostInvoice(ctx, in, func(items []*domain.GrirItem) (store.InvoiceOutcome, error) {
		out, err := fn(items)
		out.SelfBill = func(string) (store.SelfBill, events.Event, error) { return store.SelfBill{}, events.Event{}, boom }
		return out, err
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if n := scalar(t, db, `select count(*) from receiving_vendor_invoice where invoice_number = 'ERS-RCV-RB'`); n != "0" {
		t.Fatalf("the invoice stayed posted without its self-bill")
	}
}
