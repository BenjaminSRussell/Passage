package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// repriceOrder is a PO of three 100 EA lines: line 1 priced from trade
// agreement t1 at 10.00, line 2 manual at 11.00, line 3 from t1 at 10.00.
func repriceOrder(t *testing.T, vendor, t1 string, changeManagement bool) store.PurchaseOrder {
	t.Helper()
	trade := domain.PriceSourceTradePrefix + t1
	po := store.PurchaseOrder{LegalEntityID: legalEntity, VendorID: vendor, Currency: "USD", ChangeManagement: changeManagement}
	for i, l := range []struct{ price, source string }{{"10", trade}, {"11", domain.PriceSourceManual}, {"10", trade}} {
		po.Lines = append(po.Lines, store.PurchaseOrderLine{LineNo: i + 1, ProductID: ids.New(), Qty: dec("100"), Unit: "EA",
			UnitPrice: dec(l.price), PriceUnit: dec("1"), DiscountPct: decimal.Zero, TaxAmount: decimal.Zero,
			DeliveryDate: day(t, "2027-01-05"), PriceSource: l.source})
	}
	return priced(t, po)
}

// priced fills line amounts and totals the way the API does.
func priced(t *testing.T, po store.PurchaseOrder) store.PurchaseOrder {
	t.Helper()
	inputs := make([]domain.LineInput, 0, len(po.Lines))
	for _, l := range po.Lines {
		inputs = append(inputs, domain.LineInput{Qty: l.Qty, UnitPrice: l.UnitPrice, PriceUnit: l.PriceUnit, DiscountPct: l.DiscountPct, Tax: l.TaxAmount})
	}
	doc, err := domain.Price(inputs, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	for i := range po.Lines {
		po.Lines[i].GrossAmount, po.Lines[i].DiscountAmount = doc.Lines[i].Gross, doc.Lines[i].Discount
		po.Lines[i].LineAmount, po.Lines[i].TaxAmount = doc.Lines[i].Amount, doc.Lines[i].Tax
	}
	po.Subtotal, po.DiscountTotal, po.ChargesTotal = doc.Totals.Subtotal, doc.Totals.DiscountTotal, doc.Totals.ChargesTotal
	po.TaxTotal, po.Total = doc.Totals.TaxTotal, doc.Totals.Total
	return po
}

// planAt plans a run on cur where every trade agreement line's current winner
// is t2 at price, and builds the next document as the API does.
func planAt(t *testing.T, cur store.PurchaseOrder, t2, price string) (domain.RepricePlan, store.PurchaseOrder) {
	t.Helper()
	lines := []domain.RepriceLine{}
	for _, l := range cur.Lines {
		lines = append(lines, domain.RepriceLine{LineNo: l.LineNo, ProductID: l.ProductID, Unit: l.Unit, PriceSource: l.PriceSource,
			InvoicedQty: l.InvoicedQty, HandedOver: l.InvoicedRepriceSource,
			Input: domain.LineInput{Qty: l.Qty, UnitPrice: l.UnitPrice, PriceUnit: l.PriceUnit, DiscountPct: l.DiscountPct, Tax: l.TaxAmount}})
	}
	plan, err := domain.PlanReprice(lines, func(domain.RepriceLine) (domain.RepriceQuote, bool) {
		return domain.RepriceQuote{Price: dec(price), PriceUnit: dec("1"), Source: domain.PriceSourceTradePrefix + t2}, true
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	next := cur
	next.Lines = append([]store.PurchaseOrderLine(nil), cur.Lines...)
	for _, c := range plan.Changes {
		l := &next.Lines[c.LineNo-1]
		l.UnitPrice, l.PriceUnit, l.PriceSource = c.NewPrice, c.NewPriceUnit, c.NewSource
	}
	return plan, priced(t, next)
}

func setInvoiced(t *testing.T, ctx context.Context, pool *pgxpool.Pool, poID string, lineNo int, qty string) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update purchase_order_line set received_qty = $3, invoiced_qty = $3
			where purchase_order_id = $1 and line_no = $2`, poID, lineNo, qty)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// AC-17.16 against Postgres: T1 10.00 → T2 9.50. The open line moves in
// version 2 (patch /lines/0/unitPrice), the manual line is untouched, the
// invoiced line keeps 10.00 and is handed to payables once, and po.repriced is
// written with the version in the same transaction. A replayed key writes
// nothing; a stale If-Match is PRC-PO-009.
func TestReprice_AppliesAsVersionAndHandsInvoicedLinesOver(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(61)
	t1, t2 := ids.New(), ids.New()
	cur := confirmedPO(t, ctx, s, repriceOrder(t, ids.New(), t1, true))
	setInvoiced(t, ctx, pool, cur.ID, 3, "40")
	cur, err := s.GetPurchaseOrder(ctx, cur.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := len(outbox(t, ctx, pool))

	plan, next := planAt(t, cur, t2, "9.5")
	if len(plan.Changes) != 1 || len(plan.Invoiced) != 1 {
		t.Fatalf("plan %+v", plan)
	}
	key := store.Idem{Key: "reprice-1", Hash: "h"}
	out, err := s.RepricePurchaseOrder(ctx, cur.ID, cur.RowVersion, next, plan, key)
	if err != nil {
		t.Fatal(err)
	}
	if out.Replayed || out.ReapprovalRequired || out.VersionNo != 2 || out.Order.ApprovalStatus != domain.POStatusConfirmed ||
		out.Order.VersionNo != 2 {
		t.Fatalf("outcome %+v (status %s v%d)", out, out.Order.ApprovalStatus, out.Order.VersionNo)
	}
	l1, l2, l3 := out.Order.Lines[0], out.Order.Lines[1], out.Order.Lines[2]
	if !l1.UnitPrice.Equal(dec("9.5")) || l1.PriceSource != domain.PriceSourceTradePrefix+t2 || !l1.LineAmount.Equal(dec("950")) {
		t.Fatalf("line 1 = %s %s %s", l1.UnitPrice, l1.PriceSource, l1.LineAmount)
	}
	if !l2.UnitPrice.Equal(dec("11")) || !l3.UnitPrice.Equal(dec("10")) || l3.PriceSource != domain.PriceSourceTradePrefix+t1 {
		t.Fatalf("lines 2/3 moved: %s, %s %s", l2.UnitPrice, l3.UnitPrice, l3.PriceSource)
	}
	if l3.InvoicedRepriceSource != domain.PriceSourceTradePrefix+t2 || l1.InvoicedRepriceSource != "" {
		t.Fatalf("hand-over marks: line 3 %q, line 1 %q", l3.InvoicedRepriceSource, l1.InvoicedRepriceSource)
	}
	if !out.Order.Total.Equal(dec("3050")) {
		t.Fatalf("total %s, want 950 + 1100 + 1000", out.Order.Total)
	}
	v2, err := s.GetPurchaseOrderVersion(ctx, cur.ID, 2)
	if err != nil || v2.ConfirmedAt == nil || !hasPatchPath(v2.Patch, "/lines/0/unitPrice") || hasPatchPath(v2.Patch, "/lines/2/unitPrice") {
		t.Fatalf("version 2 = %+v, %v", v2, err)
	}

	rows := outbox(t, ctx, pool)[before:]
	if got := eventTypes(rows); len(got) != 3 || got[0] != purchasingevents.POConfirmed || got[1] != purchasingevents.POVersionCreated ||
		got[2] != purchasingevents.PORepriced {
		t.Fatalf("outbox after reprice = %v", got)
	}
	ev := rows[2].Data
	lines, _ := ev["lines"].([]any)
	invoiced, _ := ev["invoiced_lines"].([]any)
	if ev["version_no"] != float64(2) || ev["reapproval_required"] != false || ev["total_delta"] != "-50" || len(lines) != 1 || len(invoiced) != 1 {
		t.Fatalf("po.repriced = %+v", ev)
	}
	if inv := invoiced[0].(map[string]any); inv["line_no"] != float64(3) || inv["qty"] != "40" || inv["delta"] != "-20" || inv["old_price"] != "10" {
		t.Fatalf("invoiced line = %+v", inv)
	}

	// A replayed key returns the PO as it is and writes nothing.
	again, err := s.RepricePurchaseOrder(ctx, cur.ID, cur.RowVersion, next, plan, key)
	if err != nil || !again.Replayed || again.Order.RowVersion != out.Order.RowVersion {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	// A new key on the old version is stale.
	if _, err := s.RepricePurchaseOrder(ctx, cur.ID, cur.RowVersion, next, plan, store.Idem{Key: "reprice-2", Hash: "h"}); problemCode(err) != store.CodeStaleVersion {
		t.Fatalf("stale reprice = %v", err)
	}
	if got := len(outbox(t, ctx, pool)); got != before+3 {
		t.Fatalf("outbox grew to %d after replay and stale call", got-before)
	}

	// The next run moves nothing and does not hand line 3 over again.
	replan, _ := planAt(t, out.Order, t2, "9.5")
	if len(replan.Changes) != 0 || len(replan.Invoiced) != 0 {
		t.Fatalf("second run plans %+v", replan)
	}
}

// A price increase past 5 % on a change-managed PO waits for re-approval: the
// version is pending, the PO is IN_REVIEW, no po.confirmed is written, and
// po.repriced says the version needs re-approval. A PO no longer CONFIRMED
// cannot be repriced.
func TestReprice_IncreaseWaitsForReapproval(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(62)
	t1, t2 := ids.New(), ids.New()
	cur := confirmedPO(t, ctx, s, repriceOrder(t, ids.New(), t1, true))
	before := len(outbox(t, ctx, pool))
	plan, next := planAt(t, cur, t2, "11")
	out, err := s.RepricePurchaseOrder(ctx, cur.ID, cur.RowVersion, next, plan, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.ReapprovalRequired || out.VersionNo != 2 || out.Order.ApprovalStatus != domain.POStatusInReview || out.Order.VersionNo != 1 {
		t.Fatalf("outcome %+v (status %s v%d)", out, out.Order.ApprovalStatus, out.Order.VersionNo)
	}
	rows := outbox(t, ctx, pool)[before:]
	if got := eventTypes(rows); len(got) != 1 || got[0] != purchasingevents.PORepriced || rows[0].Data["reapproval_required"] != true {
		t.Fatalf("outbox = %v %+v", got, rows)
	}
	if _, err := s.RepricePurchaseOrder(ctx, cur.ID, out.Order.RowVersion, next, plan, store.Idem{}); problemCode(err) != domain.CodePOState {
		t.Fatalf("reprice of an IN_REVIEW PO = %v", err)
	}
}

// The run visits only CONFIRMED POs of the tenant with an open, unlinked line
// priced from a trade agreement, filtered by vendor and price row, in id order.
func TestRepriceCandidates_FiltersAndPages(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(63)
	vendor, t1 := ids.New(), ids.New()
	a := confirmedPO(t, ctx, s, repriceOrder(t, vendor, t1, false))
	b := confirmedPO(t, ctx, s, repriceOrder(t, vendor, t1, false))
	if _, err := s.CreatePurchaseOrder(ctx, repriceOrder(t, vendor, t1, false), store.Idem{}); err != nil { // DRAFT
		t.Fatal(err)
	}
	confirmedPO(t, ctx, s, repriceOrder(t, ids.New(), t1, false))     // other vendor
	confirmedPO(t, ctx, s, repriceOrder(t, vendor, ids.New(), false)) // other price row
	if _, err := s.CreatePurchaseOrder(principalContext(64), repriceOrder(t, vendor, t1, false), store.Idem{}); err != nil {
		t.Fatal(err)
	}

	page, err := s.RepriceCandidates(ctx, store.RepriceFilter{VendorID: vendor, TradeAgreementID: t1}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	first, second := a.ID, b.ID
	if second < first {
		first, second = second, first
	}
	if len(page.Items) != 1 || page.Items[0] != first || page.Next != first {
		t.Fatalf("first page = %+v, want [%s]", page, first)
	}
	page, err = s.RepriceCandidates(ctx, store.RepriceFilter{VendorID: vendor, TradeAgreementID: t1}, page.Next, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0] != second || page.Next != "" {
		t.Fatalf("second page = %+v, %v", page, err)
	}
	all, err := s.RepriceCandidates(ctx, store.RepriceFilter{VendorID: vendor}, "", 10)
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("vendor filter only = %+v, %v", all, err)
	}
}
