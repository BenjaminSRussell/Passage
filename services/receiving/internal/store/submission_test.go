package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

func qty3Receipt(t *testing.T, e env, p tenant.Principal, slip string, mutate func(*store.Receipt)) store.Receipt {
	t.Helper()
	in := arrival(p, slip)
	if mutate != nil {
		mutate(&in)
	}
	r, err := e.st.DirectReceipt(ctxOf(p), in, registerWith(slip+"-1", slip+"-2", slip+"-3"))
	if err != nil {
		t.Fatal(err)
	}
	if r, err = e.st.Post(ctxOf(p), r.ID, postWith(t, p, "entry-"+slip)); err != nil {
		t.Fatal(err)
	}
	return r
}

func submission(p tenant.Principal, vendor, number string, lines ...domain.SubmissionLine) store.Submission {
	total := decimal.Zero
	for i := range lines {
		lines[i].Amount = lines[i].Qty.Mul(lines[i].UnitPrice)
		total = total.Add(lines[i].Amount)
	}
	return store.Submission{ID: uuid.NewString(), LegalEntityID: p.LegalEntityID, VendorID: vendor, InvoiceNumber: number, InvoiceDate: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
		Currency: "USD", Total: total, Lines: lines, CreatedBy: "vendor-user"}
}

func billing(receiptID string, qty string) domain.SubmissionLine {
	return domain.SubmissionLine{ReceiptID: receiptID, ReceiptLineNo: 1, Qty: decimal.RequireFromString(qty), UnitPrice: decimal.RequireFromString("2.5")}
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want a domain error, got %v", err)
	}
	return de.Code
}

func TestSubmission_OnlyTheVendorsOwnPostedUninvoicedLinesCanBeBilledAndQuantitiesAreHeld(t *testing.T) {
	e := setup(t)
	p := principal(1)
	ctx := ctxOf(p)
	mine := qty3Receipt(t, e, p, "PS-1", nil)
	theirs := qty3Receipt(t, e, p, "PS-2", func(r *store.Receipt) { r.VendorID, r.PurchaseOrderID = otherVendorID, otherPOID })

	lines, err := e.st.InvoiceableLines(ctx, vendorID, "", 50)
	if err != nil || len(lines) != 1 || lines[0].ReceiptID != mine.ID || !lines[0].AvailableQty.Equal(decimal.NewFromInt(3)) || lines[0].PackingSlip != "PS-1" || lines[0].Currency != "USD" {
		t.Fatalf("invoiceable lines for the vendor: %+v %v", lines, err)
	}
	if other, _ := e.st.InvoiceableLines(ctx, otherVendorID, poID, 50); len(other) != 0 {
		t.Errorf("the other vendor sees %d of the first vendor's lines through a PO filter", len(other))
	}

	// Another vendor's line, an unknown line: the same refusal, so nothing is learned about either.
	for name, line := range map[string]domain.SubmissionLine{"another vendor's line": billing(theirs.ID, "1"), "unknown line": billing(uuid.NewString(), "1")} {
		if _, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-X", line)); codeOf(t, err) != domain.CodeSubmissionLine {
			t.Errorf("%s: %v", name, err)
		}
	}
	wrongCurrency := submission(p, vendorID, "INV-EUR", billing(mine.ID, "1"))
	wrongCurrency.Currency = "EUR"
	if _, err := e.st.CreateSubmission(ctx, wrongCurrency); codeOf(t, err) != domain.CodeSubmissionLine {
		t.Errorf("wrong currency: %v", err)
	}

	first, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-1", billing(mine.ID, "2")))
	if err != nil || first.Status != domain.SubmissionSubmitted || len(first.Lines) != 1 || !first.Total.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("submit %+v %v", first, err)
	}
	// The two units are held: only one is left for another submission.
	if _, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-2", billing(mine.ID, "2"))); codeOf(t, err) != domain.CodeSubmissionOverQty {
		t.Errorf("billing held quantity again: %v", err)
	}
	if left, _ := e.st.InvoiceableLines(ctx, vendorID, "", 50); len(left) != 1 || !left[0].AvailableQty.Equal(decimal.NewFromInt(1)) || !left[0].ReservedQty.Equal(decimal.NewFromInt(2)) {
		t.Errorf("after one submission: %+v", left)
	}
	if _, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "inv-1", billing(mine.ID, "1"))); codeOf(t, err) != domain.CodeSubmissionDuplicate {
		t.Errorf("the same invoice number: %v", err)
	}
	second, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-2", billing(mine.ID, "1")))
	if err != nil {
		t.Fatalf("the last unit: %v", err)
	}
	if left, _ := e.st.InvoiceableLines(ctx, vendorID, "", 50); len(left) != 0 {
		t.Errorf("fully held line still listed: %+v", left)
	}

	// Withdrawing frees the quantity; a vendor cannot touch another vendor's submission.
	if _, err := e.st.WithdrawSubmission(ctx, first.ID, otherVendorID); !errors.Is(err, store.ErrSubmissionNotFound) {
		t.Errorf("another vendor withdraws: %v", err)
	}
	if w, err := e.st.WithdrawSubmission(ctx, first.ID, vendorID); err != nil || w.Status != domain.SubmissionWithdrawn {
		t.Fatalf("withdraw %+v %v", w, err)
	}
	if _, err := e.st.WithdrawSubmission(ctx, first.ID, vendorID); codeOf(t, err) != domain.CodeSubmissionState {
		t.Errorf("withdrawing twice: %v", err)
	}
	if left, _ := e.st.InvoiceableLines(ctx, vendorID, "", 50); len(left) != 1 || !left[0].AvailableQty.Equal(decimal.NewFromInt(2)) {
		t.Errorf("after the withdrawal: %+v", left)
	}
	// A withdrawn invoice number can be used again.
	if _, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-1", billing(mine.ID, "2"))); err != nil {
		t.Errorf("resubmitting after a withdrawal: %v", err)
	}

	// Lists are the vendor's own.
	if mineList, _ := e.st.ListSubmissions(ctx, store.SubmissionFilter{VendorID: vendorID, Limit: 50}); len(mineList) != 3 {
		t.Errorf("vendor lists %d submissions, want 3", len(mineList))
	}
	if other, _ := e.st.ListSubmissions(ctx, store.SubmissionFilter{VendorID: otherVendorID, Limit: 50}); len(other) != 0 {
		t.Errorf("another vendor lists %d submissions", len(other))
	}
	_ = second
}

