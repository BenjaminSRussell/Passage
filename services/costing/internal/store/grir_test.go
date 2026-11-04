package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/costing/internal/domain"
)

func TestPostgres_SplitInvoicesClearTheReceiptsGRIRByRemainder(t *testing.T) {
	s, app := setup(t)
	ctx := scopedCtx(testkit.TenantID(8))
	le, product, site := ids.New(), ids.New(), ids.New()
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{costRow("gr", le, product, site, "FINANCIAL", "3", "0.93", "2026-03-05")}); err != nil {
		t.Fatal(err)
	}
	receiptKey := "ppv:RECEIPT:" + le + ":GR-9:1"
	receipt := costRow(receiptKey, le, product, site, "PPV", "3", "0.07", "2026-03-05")
	receipt.GrirAmount = "1.00" // round(3 x 0.335)
	if _, err := s.RecordCostTransactions(ctx, []CostTransaction{receipt}); err != nil {
		t.Fatal(err)
	}
	dec := decimal.RequireFromString
	invoice := func(doc, qty string) InvoicePPV {
		return InvoicePPV{Row: costRow("ppv:INVOICE:"+le+":"+doc+":1", le, product, site, "PPV", "0", "0", "2026-03-06"),
			ReceiptKey: receiptKey, Qty: dec(qty), InvoiceUnit: dec("0.345"), POUnit: dec("0.335"), Scale: 2}
	}
	first, fresh, err := s.RecordInvoicePPV(ctx, invoice("INV-1", "1"))
	if err != nil || !fresh || first.GrirAmount != "0.34" || first.Amount != "0.00" {
		t.Fatalf("first invoice = %+v fresh %v, %v; want GR/IR 0.34, PPV 0.00", first, fresh, err)
	}
	second, fresh, err := s.RecordInvoicePPV(ctx, invoice("INV-2", "2"))
	if err != nil || !fresh || second.GrirAmount != "0.66" || second.Amount != "0.03" {
		t.Fatalf("second invoice = %+v fresh %v, %v; want GR/IR 0.66 (1.00 less 0.34), PPV 0.03", second, fresh, err)
	}
	replay, fresh, err := s.RecordInvoicePPV(ctx, invoice("INV-2", "2"))
	if err != nil || fresh || !dec(replay.GrirAmount).Equal(dec("0.66")) || !dec(replay.Amount).Equal(dec("0.03")) {
		t.Fatalf("replay = %+v fresh %v, %v; want the stored row", replay, fresh, err)
	}
	var cleared string
	query := func(sql string, dst any, args ...any) error {
		return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error { return tx.QueryRow(ctx, sql, args...).Scan(dst) })
	}
	if err := query(`select sum(gr_ir_amount)::text from cost_transaction where grir_receipt_key = $1`, &cleared, receiptKey); err != nil {
		t.Fatal(err)
	}
	if !dec(cleared).Equal(dec("1.00")) {
		t.Fatalf("invoices cleared %s of GR/IR 1.00", cleared)
	}
	otherQty := invoice("INV-2", "1")
	if _, _, err := s.RecordInvoicePPV(ctx, otherQty); problemCode(err) != CodeCostTxnConflict {
		t.Errorf("invoice line replayed with another quantity: %v, want %s", err, CodeCostTxnConflict)
	}
	isAllocation := func(err error) bool {
		var de *domain.Error
		return errors.As(err, &de) && de.Code == domain.CodeGRIRAllocation
	}
	if _, _, err := s.RecordInvoicePPV(ctx, invoice("INV-3", "0.5")); !isAllocation(err) {
		t.Errorf("invoice beyond the received quantity: %v", err)
	}
	unknown := invoice("INV-4", "1")
	unknown.ReceiptKey = "ppv:RECEIPT:" + le + ":GR-0:1"
	if _, _, err := s.RecordInvoicePPV(ctx, unknown); !isAllocation(err) {
		t.Errorf("invoice against an unrecorded receipt: %v", err)
	}
	otherProduct := invoice("INV-5", "1")
	otherProduct.Row.ProductID = ids.New()
	if _, _, err := s.RecordInvoicePPV(ctx, otherProduct); !isAllocation(err) {
		t.Errorf("invoice for another product: %v", err)
	}
	var rows int
	if err := query(`select count(*) from cost_transaction where grir_receipt_key is not null`, &rows); err != nil || rows != 2 {
		t.Fatalf("allocated rows = %d, %v; want the two invoices", rows, err)
	}
}
