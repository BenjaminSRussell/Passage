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

func TestActiveRule_TakesTheLatestEffectiveVersionOnOrBeforeTheDate(t *testing.T) {
	ctx, st, _, rule := setup(t)
	if _, ok, err := st.ActiveRule(ctx, rule.LegalEntityID, "2025-12-31"); err != nil || ok {
		t.Fatalf("before the first rule: ok %v, %v; want none", ok, err)
	}
	got, ok, err := st.ActiveRule(ctx, rule.LegalEntityID, "2026-06-01")
	if err != nil || !ok || got.ID != rule.ID {
		t.Fatalf("2026-06-01 = %+v, %v, %v; want %s", got, ok, err, rule.ID)
	}
	_, raw, err := st.CreateRule(ctx, "rule-2", "h-rule-2", NewRule{LegalEntityID: rule.LegalEntityID, Code: "DEPT", WarnPct: d("0"), BlockPct: d("3"), Currency: "USD", EffectiveFrom: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	var next Rule
	if err := json.Unmarshal(raw, &next); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.ActiveRule(ctx, rule.LegalEntityID, "2026-06-30"); got.ID != rule.ID {
		t.Fatalf("2026-06-30 picked %s, want the first version", got.ID)
	}
	if got, _, _ := st.ActiveRule(ctx, rule.LegalEntityID, "2026-07-01"); got.ID != next.ID {
		t.Fatalf("2026-07-01 picked %s, want version 2", got.ID)
	}
	if _, ok, _ := st.ActiveRule(ctx, ids.New(), "2026-07-01"); ok {
		t.Fatal("another legal entity has no rule")
	}
}

func TestReleasePreEncumbranceByLine_GivesBackOnlyAnOpenPreEncumbrance(t *testing.T) {
	ctx, st, _, rule := setup(t)
	req := requisitionReq(rule.ID, "800")
	lineID := req.Lines[0].LineID
	if _, _, err := st.CheckBudget(ctx, "req-rel", "h", req, false); err != nil {
		t.Fatal(err)
	}
	status, _, err := st.ReleasePreEncumbranceByLine(ctx, "rel-1", "h-rel", lineID, req.DocumentID)
	if err != nil || status != 200 {
		t.Fatalf("release: %d %v", status, err)
	}
	if _, pre := preColumn(t, func() ([]BalanceView, error) { return st.Available(ctx, rule.ID, "dept-10", "FY2026") }); pre != "0.00" {
		t.Fatalf("pre-encumbrances after the release = %s, want 0", pre)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("100000")) {
		t.Fatalf("available after the release = %s, want the full 100000", got)
	}
	if _, _, err := st.ReleasePreEncumbranceByLine(ctx, "rel-2", "h-rel-2", lineID, req.DocumentID); !hasCode(err, CodeNothingToRelease) {
		t.Fatalf("second release: %v, want %s", err, CodeNothingToRelease)
	}
	if _, _, err := st.ReleasePreEncumbranceByLine(ctx, "rel-3", "h-rel-3", ids.New(), req.DocumentID); !hasCode(err, CodeEncumbranceNotFound) {
		t.Fatalf("a line never checked: %v, want %s", err, CodeEncumbranceNotFound)
	}
	// A converted line is a PO line's encumbrance now, so a late release of the requisition line finds nothing.
	req2 := requisitionReq(rule.ID, "300")
	_, raw, err := st.CheckBudget(ctx, "req-rel-2", "h", req2, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ConvertPreEncumbrance(ctx, "conv-rel", "h-conv", ConvertRequest{EncumbranceID: decodeCheck(t, raw).Lines[0].EncumbranceID, PurchaseOrderID: ids.New(), PurchaseOrderLineID: ids.New()}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReleasePreEncumbranceByLine(ctx, "rel-4", "h-rel-4", req2.Lines[0].LineID, req2.DocumentID); !hasCode(err, CodeEncumbranceNotFound) {
		t.Fatalf("a converted line: %v, want %s", err, CodeEncumbranceNotFound)
	}
}