func TestSubmission_APClosesASubmissionWithTheInvoiceItKeyed(t *testing.T) {
	e := setup(t)
	p := principal(1)
	ctx := ctxOf(p)
	r := qty3Receipt(t, e, p, "PS-1", nil)
	s, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-9", billing(r.ID, "3")))
	if err != nil {
		t.Fatal(err)
	}
	invoice := func(id, vendor string) {
		t.Helper()
		err := dbx.WithTenantTx(ctx, e.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into receiving_vendor_invoice (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total, status)
				values ($1, $2, $3, $4, $5, current_date, current_date, 'USD', $6, $6, 'e1', 7.5, 'POSTED')`, p.TenantID, id, p.LegalEntityID, vendor, "K-"+id[30:], vendorID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	otherInvoice, mineInvoice := uuid.NewString(), uuid.NewString()
	invoice(otherInvoice, otherVendorID)
	invoice(mineInvoice, vendorID)
	if _, err := e.st.DecideSubmission(ctx, s.ID, "ap-clerk", domain.SubmissionPosted, "", otherInvoice); codeOf(t, err) != domain.CodeSubmissionInvalid {
		t.Errorf("closed with another vendor's invoice: %v", err)
	}
	d, err := e.st.DecideSubmission(ctx, s.ID, "ap-clerk", domain.SubmissionPosted, "", mineInvoice)
	if err != nil || d.Status != domain.SubmissionPosted || d.InvoiceID != mineInvoice || d.DecidedBy != "ap-clerk" {
		t.Fatalf("post %+v %v", d, err)
	}
	// A posted submission keeps its invoice number taken.
	if _, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-9", billing(r.ID, "1"))); codeOf(t, err) != domain.CodeSubmissionDuplicate {
		t.Errorf("reusing a posted submission's number: %v", err)
	}
}

func TestSubmission_BuyerRejectsWithANoteAndNoOtherLegalEntitySeesIt(t *testing.T) {
	e := setup(t)
	p := principal(1)
	ctx := ctxOf(p)
	r := qty3Receipt(t, e, p, "PS-1", nil)
	s, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-1", billing(r.ID, "3")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.GetSubmission(ctxOf(tenant.Principal{TenantID: p.TenantID, LegalEntityID: "00000000-0000-0000-0000-0000000000e8"}), s.ID); !errors.Is(err, store.ErrSubmissionNotFound) {
		t.Errorf("another legal entity reads it: %v", err)
	}
	d, err := e.st.DecideSubmission(ctx, s.ID, "ap-clerk", domain.SubmissionRejected, "wrong price", "")
	if err != nil || d.Status != domain.SubmissionRejected || d.DecidedBy != "ap-clerk" || d.DecidedAt == nil || d.DecisionNote != "wrong price" {
		t.Fatalf("reject %+v %v", d, err)
	}
	if _, err := e.st.DecideSubmission(ctx, s.ID, "ap-clerk", domain.SubmissionRejected, "again", ""); codeOf(t, err) != domain.CodeSubmissionState {
		t.Errorf("deciding twice: %v", err)
	}
	if left, _ := e.st.InvoiceableLines(ctx, vendorID, "", 50); len(left) != 1 || !left[0].AvailableQty.Equal(decimal.NewFromInt(3)) {
		t.Errorf("a rejected submission still holds quantity: %+v", left)
	}
	// Posting needs an invoice of the same vendor.
	again, _ := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-3", billing(r.ID, "1")))
	if _, err := e.st.DecideSubmission(ctx, again.ID, "ap-clerk", domain.SubmissionPosted, "", uuid.NewString()); codeOf(t, err) != domain.CodeSubmissionInvalid {
		t.Errorf("posted against an unknown invoice: %v", err)
	}
}

// A submission is closed by an invoice of its own legal entity that is posted or
// held for audit, and an invoice closes at most one submission (#511 review).
func TestSubmission_AnInvoiceClosesOneSubmissionOfItsOwnLegalEntity(t *testing.T) {
	e := setup(t)
	p := principal(1)
	ctx := ctxOf(p)
	r := qty3Receipt(t, e, p, "PS-7", nil)
	first, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-71", billing(r.ID, "1")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.st.CreateSubmission(ctx, submission(p, vendorID, "INV-72", billing(r.ID, "1")))
	if err != nil {
		t.Fatal(err)
	}
	invoice := func(legalEntity, status string) string {
		t.Helper()
		id := uuid.NewString()
		owner := tenant.Principal{TenantID: p.TenantID, LegalEntityID: legalEntity}
		err := dbx.WithTenantTx(ctxOf(owner), e.pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `insert into receiving_vendor_invoice (tenant_id, id, legal_entity_id, vendor_id, invoice_number, invoice_date, posting_date, currency, ledger_id, fiscal_period_id, ledger_entry_id, total, status)
				values ($1, $2, $3, $4, $5, current_date, current_date, 'USD', $6, $6, 'e1', 2.5, $7)`, p.TenantID, id, legalEntity, vendorID, "K-"+id[30:], vendorID, status)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	otherEntity := invoice(uuid.NewString(), "POSTED")
	rejected := invoice(p.LegalEntityID, "AUDIT_REJECTED")
	held := invoice(p.LegalEntityID, "AUDIT_BLOCKED")
	if _, err := e.st.DecideSubmission(ctx, first.ID, "ap-clerk", domain.SubmissionPosted, "", otherEntity); err == nil {
		t.Error("closed with an invoice of another legal entity")
	}
	if _, err := e.st.DecideSubmission(ctx, first.ID, "ap-clerk", domain.SubmissionPosted, "", rejected); codeOf(t, err) != domain.CodeSubmissionInvoiceMismatch {
		t.Errorf("closed with an invoice rejected after audit: %v", err)
	}
	if d, err := e.st.DecideSubmission(ctx, first.ID, "ap-clerk", domain.SubmissionPosted, "", held); err != nil || d.InvoiceID != held {
		t.Fatalf("an invoice held for audit closes the submission: %+v %v", d, err)
	}
	if _, err := e.st.DecideSubmission(ctx, second.ID, "ap-clerk", domain.SubmissionPosted, "", held); codeOf(t, err) != domain.CodeSubmissionInvoiceTaken {
		t.Errorf("the same invoice closed a second submission: %v", err)
	}
	// The index refuses it too, whatever reaches the table.
	err = dbx.WithTenantTx(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update receiving_invoice_submission set invoice_id = $3 where tenant_id = $1 and id = $2`, p.TenantID, second.ID, held)
		return err
	})
	if err == nil {
		t.Error("the unique index let two submissions name one invoice")
	}
}
