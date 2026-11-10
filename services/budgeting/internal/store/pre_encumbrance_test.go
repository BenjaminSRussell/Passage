package store

import (
	"encoding/json"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
)

func requisitionReq(ruleID, amount string) CheckRequest {
	r := checkReq(ruleID, amount)
	r.DocumentType = "Requisition"
	return r
}

func preColumn(t *testing.T, f func() ([]BalanceView, error)) (enc, pre string) {
	t.Helper()
	rows, err := f()
	if err != nil || len(rows) != 1 {
		t.Fatalf("available: %v (%d rows)", err, len(rows))
	}
	return rows[0].Encumbrances.StringFixed(2), rows[0].PreEncumbrances.StringFixed(2)
}

// FR-38.5: a requisition reserves a pre-encumbrance; the PO that confirms it converts it into an encumbrance on its own
// line without moving available funds, and the PO's budget check then revises that line by the price difference.
func TestPreEncumbrance_RequisitionReservesThenPOConverts(t *testing.T) {
	ctx, st, _, rule := setup(t)
	req := requisitionReq(rule.ID, "800")
	_, raw, err := st.CheckBudget(ctx, "req-1", "h", req, false)
	if err != nil {
		t.Fatal(err)
	}
	encID := decodeCheck(t, raw).Lines[0].EncumbranceID
	enc, pre := preColumn(t, func() ([]BalanceView, error) { return st.Available(ctx, rule.ID, "dept-10", "FY2026") })
	if enc != "0.00" || pre != "800.00" {
		t.Fatalf("after the requisition: encumbrances %s, pre-encumbrances %s, want 0 and 800", enc, pre)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("99200")) {
		t.Fatalf("available after the requisition = %s, want 99200", got)
	}
	view, err := st.GetEncumbrance(ctx, encID)
	if err != nil || view.Stage != domain.StagePre || view.SourceType != "Requisition" {
		t.Fatalf("encumbrance = %+v, %v", view, err)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)

	// Until the PO converts it, an invoice cannot relieve it and a PO check on the same line is refused.
	if _, _, err := st.RelieveEncumbrance(ctx, "inv-0", "h", RelieveRequest{EncumbranceID: encID, InvoiceID: ids.New(), InvoiceAmount: d("10"), MatchedShare: d("10")}); !hasCode(err, CodeLinePre) {
		t.Fatalf("relief of a pre-encumbrance: %v, want %s", err, CodeLinePre)
	}
	asPO := req
	asPO.DocumentType = "PurchaseOrder"
	if _, _, err := st.CheckBudget(ctx, "po-early", "h", asPO, false); !hasCode(err, CodeLinePre) {
		t.Fatalf("PO check on a requisition line: %v, want %s", err, CodeLinePre)
	}

	poID, poLine := ids.New(), ids.New()
	status, raw, err := st.ConvertPreEncumbrance(ctx, "conv-1", "h-conv", ConvertRequest{EncumbranceID: encID, PurchaseOrderID: poID, PurchaseOrderLineID: poLine})
	if err != nil || status != 200 {
		t.Fatalf("convert: %d %v", status, err)
	}
	var out ConvertResponse
	if err := json.Unmarshal(raw, &out); err != nil || !out.Converted.Equal(d("800")) {
		t.Fatalf("convert = %+v (%v)", out, err)
	}
	enc, pre = preColumn(t, func() ([]BalanceView, error) { return st.Available(ctx, rule.ID, "dept-10", "FY2026") })
	if enc != "800.00" || pre != "0.00" {
		t.Fatalf("after the conversion: encumbrances %s, pre-encumbrances %s, want 800 and 0", enc, pre)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("99200")) {
		t.Fatalf("available must not move on conversion, got %s", got)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)
	// A replay with the same key moves nothing; a second conversion is refused.
	if _, _, err := st.ConvertPreEncumbrance(ctx, "conv-1", "h-conv", ConvertRequest{EncumbranceID: encID, PurchaseOrderID: poID, PurchaseOrderLineID: poLine}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if _, _, err := st.ConvertPreEncumbrance(ctx, "conv-2", "h", ConvertRequest{EncumbranceID: encID, PurchaseOrderID: poID, PurchaseOrderLineID: ids.New()}); !hasCode(err, CodeNotPre) {
		t.Fatalf("second conversion: %v, want %s", err, CodeNotPre)
	}

	// The PO's own check now revises the line: a price rise of 50 reserves 50 more, as an encumbrance.
	po := CheckRequest{RuleVersionID: rule.ID, DocumentID: poID, DocumentType: "PurchaseOrder", Lines: []CheckLine{{LineID: poLine, DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d("850")}}}
	if _, _, err := st.CheckBudget(ctx, "po-1", "h", po, false); err != nil {
		t.Fatal(err)
	}
	enc, pre = preColumn(t, func() ([]BalanceView, error) { return st.Available(ctx, rule.ID, "dept-10", "FY2026") })
	if enc != "850.00" || pre != "0.00" {
		t.Fatalf("after the PO check: encumbrances %s, pre-encumbrances %s, want 850 and 0", enc, pre)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)
	// From here the line behaves like any encumbrance: an invoice relieves it.
	if _, _, err := st.RelieveEncumbrance(ctx, "inv-1", "h", RelieveRequest{EncumbranceID: encID, InvoiceID: ids.New(), InvoiceAmount: d("100"), MatchedShare: d("100")}); err != nil {
		t.Fatal(err)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)
}

// A cancelled requisition gives its pre-encumbrance back to available funds without touching encumbrances.
func TestPreEncumbrance_ReleaseReturnsFundsFromThePreColumn(t *testing.T) {
	ctx, st, _, rule := setup(t)
	req := requisitionReq(rule.ID, "300")
	_, raw, err := st.CheckBudget(ctx, "req-1", "h", req, false)
	if err != nil {
		t.Fatal(err)
	}
	encID := decodeCheck(t, raw).Lines[0].EncumbranceID
	if _, _, err := st.ReleaseEncumbrance(ctx, "rel-1", "h", ReleaseRequest{EncumbranceID: encID, DocumentID: req.DocumentID, Reason: ReleaseCancelled}); err != nil {
		t.Fatal(err)
	}
	enc, pre := preColumn(t, func() ([]BalanceView, error) { return st.Available(ctx, rule.ID, "dept-10", "FY2026") })
	if enc != "0.00" || pre != "0.00" {
		t.Fatalf("after release: encumbrances %s, pre-encumbrances %s, want 0 and 0", enc, pre)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("100000")) {
		t.Fatalf("available after release = %s, want 100000", got)
	}
	if _, _, err := st.ConvertPreEncumbrance(ctx, "conv-1", "h", ConvertRequest{EncumbranceID: encID, PurchaseOrderID: ids.New(), PurchaseOrderLineID: ids.New()}); !hasCode(err, CodeLineReleased) {
		t.Fatalf("convert of a released line: %v, want %s", err, CodeLineReleased)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)
}
